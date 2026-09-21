package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// csp is the Content-Security-Policy applied to every response. htmx 4 and the
// app bundle are self-hosted (no CDN — DECISIONS.md D2), so 'self' suffices;
// inline styles are disallowed by design (main.css is a single linked sheet).
const csp = "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'"

// middleware is the standard shape for HTTP decorators here.
type middleware func(http.Handler) http.Handler

// chain applies middleware in the given order (first argument = outermost).
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// recoverer converts panics into a Persian 500 page plus a structured log entry
// with the stack trace. Users never see stack traces.
func recoverer(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"error", fmt.Sprint(rec),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					writePersianError(w, http.StatusInternalServerError,
						"خطای غیرمنتظره‌ای رخ داد. لطفاً دوباره تلاش کنید.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders sets the baseline security headers on every response.
//
// The set itself lives in security.go (baselineSecurityHeaders) so that the
// middleware which sends the headers and the test which guards them read the
// same list: a header added there is sent, and one removed there fails the
// contract test in security_test.go rather than silently disappearing.
//
// HSTS is deliberately TLS-only — the app ships on plain HTTP behind
// Caddy/Nginx, where a browser ignores HSTS anyway (SECURITY.md §2). Do not
// "fix" this by sending it unconditionally; that would promise TLS for a host
// that may not have it once TLS is terminated at the proxy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for _, sh := range baselineSecurityHeaders() {
			h.Set(sh.name, sh.value)
		}
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures status code and byte count for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// requestLogger logs method, path, status, duration and response size.
// It deliberately logs no bodies, no cookies, no headers, and no query string
// (query strings can carry credentials in login/reset links).
func requestLogger(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", rec.bytes,
			)
		})
	}
}
