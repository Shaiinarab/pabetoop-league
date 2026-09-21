package web

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
)

// Flash cookies: written by mutating handlers, read+cleared on the next render.
// Values are Persian user-facing strings; messages never contain internals.
//
// They are percent-encoded because a cookie value may only contain ASCII bytes
// 0x21–0x7e: net/http's sanitiser DROPS every other byte with a warning, which
// silently turned «یک تیم نمیتواند با خودش بازی کند.» into `      .` — the
// operator saw a meaningless message (or, when nothing survived, no message at
// all). Encoding happens here, at the single write/read pair, so every caller
// keeps passing plain Persian text.
const (
	flashMsgCookie  = "flash_msg"
	flashKindCookie = "flash_kind"
	// Request-only mirrors, used so a flash set and rendered in the SAME
	// response is visible. Never sent to the browser.
	flashNowMsgCookie  = "flash_now_msg"
	flashNowKindCookie = "flash_now_kind"

	flashSuccess = "success"
	flashError   = "error"
)

// flashMessage matches admin_base.html's `.Flash` contract (value, never nil,
// so `{{if .Flash.Success}}` is always safe).
type flashMessage struct {
	Success string
	Error   string
}

// breadcrumbItem matches admin_base.html's `.Breadcrumb` contract.
type breadcrumbItem struct {
	Label string
	Href  string
}

// adminPage is the chrome every admin_base-wrapped page carries.
type adminPage struct {
	SiteName     string
	SiteSubtitle string
	ActiveSeason string
	CSRFToken    string
	NavActive    string
	Flash        flashMessage
	Breadcrumb   []breadcrumbItem
}

// adminPageFiles maps logical page names to their template files. Each page is
// parsed as its own clone of admin_base + partials (same block-override wiring
// as the public pages); login.html is standalone by design (no sidebar).
var adminPageFiles = map[string]string{
	"dashboard": "admin/dashboard.html",
	"clubs":     "admin/clubs.html",
	"team_form": "admin/team_form.html",
	"login":     "admin/login.html",
}

// admin template sets are cached per templates directory. The cache is
// package-level (not a Server field) deliberately: Server's struct is TASK-008's
// frozen shape and TASK-010's allowlist must not restructure it.
var (
	adminTmplMu    sync.Mutex
	adminTmplCache = map[string]map[string]*template.Template{}
	adminTmplErr   = map[string]error{}
)

// adminPageSet returns (and caches) the parsed admin template sets.
func adminPageSet(dir string) (map[string]*template.Template, error) {
	adminTmplMu.Lock()
	defer adminTmplMu.Unlock()

	if pages, ok := adminTmplCache[dir]; ok {
		return pages, nil
	}
	if err := adminTmplErr[dir]; err != nil {
		return nil, err
	}

	fsys := os.DirFS(dir)
	funcs := templateFuncMap(nil)

	base, err := template.New("adminbase").Funcs(funcs).ParseFS(fsys, "admin_base.html", "partials/*.html")
	if err != nil {
		adminTmplErr[dir] = err
		return nil, err
	}

	pages := map[string]*template.Template{}
	for name, file := range adminPageFiles {
		clone, err := base.Clone()
		if err != nil {
			adminTmplErr[dir] = err
			return nil, err
		}
		if file == "admin/login.html" {
			// Standalone page: re-parse it alone so it carries no base blocks.
			standalone, err := template.New("login").Funcs(funcs).ParseFS(fsys, file)
			if err != nil {
				adminTmplErr[dir] = err
				return nil, err
			}
			pages[name] = standalone
			continue
		}
		parsed, err := clone.ParseFS(fsys, file)
		if err != nil {
			adminTmplErr[dir] = err
			return nil, err
		}
		pages[name] = parsed
	}
	adminTmplCache[dir] = pages
	return pages, nil
}

// adminExecName is the template executed for a page: base-wrapped pages render
// through admin_base.html so their title/content blocks resolve.
func adminExecName(page string) string {
	if page == "login" {
		return "login.html"
	}
	return "admin_base.html"
}

// renderAdmin renders an admin page buffer-first (so a late template error still
// yields a Persian 500 instead of a half-written page), then writes it.
func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, page string, data any, status int) {
	if status == 0 {
		status = http.StatusOK
	}
	pages, err := adminPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("admin templates unavailable", "error", err, "page", page)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	tmpl, ok := pages[page]
	if !ok {
		s.log.Error("unknown admin page", "page", page)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, adminExecName(page), data); err != nil {
		s.log.Error("render admin page failed", "page", page, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderAdminPartial renders a named partial (e.g. club_row) from the cached
// admin set for htmx row swaps.
func (s *Server) renderAdminPartial(w http.ResponseWriter, name string, status int, data any) {
	pages, err := adminPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("admin templates unavailable", "error", err, "partial", name)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	tmpl, ok := pages["clubs"]
	if !ok {
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render partial failed", "partial", name, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// ---------- flash cookies ----------

// setFlash stores a one-shot message.
//
// It writes TWO things, and both are needed:
//   - Set-Cookie headers, for the classic redirect-after-post render on the
//     NEXT request (the common path), and
//   - the same values attached to the CURRENT request, because some handlers
//     set a flash and then render in the SAME response (a failed create that
//     re-renders its form). takeFlash reads from the request, and a Set-Cookie
//     header is not visible to the request that wrote it — so without this the
//     operator saw a silent no-op instead of the Persian error.
//     Repro that found it: create club «نمونه ب» twice → 200 with no message.
//     (BUG #2, TASK-018; the two tests guarding it were skipped until this.)
func setFlash(w http.ResponseWriter, r *http.Request, kind, message string) {
	secure := r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name: flashMsgCookie, Value: url.QueryEscape(message), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name: flashKindCookie, Value: kind, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
	// Same-response channel. Distinct names on purpose: the browser may have
	// sent a real flash_msg cookie on this request, and Request.Cookie returns
	// the first match, so reusing the name would be ambiguous.
	r.AddCookie(&http.Cookie{Name: flashNowMsgCookie, Value: url.QueryEscape(message)})
	r.AddCookie(&http.Cookie{Name: flashNowKindCookie, Value: kind})
}

// takeFlash reads and clears the flash cookies (read-once semantics).
func takeFlash(w http.ResponseWriter, r *http.Request) flashMessage {
	var msg flashMessage

	// A flash set earlier in THIS request wins, and must not clear the pending
	// Set-Cookie values (they are still meant for the next request).
	if now, err := r.Cookie(flashNowMsgCookie); err == nil && now.Value != "" {
		value := decodeCookieValue(now.Value)
		if k, err := r.Cookie(flashNowKindCookie); err == nil && k.Value == flashError {
			msg.Error = value
		} else {
			msg.Success = value
		}
		return msg
	}

	c, err := r.Cookie(flashMsgCookie)
	if err != nil || c.Value == "" {
		return msg
	}
	value := decodeCookieValue(c.Value)
	kind := flashSuccess
	if k, err := r.Cookie(flashKindCookie); err == nil && k.Value == flashError {
		kind = flashError
	}
	if kind == flashError {
		msg.Error = value
	} else {
		msg.Success = value
	}
	// Clear both cookies so the message shows exactly once.
	for _, name := range []string{flashMsgCookie, flashKindCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: -1,
		})
	}
	return msg
}

// decodeCookieValue tolerates an unencoded value (older session, or plain
// ASCII): QueryUnescape leaves a string without escapes untouched. Cookie
// values may only hold bytes 0x21–0x7e, so Persian text MUST be escaped on the
// way in — net/http silently drops the rest with a warning.
func decodeCookieValue(value string) string {
	if decoded, err := url.QueryUnescape(value); err == nil {
		return decoded
	}
	return value
}

// adminChrome builds the shared page chrome for a handler.
func (s *Server) adminChrome(w http.ResponseWriter, r *http.Request, nav string) adminPage {
	p := adminPage{
		SiteName:     SiteName,
		SiteSubtitle: SiteSubtitle,
		CSRFToken:    s.CSRFToken(r),
		NavActive:    nav,
		Flash:        takeFlash(w, r),
	}
	if season, err := s.store.ActiveSeason(); err == nil && season != nil {
		p.ActiveSeason = season.Name
	}
	return p
}

// ---------- small helpers ----------

// pathID parses a numeric path value ({id}) into int64.
func pathID(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// isoDateOrNil normalises an ISO date string pointer, treating "" as nil.
func isoDateOrNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}
