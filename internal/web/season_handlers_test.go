// Acceptance tests for the TASK-016 seasons surface.
//
// Harness: a real store (`store.OpenMemory` + `Migrate`) behind
// `NewWithConfig(st, testConfig(t))`, driven through `Server.Handler()` so the
// route table AND the `requireAdmin` wrapping are part of what is proven —
// `RegisterSeasonRoutes` is wired there (server.go). Auth is the real login flow
// (`loginTestAdmin`, shared with TASK-011/015/018).
//
// Two tests exist purely to pin ABSENCES the brief calls out as product rules:
// seasons are never deleted (§7 rule 7), so a delete route must not resolve; and
// every mutation needs a CSRF token, so a token-less POST must be refused.
//
// Persian strings are asserted as the store/template actually write them, never
// paraphrased — a re-worded message is a product bug, not a test detail.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// activeBadge is the exact markup one active row carries. Asserting on the full
// span matters: the inactive label is «غیرفعال», which CONTAINS «فعال», so a
// substring count of the bare word would always find at least one.
const activeBadge = `<span class="pill">فعال</span>`

// seasonByName returns a season by its exact official name.
func seasonByName(t *testing.T, st *store.Store, name string) (store.Season, bool) {
	t.Helper()
	seasons, err := st.Seasons()
	if err != nil {
		t.Fatalf("seasons: %v", err)
	}
	for _, s := range seasons {
		if s.Name == name {
			return s, true
		}
	}
	return store.Season{}, false
}

// createSeason makes a season directly in the store (setup, not the code under
// test) and returns its id.
func createSeason(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	id, err := st.CreateSeason(name, nil, nil)
	if err != nil {
		t.Fatalf("create season %q: %v", name, err)
	}
	return id
}

// flashFrom reads the flash cookies a response wrote (the redirect path's only
// channel for a Persian message).
func flashFrom(t *testing.T, rec *httptest.ResponseRecorder) (kind, message string) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case flashMsgCookie:
			message = decodeCookieValue(c.Value)
		case flashKindCookie:
			kind = c.Value
		}
	}
	return kind, message
}

// countActives returns how many seasons the store currently marks active.
func countActives(t *testing.T, st *store.Store) int {
	t.Helper()
	seasons, err := st.Seasons()
	if err != nil {
		t.Fatalf("seasons: %v", err)
	}
	n := 0
	for _, s := range seasons {
		if s.IsActive {
			n++
		}
	}
	return n
}

// ---------- list ----------

func TestSeasonListRendersRowsWithExactlyOneActiveBadge(t *testing.T) {
	s, st := newAdminTestServer(t)
	active := createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	createSeason(t, st, "۱۴۰۶–۱۴۰۷")
	if err := st.ActivateSeason(active); err != nil {
		t.Fatalf("activate: %v", err)
	}
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/seasons", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/seasons = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"۱۴۰۵–۱۴۰۶", // authority-controlled official name
		"۱۴۰۶–۱۴۰۷",
		"غیرفعال",
		"/admin/seasons/", // the per-row edit + activate forms
		"فعال‌سازی",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("seasons list missing %q", want)
		}
	}
	if got := strings.Count(body, activeBadge); got != 1 {
		t.Errorf("active badge rendered %d times, want exactly 1 (one active season)", got)
	}
	// The nav tab must exist, or the screen is unreachable in the UI.
	if !strings.Contains(body, `href="/admin/seasons"`) {
		t.Error("seasons nav tab missing from the admin chrome")
	}
}

func TestSeasonListEmptyStateInPersian(t *testing.T) {
	s, _ := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/seasons", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/seasons = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "هنوز فصلی ثبت نشده است") {
		t.Errorf("empty list must explain itself in Persian, got: %s", rec.Body.String())
	}
}

// ---------- create ----------

func TestSeasonCreateWithEmptyNameIsRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	before, _ := st.Seasons()

	rec := adminMutate(s.Handler(), "/admin/seasons", url.Values{
		"name": {"   "}, // trims to empty — the field under test
	}, session, csrf)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank name = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "نام فصل الزامی است.") {
		t.Errorf("422 body must carry the Persian field error, got: %s", rec.Body.String())
	}
	after, _ := st.Seasons()
	if len(after) != len(before) {
		t.Errorf("refused create must not write a row (before=%d, after=%d)", len(before), len(after))
	}
}

func TestSeasonCreateParsesJalaliStartDate(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/seasons", url.Values{
		"name":      {"۱۴۰۵–۱۴۰۶"},
		"starts_on": {"۱۴۰۵/۰۷/۰۱"},
	}, session, csrf)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	se, ok := seasonByName(t, st, "۱۴۰۵–۱۴۰۶")
	if !ok {
		t.Fatal("season was not persisted")
	}
	parsed, err := jalali.Parse("۱۴۰۵/۰۷/۰۱")
	if err != nil {
		t.Fatalf("reference parse: %v", err)
	}
	want := jalali.FormatISO(parsed)
	if se.StartsOn == nil || *se.StartsOn != want {
		t.Errorf("starts_on = %v, want %q (canonical Gregorian ISO)", se.StartsOn, want)
	}
	if se.EndsOn != nil {
		t.Errorf("ends_on = %v, want nil (unset input must stay unset)", se.EndsOn)
	}
	assertAuditRow(t, st, "insert", "season", se.ID)
}

func TestSeasonCreateRejectsUnparseableJalaliDate(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	before, _ := st.Seasons()

	rec := adminMutate(s.Handler(), "/admin/seasons", url.Values{
		"name":      {"۱۴۰۶–۱۴۰۷"},
		"starts_on": {"۱۴۰۵/۱۳/۴۰"}, // month 13 does not exist
	}, session, csrf)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad date = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ماه شمسی نامعتبر") {
		t.Errorf("422 body must carry the jalali package's Persian message, got: %s", rec.Body.String())
	}
	after, _ := st.Seasons()
	if len(after) != len(before) {
		t.Errorf("refused create must not write a row (before=%d, after=%d)", len(before), len(after))
	}
}

func TestSeasonCreateRejectsEndBeforeStart(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	before, _ := st.Seasons()

	rec := adminMutate(s.Handler(), "/admin/seasons", url.Values{
		"name":      {"۱۴۰۶–۱۴۰۷"},
		"starts_on": {"۱۴۰۶/۰۷/۰۱"},
		"ends_on":   {"۱۴۰۶/۰۱/۰۱"}, // before the start
	}, session, csrf)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reversed range = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "تاریخ پایان نمی‌تواند پیش از تاریخ شروع باشد.") {
		t.Errorf("422 body must explain the range in Persian, got: %s", rec.Body.String())
	}
	if after, _ := st.Seasons(); len(after) != len(before) {
		t.Error("refused create must not write a row")
	}
}

func TestSeasonCreateRejectsDuplicateName(t *testing.T) {
	s, st := newAdminTestServer(t)
	createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/seasons", url.Values{
		"name": {"۱۴۰۵–۱۴۰۶"},
	}, session, csrf)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate name = %d, want 422", rec.Code)
	}
	// The store's own message is surfaced verbatim, never re-worded. It arrives
	// wrapped (the sentinel alone says «باشگاه…», which would be wrong here), so
	// the assertion is the season-specific sentence the store formats.
	const want = "فصلی با نام «۱۴۰۵–۱۴۰۶» قبلاً ثبت شده است"
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("422 body must carry the store's message %q, got: %s", want, rec.Body.String())
	}
}

// ---------- edit ----------

func TestSeasonRenameKeepsOfficialNameApartFromTrim(t *testing.T) {
	s, st := newAdminTestServer(t)
	id := createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	session, csrf := loginTestAdmin(t, s)

	const official = "۱۴۰۵ – ۱۴۰۶ (دورهٔ بیست‌وچهارم)" // inner spacing must survive
	rec := adminMutate(s.Handler(), "/admin/seasons/"+strconv.FormatInt(id, 10)+"/edit", url.Values{
		"name": {"  " + official + "  "},
	}, session, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200 (row swap) — body: %s", rec.Code, rec.Body.String())
	}
	se, ok := seasonByName(t, st, official)
	if !ok {
		t.Fatalf("name was not stored byte-for-byte apart from trim; seasons: %+v", seasonsForDebug(t, st))
	}
	if se.ID != id {
		t.Errorf("rename wrote to id %d, want %d", se.ID, id)
	}
	assertAuditRow(t, st, "update", "season", id)
}

func TestSeasonEditRefusesBlankName(t *testing.T) {
	s, st := newAdminTestServer(t)
	id := createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/seasons/"+strconv.FormatInt(id, 10)+"/edit", url.Values{
		"name": {""},
	}, session, csrf)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank rename = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "نام فصل الزامی است.") {
		t.Errorf("refusal must carry the Persian message, got: %s", rec.Body.String())
	}
	if _, ok := seasonByName(t, st, "۱۴۰۵–۱۴۰۶"); !ok {
		t.Error("refused rename must leave the official name untouched")
	}
}

// seasonsForDebug prints the store's seasons in a failure message.
func seasonsForDebug(t *testing.T, st *store.Store) []store.Season {
	t.Helper()
	seasons, err := st.Seasons()
	if err != nil {
		return []store.Season{{Name: "seasons() failed: " + err.Error()}}
	}
	return seasons
}

// ---------- activate ----------

func TestSeasonActivateLeavesExactlyOneActiveAndNamesIt(t *testing.T) {
	s, st := newAdminTestServer(t)
	first := createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	second := createSeason(t, st, "۱۴۰۶–۱۴۰۷")
	if err := st.ActivateSeason(first); err != nil {
		t.Fatalf("activate first: %v", err)
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/seasons/"+strconv.FormatInt(second, 10)+"/activate", url.Values{}, session, csrf)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/seasons" {
		t.Errorf("Location = %q, want /admin/seasons", loc)
	}
	if got := countActives(t, st); got != 1 {
		t.Fatalf("%d active seasons after activation, want exactly 1", got)
	}
	active, err := st.ActiveSeason()
	if err != nil || active == nil {
		t.Fatalf("active season: %v", err)
	}
	if active.ID != second {
		t.Errorf("active season = #%d, want #%d (the one just activated)", active.ID, second)
	}
	kind, msg := flashFrom(t, rec)
	if kind != flashSuccess {
		t.Errorf("flash kind = %q, want %q", kind, flashSuccess)
	}
	if !strings.Contains(msg, "۱۴۰۶–۱۴۰۷") {
		t.Errorf("flash must name the activated season, got %q", msg)
	}
	assertAuditRow(t, st, "update", "season", second)

	// The page must agree with the store: one badge, and the header pill names it.
	page := adminBrowse(s.Handler(), "/admin/seasons", session)
	if page.Code != http.StatusOK {
		t.Fatalf("GET after activate = %d, want 200", page.Code)
	}
	if got := strings.Count(page.Body.String(), activeBadge); got != 1 {
		t.Errorf("page shows %d active badges, want 1", got)
	}
}

func TestSeasonActivateWarnsWhenSeasonHasNoCompetitions(t *testing.T) {
	s, st := newAdminTestServer(t)
	id := createSeason(t, st, "۱۴۰۶–۱۴۰۷")
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/seasons/"+strconv.FormatInt(id, 10)+"/activate", url.Values{}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate empty season = %d, want 303", rec.Code)
	}
	// A warning, not an error: the operator may legitimately activate first and
	// create the competitions afterwards.
	kind, msg := flashFrom(t, rec)
	if kind != flashSuccess {
		t.Fatalf("empty-season activation must not be an error flash, got kind %q", kind)
	}
	if !strings.Contains(msg, "هیچ مسابقه‌ای ندارد") {
		t.Errorf("flash must warn that the season has no competitions, got %q", msg)
	}
	if got := countActives(t, st); got != 1 {
		t.Errorf("%d active seasons, want 1 (the warning must not block activation)", got)
	}
}

func TestSeasonActivateUnknownIDIsRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/seasons/999999/activate", url.Values{}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate unknown id = %d, want 303", rec.Code)
	}
	kind, msg := flashFrom(t, rec)
	if kind != flashError {
		t.Errorf("unknown id flash kind = %q, want %q", kind, flashError)
	}
	if !strings.Contains(msg, "یافت نشد") {
		t.Errorf("unknown id flash = %q, want a Persian not-found message", msg)
	}
	if got := countActives(t, st); got != 0 {
		t.Errorf("%d active seasons after a refused activation, want 0", got)
	}
}

// ---------- absences the brief pins ----------

// Seasons are never deleted (§7 rule 7), so the route must not resolve — not even
// to a handler that refuses. A 404/405 here is the product rule holding.
func TestSeasonDeleteRouteDoesNotExist(t *testing.T) {
	s, st := newAdminTestServer(t)
	id := createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	session, csrf := loginTestAdmin(t, s)

	for _, path := range []string{
		"/admin/seasons/" + strconv.FormatInt(id, 10) + "/delete",
		"/admin/seasons/" + strconv.FormatInt(id, 10),
	} {
		rec := adminMutate(s.Handler(), path, url.Values{}, session, csrf)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 404/405 (deletion is not exposed)", path, rec.Code)
		}
	}
	if _, ok := seasonByName(t, st, "۱۴۰۵–۱۴۰۶"); !ok {
		t.Error("the season vanished — a hidden delete path exists")
	}
}

func TestSeasonMutationsRequireCSRF(t *testing.T) {
	s, st := newAdminTestServer(t)
	id := createSeason(t, st, "۱۴۰۵–۱۴۰۶")
	session, _ := loginTestAdmin(t, s)

	cases := []struct {
		path string
		form url.Values
	}{
		{"/admin/seasons", url.Values{"name": {"۱۴۰۶–۱۴۰۷"}}},
		{"/admin/seasons/" + strconv.FormatInt(id, 10) + "/edit", url.Values{"name": {"x"}}},
		{"/admin/seasons/" + strconv.FormatInt(id, 10) + "/activate", url.Values{}},
	}
	for _, tc := range cases {
		rec := adminMutate(s.Handler(), tc.path, tc.form, session, nil) // no CSRF token
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF = %d, want 403", tc.path, rec.Code)
		}
	}
	if after, _ := st.Seasons(); len(after) != 1 {
		t.Errorf("CSRF-less POSTs wrote rows (%d seasons, want 1)", len(after))
	}
}
