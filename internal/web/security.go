package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "session"
	csrfCookie    = "csrf"
	csrfField     = "_csrf"
	csrfHeader    = "X-CSRF-Token"

	sessionTTL     = 12 * time.Hour
	rateLimitMax   = 5                // failures per window per IP
	rateLimitFor   = 15 * time.Minute // window length
	adminLoginPath = "/admin/login"
)

// ---------- response security headers ----------

// permissionsPolicy denies every browser capability this product has no use
// for. It renders tables and forms: no media, no sensors, no camera, no
// microphone, no geolocation, no WebAuthn, no payments.
//
// The directives are named explicitly and each carries an empty allow-list
// because an empty Permissions-Policy is NOT the same as deny-all — a feature
// left unmentioned keeps its default allow-list, so "say nothing" would leave
// the capability available to any script on the page.
const permissionsPolicy = "accelerometer=(), autoplay=(), camera=(), display-capture=(), " +
	"encrypted-media=(), fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), " +
	"microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(), " +
	"screen-wake-lock=(), serial=(), usb=(), xr-spatial-tracking=()"

// hstsValue is the HSTS policy, sent only when the request arrived over TLS.
// Deployment is plain HTTP behind Caddy/Nginx today (SECURITY.md §2): an HSTS
// header from an HTTP origin is ignored by browsers at best, and pinning the
// proxy's scheme at worst. Keep the TLS gate — do not send it unconditionally.
const hstsValue = "max-age=31536000; includeSubDomains"

// securityHeader is one response header the product promises to send.
type securityHeader struct{ name, value string }

// baselineSecurityHeaders is the single source of truth for the
// protocol-independent header set. securityHeaders() in middleware.go applies
// exactly this list, and security_test.go asserts the documented contract —
// the two cannot drift apart, which is the failure TASK-022 was raised for.
// Changing this list means changing SECURITY.md §2 in the same edit.
func baselineSecurityHeaders() []securityHeader {
	return []securityHeader{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "no-referrer"},
		{"Content-Security-Policy", csp},
		{"Permissions-Policy", permissionsPolicy},
	}
}

// ---------- sessions ----------

// sessionPayload is the signed session state (single admin, DECISIONS.md D11).
type sessionPayload struct {
	Admin    bool  `json:"admin"`
	IssuedAt int64 `json:"iat"`
}

// sessionManager signs and verifies session cookies with HMAC-SHA256.
type sessionManager struct {
	secret []byte
}

func newSessionManager(secret []byte) *sessionManager {
	return &sessionManager{secret: secret}
}

func (m *sessionManager) sign(p sessionPayload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// verify returns the payload when the signature and TTL check out.
func (m *sessionManager) verify(v string) (sessionPayload, bool) {
	var p sessionPayload
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return p, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return p, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return p, false
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return p, false
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, false
	}
	if time.Since(time.Unix(p.IssuedAt, 0)) > sessionTTL {
		return p, false
	}
	return p, true
}

// issue writes a fresh session cookie (rotating on login).
func (m *sessionManager) issue(w http.ResponseWriter, r *http.Request, admin bool) {
	val, err := m.sign(sessionPayload{Admin: admin, IssuedAt: time.Now().Unix()})
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		Expires:  time.Now().Add(sessionTTL),
	})
}

func (m *sessionManager) clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})
}

func (m *sessionManager) current(r *http.Request) (sessionPayload, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return sessionPayload{}, false
	}
	return m.verify(c.Value)
}

func (m *sessionManager) isAdmin(r *http.Request) bool {
	p, ok := m.current(r)
	return ok && p.Admin
}

// ---------- CSRF (signed double-submit) ----------

// csrfToken returns the request's CSRF token, creating and setting the signed
// cookie when absent. Templates embed the token server-side (hidden field) and
// htmx sends it as X-CSRF-Token via hx-headers:inherited on <body>.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := hex.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: token})
	return token
}

// CSRFToken is the template-facing accessor (Server method, wired in funcmap
// per request by handlers that render forms).
func (s *Server) CSRFToken(r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil {
		return c.Value
	}
	return ""
}

// verifyCSRF compares the submitted token with the cookie in constant time.
func (s *Server) verifyCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	submitted := r.Header.Get(csrfHeader)
	if submitted == "" {
		submitted = r.PostFormValue(csrfField)
	}
	if submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(c.Value)) == 1
}

// csrfProtect rejects unsafe methods under /admin/ without a valid token.
func (s *Server) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/admin/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.verifyCSRF(r) {
			s.log.Warn("csrf rejected", "method", r.Method, "path", r.URL.Path, "remote", clientIP(r))
			writePersianError(w, http.StatusForbidden, "نشست شما منقضی شده است؛ دوباره وارد شوید.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ensureCSRFCookie issues a token on safe requests so forms can embed it.
func (s *Server) ensureCSRFCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			s.csrfToken(w, r)
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- login rate limiting ----------

// loginLimiter is a deliberately simple in-memory failure counter per IP.
// Tradeoff (documented in internal/web/README.md): per-process state, lost on
// restart — fine for a single-admin MVP; swap for store-backed counting if the
// admin ever runs multi-replica.
type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginBucket
}

type loginBucket struct {
	failures int
	first    time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{buckets: map[string]*loginBucket{}}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		return true
	}
	if time.Since(b.first) > rateLimitFor {
		delete(l.buckets, ip)
		return true
	}
	return b.failures < rateLimitMax
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok || time.Since(b.first) > rateLimitFor {
		l.buckets[ip] = &loginBucket{failures: 1, first: time.Now()}
		return
	}
	b.failures++
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, ip)
}

// ---------- auth handlers ----------

// verifyAdminPassword compares against the configured bcrypt hash. bcrypt's
// compare is constant-time for a fixed hash, so no timing oracle on the digest.
func (s *Server) verifyAdminPassword(password string) bool {
	if len(s.adminHash) == 0 {
		return false
	}
	return bcrypt.CompareHashAndPassword(s.adminHash, []byte(password)) == nil
}

func (s *Server) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	if s.sessions.isAdmin(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := loginTmpl.Execute(w, loginView{Token: s.csrfToken(w, r), SiteName: SiteName}); err != nil {
		s.log.Error("render login", "error", err)
	}
}

// handleLoginPOST: generic Persian error on failure, per-IP rate limit, session
// rotation on success.
func (s *Server) handleLoginPOST(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		s.log.Warn("login rate limited", "remote", ip)
		writePersianError(w, http.StatusTooManyRequests,
			"تلاش‌های ناموفق بیش از حد مجاز؛ ۱۵ دقیقهٔ دیگر دوباره تلاش کنید.")
		return
	}
	if len(s.adminHash) == 0 {
		s.log.Warn("login attempt while ADMIN_PASSWORD is unset")
		writePersianError(w, http.StatusServiceUnavailable,
			"ورود در این نصب فعال نیست؛ گذرواژهٔ مدیر تنظیم نشده است.")
		return
	}
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	if username != s.adminUser || !s.verifyAdminPassword(password) {
		s.limiter.fail(ip)
		s.log.Warn("login failed", "remote", ip)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		if err := loginTmpl.Execute(w, loginView{
			Token:    s.csrfToken(w, r),
			Error:    "نام کاربری یا گذرواژه نادرست است.",
			SiteName: SiteName,
		}); err != nil {
			s.log.Error("render login", "error", err)
		}
		return
	}
	s.limiter.reset(ip)
	s.sessions.issue(w, r, true) // rotate session on privilege change
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleLogout clears the session (POST canonical, GET fallback link).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.clear(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requireAdmin gates /admin/: browsers redirect to login, htmx requests get
// 403 + HX-Redirect (htmx 4 acts on the header without swapping).
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.sessions.isAdmin(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("HX-Request"), "true") {
			w.Header().Set("HX-Redirect", adminLoginPath)
			writePersianError(w, http.StatusForbidden, "برای دسترسی به این بخش باید وارد شوید.")
			return
		}
		if r.Method == http.MethodGet {
			http.Redirect(w, r, adminLoginPath, http.StatusSeeOther)
			return
		}
		writePersianError(w, http.StatusForbidden, "برای دسترسی به این بخش باید وارد شوید.")
	})
}

// ---------- helpers ----------

// clientIP is the Host-stripped RemoteAddr; proxy headers are not trusted in
// the MVP (no reverse proxy in front yet — see PROJECT_SPEC §9 deployment).
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}

var persianErrorTmpl = template.Must(template.New("error").Parse(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>خطا — {{.SiteName}}</title>
<link rel="stylesheet" href="/static/css/main.css"></head>
<body><main class="container">
  <div class="card">
    <div class="card-title">خطا</div>
    <p>{{.Message}}</p>
    <p><a class="btn ghost" href="/">بازگشت به خانه</a></p>
  </div>
</main></body></html>`))

var loginTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ورود مدیر — {{.SiteName}}</title>
<link rel="stylesheet" href="/static/css/main.css"></head>
<body><main class="container">
  <div class="card">
    <div class="card-title">ورود مدیر سامانه</div>
    {{if .Error}}<div class="flash error">{{.Error}}</div>{{end}}
    <form method="post" action="/admin/login">
      <input type="hidden" name="_csrf" value="{{.Token}}">
      <div class="form-row"><div class="field">
        <label for="username">نام کاربری</label>
        <input id="username" name="username" autocomplete="username" required>
      </div></div>
      <div class="form-row"><div class="field">
        <label for="password">گذرواژه</label>
        <input id="password" name="password" type="password" autocomplete="current-password" required>
      </div></div>
      <button class="btn primary large" type="submit">ورود</button>
    </form>
  </div>
</main></body></html>`))

type errorView struct {
	Message  string
	SiteName string
}

type loginView struct {
	Token    string
	Error    string
	SiteName string
}

// writePersianError renders the small Persian error page (no stack traces).
func writePersianError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = persianErrorTmpl.Execute(w, errorView{Message: message, SiteName: SiteName})
}
