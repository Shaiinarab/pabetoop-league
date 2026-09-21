// Acceptance tests for the TASK-012 fixture-management handlers.
//
// Harness: a real store (`store.OpenMemory` + `Migrate`) behind
// `NewWithConfig(st, testConfig(t))`, driven through `Server.Handler()` — the
// route table AND the `requireAdmin` wrapping are part of what is proven here.
// `RegisterFixtureImportRoutes` was defined but never called from `Handler()`
// for ~20 h, which made /admin/fixtures and /admin/import answer 404 while every
// test in the suite still passed: the first test below is the regression pin for
// exactly that (it is the only place the mount point is asserted).
//
// Auth is the real login flow (`loginTestAdmin`, shared with TASK-011/015/018);
// every mutation POST is an htmx request carrying the CSRF header, as
// admin_base.html issues them. Assertions quote the real Persian strings from
// the handlers and the store, never paraphrases.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// ---------- helpers ----------

// followFlash performs the GET that a browser would perform after a 303 whose
// response set the flash cookies — the flash is read from the REQUEST cookies,
// so a test that skips this step cannot see any message at all.
func followFlash(h http.Handler, path string, rec *httptest.ResponseRecorder, session *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(session)
	if rec != nil {
		for _, c := range rec.Result().Cookies() {
			if c.Name == flashMsgCookie || c.Name == flashKindCookie {
				req.AddCookie(c)
			}
		}
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}

// matchInWeek returns the stored fixture for one competition + week + ordered
// pairing. The week is part of the identity on purpose: seedTestData already
// contains an نمونه ب ۱ vs نمونه پ ۱ fixture in week 1, so a pairing-only lookup
// would find the seeded row and make "was a new row written?" unanswerable.
func matchInWeek(t *testing.T, st *store.Store, compID, homeID, awayID int64, week int) (store.Match, bool) {
	t.Helper()
	matches, err := st.Matches(compID, nil, nil)
	if err != nil {
		t.Fatalf("matches: %v", err)
	}
	for _, m := range matches {
		if m.Week != nil && *m.Week == week && m.HomeTeamID == homeID && m.AwayTeamID == awayID {
			return m, true
		}
	}
	return store.Match{}, false
}

// countInWeek counts stored fixtures for one competition + week + pairing.
func countInWeek(t *testing.T, st *store.Store, compID, homeID, awayID int64, week int) int {
	t.Helper()
	matches, err := st.Matches(compID, nil, nil)
	if err != nil {
		t.Fatalf("matches: %v", err)
	}
	count := 0
	for _, m := range matches {
		if m.Week != nil && *m.Week == week && m.HomeTeamID == homeID && m.AwayTeamID == awayID {
			count++
		}
	}
	return count
}

// fixtureForm is the new-fixture form as the template posts it.
func fixtureForm(compID int64, week, homeID, awayID int64, date string) url.Values {
	return url.Values{
		"competition": {strconv.FormatInt(compID, 10)},
		"week":        {strconv.FormatInt(week, 10)},
		"home_team":   {strconv.FormatInt(homeID, 10)},
		"away_team":   {strconv.FormatInt(awayID, 10)},
		"date":        {date},
	}
}

// ---------- mount point (regression pin) ----------

func TestFixtureRoutesAreMounted(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/fixtures", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/fixtures = %d, want 200 — RegisterFixtureImportRoutes is not wired into Handler()", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="home_team"`, `name="away_team"`, `name="date"`, "/admin/import"} {
		if !strings.Contains(body, want) {
			t.Errorf("fixtures page missing %q", want)
		}
	}

	rec = adminBrowse(s.Handler(), "/admin/import", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/import = %d, want 200 — RegisterFixtureImportRoutes is not wired into Handler()", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="file"`) {
		t.Error("import page missing the upload field")
	}
}

// The team dropdown only offers teams registered in the selected competition.
func TestFixturePageOffersOnlyRegisteredTeams(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/fixtures", session)
	body := rec.Body.String()
	for _, name := range []string{"نمونه ب ۱", "نمونه پ ۱", "نمونه ث ۱"} {
		if !strings.Contains(body, name) {
			t.Errorf("registered team %q missing from the dropdown", name)
		}
	}
}

// ---------- create ----------

func TestFixtureCreatePersistsScheduledMatch(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	form := fixtureForm(comp.ID, 5, teams[0].ID, teams[2].ID, "۱۴۰۵/۰۸/۰۱")
	form.Set("time", "۱۶:۳۰")
	form.Set("venue", "زمین نمونه")

	rec := adminMutate(s.Handler(), "/admin/fixtures", form, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid create = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}

	m, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[2].ID, 5)
	if !ok {
		t.Fatal("created fixture not persisted")
	}
	if m.ScheduledDate == nil || *m.ScheduledDate != "2026-10-23" {
		t.Errorf("Jalali «۱۴۰۵/۰۸/۰۱» must persist as canonical ISO 2026-10-23, got %v", m.ScheduledDate)
	}
	if m.ScheduledTime == nil || *m.ScheduledTime != "16:30" {
		t.Errorf("Persian clock «۱۶:۳۰» must persist as 16:30, got %v", m.ScheduledTime)
	}
	if m.Status != "scheduled" {
		t.Errorf("a new fixture must be scheduled, got %q", m.Status)
	}

	// The message the operator sees (flash cookie round-trip, Persian text included).
	after := followFlash(s.Handler(), "/admin/fixtures?competition="+strconv.FormatInt(comp.ID, 10), rec, session)
	if !strings.Contains(after.Body.String(), "مسابقه ثبت شد") {
		t.Errorf("success flash missing from the page after redirect: %s", after.Body.String())
	}
}

// A Persian flash that does not survive the cookie round-trip is a product bug:
// net/http drops non-ASCII cookie bytes. This pins the exact handler string.
func TestPersianFlashSurvivesTheCookieRoundTrip(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	// home == away: refused by the handler before it reaches the store.
	rec := adminMutate(s.Handler(), "/admin/fixtures", fixtureForm(comp.ID, 5, teams[0].ID, teams[0].ID, "۱۴۰۵/۰۸/۰۱"), session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self-match = %d, want 303", rec.Code)
	}
	page := followFlash(s.Handler(), "/admin/fixtures", rec, session).Body.String()
	if !strings.Contains(page, "یک تیم نمی‌تواند با خودش بازی کند.") {
		t.Errorf("the Persian refusal must render verbatim after the redirect, got: %s", page)
	}
}

func TestFixtureCreateSelfMatchRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/fixtures", fixtureForm(comp.ID, 6, teams[0].ID, teams[0].ID, "۱۴۰۵/۰۸/۰۱"), session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self-match = %d, want 303", rec.Code)
	}
	if _, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[0].ID, 6); ok {
		t.Error("a team was allowed to play itself")
	}
}

func TestFixtureCreateUnregisteredTeamRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	// A real team of the club, but not registered in this competition (D7: the
	// team's official name is untouched, it simply is not part of this league).
	clubID, err := st.CreateClub("ذخیره")
	if err != nil {
		t.Fatalf("create club: %v", err)
	}
	outsider, err := st.CreateTeam(clubID, "", "ذخیره ۱")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/fixtures", fixtureForm(comp.ID, 6, teams[0].ID, outsider, "۱۴۰۵/۰۸/۰۱"), session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unregistered team = %d, want 303", rec.Code)
	}
	page := followFlash(s.Handler(), "/admin/fixtures", rec, session).Body.String()
	if !strings.Contains(page, "هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند.") {
		t.Errorf("refusal must be Persian and specific, got: %s", page)
	}
	if _, ok := matchInWeek(t, st, comp.ID, teams[0].ID, outsider, 6); ok {
		t.Error("a fixture with an unregistered team was stored")
	}
}

func TestFixtureCreateDuplicateRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	form := fixtureForm(comp.ID, 5, teams[0].ID, teams[1].ID, "۱۴۰۵/۰۸/۰۱")
	if rec := adminMutate(s.Handler(), "/admin/fixtures", form, session, csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("first create = %d, want 303", rec.Code)
	}
	rec := adminMutate(s.Handler(), "/admin/fixtures", form, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("duplicate create = %d, want 303", rec.Code)
	}
	page := followFlash(s.Handler(), "/admin/fixtures", rec, session).Body.String()
	if !strings.Contains(page, "قبلاً در برنامه ثبت شده است") {
		t.Errorf("duplicate refusal must surface the store's Persian message, got: %s", page)
	}

	if count := countInWeek(t, st, comp.ID, teams[0].ID, teams[1].ID, 5); count != 1 {
		t.Errorf("duplicate fixture stored %d times, want 1", count)
	}
}

func TestFixtureCreateBadDateRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/fixtures",
		fixtureForm(comp.ID, 7, teams[0].ID, teams[1].ID, "۱۴۰۵/۱۳/۴۰"), session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("garbage date = %d, want 303", rec.Code)
	}
	page := followFlash(s.Handler(), "/admin/fixtures", rec, session).Body.String()
	if !strings.Contains(page, "تاریخ نامعتبر است") {
		t.Errorf("unparseable date must produce a Persian field error, got: %s", page)
	}
	if _, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[1].ID, 7); ok {
		t.Error("a fixture with an unparseable date was stored")
	}
}

// ---------- delete ----------

func TestFixtureDeleteRemovesScheduledMatch(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	form := fixtureForm(comp.ID, 8, teams[0].ID, teams[1].ID, "۱۴۰۵/۰۸/۰۲")
	if rec := adminMutate(s.Handler(), "/admin/fixtures", form, session, csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d, want 303", rec.Code)
	}
	m, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[1].ID, 8)
	if !ok {
		t.Fatal("fixture to delete was not created")
	}

	rec := adminMutate(s.Handler(), "/admin/fixtures/"+strconv.FormatInt(m.ID, 10)+"/delete",
		url.Values{"competition": {strconv.FormatInt(comp.ID, 10)}}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, still := matchInWeek(t, st, comp.ID, teams[0].ID, teams[1].ID, 8); still {
		t.Error("fixture still present after delete")
	}
	page := followFlash(s.Handler(), "/admin/fixtures", rec, session).Body.String()
	if !strings.Contains(page, "مسابقه حذف شد") {
		t.Errorf("delete flash missing, got: %s", page)
	}
}

// ---------- CSRF / auth ----------

func TestFixtureMutationsRequireCSRF(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, _ := loginTestAdmin(t, s) // no CSRF cookie handed to the POSTs

	cases := []struct {
		name string
		path string
		form url.Values
	}{
		{"create", "/admin/fixtures", fixtureForm(comp.ID, 9, teams[0].ID, teams[1].ID, "۱۴۰۵/۰۸/۰۳")},
		{"delete", "/admin/fixtures/1/delete", url.Values{"competition": {strconv.FormatInt(comp.ID, 10)}}},
	}
	for _, tc := range cases {
		rec := adminMutate(s.Handler(), tc.path, tc.form, session, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token = %d, want 403", tc.name, rec.Code)
		}
	}
	if _, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[1].ID, 9); ok {
		t.Error("a CSRF-less create reached the store")
	}
}

func TestFixtureRoutesAnonymousBlocked(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)

	for _, path := range []string{"/admin/fixtures", "/admin/import"} {
		rec := adminBrowse(s.Handler(), path, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("anonymous GET %s = %d, want 303 to the login page", path, rec.Code)
		}
	}
}
