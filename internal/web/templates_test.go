package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// storeMatch builds a store.Match fixture without touching the database.
func storeMatch(home, away, status string, date *string, hs, as *int) store.Match {
	return store.Match{
		ID:            1,
		CompetitionID: 1,
		HomeTeamID:    1,
		AwayTeamID:    2,
		HomeTeamName:  home,
		AwayTeamName:  away,
		Status:        status,
		ScheduledDate: date,
		HomeScore:     hs,
		AwayScore:     as,
	}
}

// testConfig points the server at the repo's real assets and a temp secret,
// with a known admin password (TASK-005 templates + TASK-008 security).
func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		TemplatesDir: "../../web/templates",
		StaticDir:    "../../web/static",
		SecretFile:   t.TempDir() + "/secret.key",
		AdminUser:    "admin",
		AdminPass:    "correct horse battery staple",
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := NewWithConfig(nil, testConfig(t))
	if err := s.TemplateErr(); err != nil {
		t.Fatalf("template parse failed: %v", err)
	}
	return s
}

// newStoreTestServer is newTestServer backed by a real, empty, migrated store.
// Needed since the TASK-010/TASK-013 handlers were wired into Handler(): the
// admin dashboard reads the store, so a nil-store server renders 500 there.
func newStoreTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewWithConfig(st, testConfig(t))
	if err := s.TemplateErr(); err != nil {
		t.Fatalf("template parse failed: %v", err)
	}
	return s
}

// ---------- templates ----------

func TestTemplatesParseAndRender(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	for _, path := range []string{"/", "/age/1", "/competition/1"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body: %s)", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			`dir="rtl"`,
			`/static/css/main.css`,
			`/static/js/htmx-4.0.0.min.js`,
			`/static/js/app.js`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s: missing %q in rendered page", path, want)
			}
		}
	}
}

// The base skeleton must wrap page content (block override wiring).
func TestBaseLayoutWrap(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `class="topbar"`) || !strings.Contains(body, `class="footer"`) {
		t.Fatal("base chrome (topbar/footer) missing — page is not wrapped by public_base.html")
	}
	if !strings.Contains(body, "هنوز نتیجه‌ای ثبت نشده است.") {
		t.Error("home empty state missing — content block not overridden by home.html")
	}
}

// A known ISO date must render through the funcmap as ۱۴۰۵/۰۷/۲۰ (D9).
func TestMatchRowRendersJalali(t *testing.T) {
	s := newTestServer(t)
	iso := "2026-10-12"
	home, away := 2, 1
	view := matchRowView{
		Match:    storeMatch("نمونه ب", "نمونه ث", "finished", &iso, &home, &away),
		ShowDate: true,
	}
	rec := httptest.NewRecorder()
	if err := s.pages["home"].ExecuteTemplate(rec, "match_row", view); err != nil {
		t.Fatalf("execute match_row: %v", err)
	}
	body := rec.Body.String()
	if want := jalali.Format(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)); !strings.Contains(body, want) {
		t.Errorf("match_row should contain %q, got: %s", want, body)
	}
	if !strings.Contains(body, "۲") {
		t.Error("score should render as Persian digits")
	}
}

func TestFuncMapNilSafe(t *testing.T) {
	fm := templateFuncMap(nil)
	date := fm["jalaliDate"].(func(*string) string)
	if got := date(nil); got != "" {
		t.Errorf("jalaliDate(nil) = %q, want empty", got)
	}
	bad := "not-a-date"
	if got := date(&bad); got != "" {
		t.Errorf("jalaliDate(bad) = %q, want empty", got)
	}
	good := "2026-10-12"
	if got := date(&good); got != "۱۴۰۵/۰۷/۲۰" {
		t.Errorf("jalaliDate(2026-10-12) = %q, want ۱۴۰۵/۰۷/۲۰", got)
	}
}

// ---------- security ----------

// The response-header contract moved to security_test.go (TASK-028):
// TestSecurityHeadersOnEveryResponse asserts it across every route, including
// the admin login page and unknown routes. TestSecurityHeaders used to live
// here and kept a private copy of four of those expectations, which is how a
// header could go missing without any test noticing.

func TestCSRFRejectsPostWithoutToken(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF token = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "نشست شما منقضی شده است") {
		t.Error("expected Persian CSRF error message")
	}
}

func TestCSRFLoginFlow(t *testing.T) {
	s := newStoreTestServer(t)
	h := s.Handler()

	// GET login → issues the CSRF cookie.
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	var csrf *http.Cookie
	for _, c := range getRec.Result().Cookies() {
		if c.Name == csrfCookie {
			csrf = c
		}
	}
	if csrf == nil || csrf.Value == "" {
		t.Fatal("login page did not issue a CSRF cookie")
	}

	// POST with token → session cookie with the required flags.
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}, csrfField: {csrf.Value}}
	postRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	h.ServeHTTP(postRec, req)
	if postRec.Code != http.StatusSeeOther {
		t.Fatalf("valid login = %d, want 303 (body: %s)", postRec.Code, postRec.Body.String())
	}
	var session *http.Cookie
	for _, c := range postRec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login did not set a session cookie")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", session.SameSite)
	}
	if session.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", session.Path)
	}

	// Authenticated admin page renders.
	adminRec := httptest.NewRecorder()
	adminReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	adminReq.AddCookie(session)
	h.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("GET /admin with session = %d, want 200", adminRec.Code)
	}
}

func TestBadLoginPersianError(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	form := url.Values{"username": {"admin"}, "password": {"wrong"}, csrfField: {"x"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "x"})
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "نام کاربری یا گذرواژه نادرست است") {
		t.Error("expected generic Persian credential error")
	}
}

func TestLoginRateLimitTrips(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	bad := url.Values{"username": {"admin"}, "password": {"wrong"}, csrfField: {"t"}}

	for i := 0; i < rateLimitMax; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(bad.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "t"})
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(bad.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "t"})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d = %d, want 429", rateLimitMax+1, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "بیش از حد مجاز") {
		t.Error("expected Persian rate-limit message")
	}
}

func TestAdminRequiresSession(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != adminLoginPath {
		t.Fatalf("anonymous GET /admin = %d %q, want 303 → %s",
			rec.Code, rec.Header().Get("Location"), adminLoginPath)
	}

	// htmx requests get HX-Redirect + 403 instead of a redirect.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req2.Header.Set("HX-Request", "true")
	s.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("htmx GET /admin = %d, want 403", rec2.Code)
	}
	if got := rec2.Header().Get("HX-Redirect"); got != adminLoginPath {
		t.Errorf("HX-Redirect = %q, want %q", got, adminLoginPath)
	}
}

func TestSessionTamperRejected(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "eyJhZG1pbiI6dHJ1ZX0.deadbeef"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("forged session cookie must not authenticate")
	}
}
