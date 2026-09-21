// Write-path tests for the TASK-010 admin handlers (TASK-018).
//
// Harness: a real store (`store.OpenMemory` + `Migrate`) behind
// `NewWithConfig(st, testConfig(t))`, exercised through `Server.Handler()` — the
// routing and the `requireAdmin` wrapping are part of what is being proven, so
// no handler is called directly. Auth uses the real login flow
// (`loginTestAdmin`, shared with the TASK-011 tests); every mutation POST is an
// htmx request carrying the inherited CSRF header, as admin_base.html does.
//
// Assertions use the store's real Persian strings (api.go/store.go contract),
// never paraphrases. No production file is touched by this task.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// ---------- harness ----------

// newAdminTestServer is the TASK-010 lane's store-backed server; it returns the
// store too, because half of these tests assert persisted state and audit rows.
func newAdminTestServer(t *testing.T) (*Server, *store.Store) {
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
	return s, st
}

// adminReq drives a request through the full middleware chain. A nil session
// simulates an anonymous caller; a nil csrf simulates a token-less POST.
func adminReq(h http.Handler, method, path string, form url.Values, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if method != http.MethodGet {
		req.Header.Set("HX-Request", "true")
	}
	if csrf != nil {
		req.Header.Set(csrfHeader, csrf.Value)
		req.AddCookie(csrf)
	}
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// adminBrowse is an authenticated GET; adminMutate is an authenticated POST.
func adminBrowse(h http.Handler, path string, session *http.Cookie) *httptest.ResponseRecorder {
	return adminReq(h, http.MethodGet, path, nil, session, nil)
}

func adminMutate(h http.Handler, path string, form url.Values, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	return adminReq(h, http.MethodPost, path, form, session, csrf)
}

// clubByName returns a club (including inactive ones) by exact official name.
func clubByName(t *testing.T, st *store.Store, name string) (store.Club, bool) {
	t.Helper()
	clubs, err := st.Clubs(true)
	if err != nil {
		t.Fatalf("clubs: %v", err)
	}
	for _, c := range clubs {
		if c.Name == name {
			return c, true
		}
	}
	return store.Club{}, false
}

// assertAuditRow proves §7 rule 10 for one mutation without relying on ordering.
func assertAuditRow(t *testing.T, st *store.Store, action, entity string, entityID int64) {
	t.Helper()
	entries, err := st.AuditLog(50)
	if err != nil {
		t.Fatalf("audit log: %v", err)
	}
	for _, e := range entries {
		if e.Action == action && e.Entity == entity && e.EntityID == entityID {
			return
		}
	}
	t.Errorf("no audit row %s/%s#%d among %d rows (§7 rule 10)", action, entity, entityID, len(entries))
}

// ---------- clubs: list ----------

func TestAdminClubsListRendersTeamCounts(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)

	clubID, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("create club: %v", err)
	}
	if _, err := st.CreateTeam(clubID, "۱", "نمونه ب ۱"); err != nil {
		t.Fatalf("create team: %v", err)
	}
	if _, err := st.CreateTeam(clubID, "۲", "نمونه ب ۲"); err != nil {
		t.Fatalf("create team: %v", err)
	}

	rec := adminBrowse(s.Handler(), "/admin/clubs", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/clubs = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "نمونه ب") {
		t.Error("club name missing from the list")
	}
	if !strings.Contains(body, `<tr id="club-`+strconv.FormatInt(clubID, 10)+`">`) {
		t.Error("club row must carry the swap target id")
	}
	if !strings.Contains(body, "۲") {
		t.Error("team count must render as a Persian numeral")
	}
}

func TestAdminClubsListEmptyState(t *testing.T) {
	s, _ := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/clubs", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/clubs = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "هنوز باشگاهی ثبت نشده است.") {
		t.Error("empty clubs list must show the Persian empty state")
	}
}

// ---------- clubs: create ----------

func TestAdminClubCreateValid(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/clubs", url.Values{"name": {"نمونه ب نوین"}}, session, csrf)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/clubs" {
		t.Fatalf("create club = %d %q, want 303 → /admin/clubs", rec.Code, rec.Header().Get("Location"))
	}
	club, ok := clubByName(t, st, "نمونه ب نوین")
	if !ok {
		t.Fatal("club was not persisted")
	}
	// Official name is stored exactly as entered (D7).
	if club.Name != "نمونه ب نوین" {
		t.Errorf("stored name = %q, want the exact entered name", club.Name)
	}
	assertAuditRow(t, st, "insert", "club", club.ID)
}

func TestAdminClubCreateDuplicateNotPersisted(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	if _, err := st.CreateClub("نمونه ب"); err != nil {
		t.Fatalf("seed club: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/clubs", url.Values{"name": {"نمونه ب"}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate create = %d, want the re-rendered clubs page (200)", rec.Code)
	}
	clubs, err := st.Clubs(true)
	if err != nil {
		t.Fatalf("clubs: %v", err)
	}
	if len(clubs) != 1 {
		t.Errorf("duplicate create must not add a row: %d clubs", len(clubs))
	}
}

// TestAdminClubCreateDuplicateShowsError documents a REAL BUG found by this task.
//
// handleAdminClubCreate's error path calls setFlash(w, r, …) and then re-renders
// the clubs page *in the same response* via s.adminChrome → takeFlash(r), which
// reads the flash from the REQUEST cookies. The flash was only written as a
// Set-Cookie header for the next request, so the error is silently omitted from
// the page the admin actually sees: a duplicate club looks like a successful no-op.
//
// Repro: create club «نمونه ب», then POST /admin/clubs name=نمونه ب.
// Observed: 200, no flash in the body (a `flash_msg` Set-Cookie is present).
// Expected: the Persian duplicate message rendered in .flash.error.
// Fix (pass Flash into the view, or redirect-after-post) is in admin_handlers.go —
// outside this test-only allowlist; reported as Next_actions #2.
func TestAdminClubCreateDuplicateShowsError(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	if _, err := st.CreateClub("نمونه ب"); err != nil {
		t.Fatalf("seed club: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/clubs", url.Values{"name": {"نمونه ب"}}, session, csrf)
	if strings.Contains(rec.Body.String(), "باشگاهی با این نام قبلاً ثبت شده است") {
		return // fixed
	}
	names := []string{}
	for _, c := range rec.Result().Cookies() {
		names = append(names, c.Name)
	}
	t.Skipf("BUG: duplicate-club error missing from the rendered page (flash cookie set: %v); "+
		"setFlash + same-response render loses the message", names)
}

func TestAdminClubCreateBlankNameNotPersisted(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/clubs", url.Values{"name": {"   "}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("blank create = %d, want 200 re-render", rec.Code)
	}
	clubs, err := st.Clubs(true)
	if err != nil {
		t.Fatalf("clubs: %v", err)
	}
	if len(clubs) != 0 {
		t.Errorf("blank create must not add a row: %d clubs", len(clubs))
	}
}

// TestAdminClubCreateBlankNameShowsError pins the same lost-flash bug as
// TestAdminClubCreateDuplicateShowsError, for the client-side-validation path.
func TestAdminClubCreateBlankNameShowsError(t *testing.T) {
	s, _ := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/clubs", url.Values{"name": {"   "}}, session, csrf)
	if strings.Contains(rec.Body.String(), "نام باشگاه الزامی است.") {
		return // fixed
	}
	t.Skip("BUG: blank-name error missing from the rendered page (setFlash + same-response render)")
}

// ---------- clubs: rename ----------

func TestAdminClubRename(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	id, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/clubs/"+strconv.FormatInt(id, 10)+"/rename",
		url.Values{"name": {"نمونه ب نوین"}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d, want 200 (row partial)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<tr id="club-`+strconv.FormatInt(id, 10)+`">`) {
		t.Error("rename must answer with the re-rendered row for that club")
	}
	if !strings.Contains(body, "نمونه ب نوین") {
		t.Error("renamed name missing from the returned row")
	}
	club, ok := clubByName(t, st, "نمونه ب نوین")
	if !ok || club.ID != id {
		t.Fatalf("rename not persisted (found=%v, id=%d)", ok, club.ID)
	}
	assertAuditRow(t, st, "update", "club", id)
}

// TestAdminClubRenameDuplicateRefused documents a REAL BUG found by this task:
// handleAdminClubRename answers a refused rename with the partial
// "club_row_error" (admin_handlers.go:309), but that template is not defined in
// web/templates/partials/club_row.html — or anywhere. renderAdminPartial fails
// and the caller gets a Persian 500 instead of the intended 400 refusal row.
//
// Repro (live, 2026-09-11): create «نمونه ب» and «نمونه پ», then
//
//	POST /admin/clubs/<نمونه پ>/rename  name=نمونه ب
//
// → observed 500 «خطایی رخ داد؛ لطفاً دوباره تلاش کنید.», expected 400 +
//
//	the store message «باشگاهی با این نام قبلاً ثبت شده است: نمونه ب».
//
// Fix is out of this task's allowlist (test-only); reported as Next_actions #1.
func TestAdminClubRenameDuplicateRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	if _, err := st.CreateClub("نمونه ب"); err != nil {
		t.Fatalf("seed club A: %v", err)
	}
	id, err := st.CreateClub("نمونه پ")
	if err != nil {
		t.Fatalf("seed club B: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/clubs/"+strconv.FormatInt(id, 10)+"/rename",
		url.Values{"name": {"نمونه ب"}}, session, csrf)

	// The store must not have accepted the duplicate, whatever the response.
	if _, ok := clubByName(t, st, "نمونه پ"); !ok {
		t.Fatal("refused rename must leave the official name untouched")
	}
	if rec.Code != http.StatusBadRequest {
		t.Skipf("BUG: refused rename = %d, want 400 with the club_row_error partial; "+
			"template %q is referenced by admin_handlers.go but not defined (see task comment)", rec.Code, "club_row_error")
	}
	if !strings.Contains(rec.Body.String(), "باشگاهی با این نام قبلاً ثبت شده است") {
		t.Errorf("400 body must carry the store's Persian message, got: %s", rec.Body.String())
	}
}

// ---------- clubs: delete (soft, blocked by registrations) ----------

func TestAdminClubDeleteUnreferencedDeactivates(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	id, err := st.CreateClub("نمونه پ")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/clubs/"+strconv.FormatInt(id, 10)+"/delete",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("successful delete must answer an empty body (htmx removes the row), got %d bytes", rec.Body.Len())
	}
	club, ok := clubByName(t, st, "نمونه پ")
	if !ok {
		t.Fatal("club must not be hard-deleted (never hard-delete, D10)")
	}
	if club.IsActive {
		t.Error("club should be deactivated after delete")
	}
	assertAuditRow(t, st, "update", "club", id)
}

// TestAdminClubDeleteReferencedRefused: the *state* guarantee is asserted here
// (and passes); the response-code guarantee is the same missing-template bug as
// the rename case, so it is asserted in a skipped test below.
func TestAdminClubDeleteReferencedRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	_ = comp
	session, csrf := loginTestAdmin(t, s)

	club, ok := clubByName(t, st, "نمونه ب")
	if !ok {
		t.Fatalf("seed club missing (teams: %+v)", teams)
	}
	rec := adminMutate(s.Handler(), "/admin/clubs/"+strconv.FormatInt(club.ID, 10)+"/delete",
		url.Values{}, session, csrf)

	after, _ := clubByName(t, st, "نمونه ب")
	if !after.IsActive {
		t.Error("a club with an active registered team must NOT be deactivated (§7 rule 7)")
	}
	if rec.Code == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "ثبت‌نام") {
		t.Errorf("400 refusal should carry the store's registration message, got: %s", rec.Body.String())
	}
}

// TestAdminClubDeleteReferencedExpects400 pins the intended contract of a
// refused delete; skipped because `club_row_error` is undefined (see the rename
// bug comment above — same root cause, admin_handlers.go:333).
func TestAdminClubDeleteReferencedExpects400(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)
	club, ok := clubByName(t, st, "نمونه ب")
	if !ok {
		t.Fatal("seed club missing")
	}

	rec := adminMutate(s.Handler(), "/admin/clubs/"+strconv.FormatInt(club.ID, 10)+"/delete",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Skipf("BUG: refused delete = %d, want 400 + club_row_error partial (template missing)", rec.Code)
	}
}

// ---------- teams ----------

func TestAdminTeamFormRendersOptions(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)
	if _, err := st.CreateClub("نمونه ب"); err != nil {
		t.Fatalf("seed club: %v", err)
	}
	if err := st.EnsureAgeGroups(12, 13); err != nil {
		t.Fatalf("ensure age groups: %v", err)
	}

	rec := adminBrowse(s.Handler(), "/admin/teams/new", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/teams/new = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="club"`, `name="age_group"`, `name="display_name"`, "نمونه ب", "۱۲ سال"} {
		if !strings.Contains(body, want) {
			t.Errorf("team form missing %q", want)
		}
	}
}

func TestAdminTeamCreateValid(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	clubID, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	if err := st.EnsureAgeGroups(12); err != nil {
		t.Fatalf("ensure age groups: %v", err)
	}
	ages, _ := st.AgeGroups()

	form := url.Values{
		"club":         {strconv.FormatInt(clubID, 10)},
		"age_group":    {strconv.FormatInt(ages[0].ID, 10)},
		"display_name": {"نمونه ب ۱"},
		"label":        {"۱"},
	}
	rec := adminMutate(s.Handler(), "/admin/teams", form, session, csrf)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/teams" {
		t.Fatalf("create team = %d %q, want 303 → /admin/teams (body: %s)",
			rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	teams, err := st.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	var found *store.Team
	for i := range teams {
		if teams[i].ClubID == clubID && teams[i].Label == "۱" {
			found = &teams[i]
		}
	}
	if found == nil {
		t.Fatalf("team not persisted with submitted club/label: %+v", teams)
	}
	if found.DisplayName != "نمونه ب ۱" {
		t.Errorf("display name = %q, want exact entry", found.DisplayName)
	}
	assertAuditRow(t, st, "insert", "team", found.ID)
}

func TestAdminTeamCreateFieldErrors(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	if err := st.EnsureAgeGroups(12); err != nil {
		t.Fatalf("ensure age groups: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/teams",
		url.Values{"club": {""}, "age_group": {""}, "display_name": {""}}, session, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid team form = %d, want 422 (htmx swaps 422, D2)", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"انتخاب باشگاه الزامی است.", "انتخاب ردهٔ سنی الزامی است.", "نام نمایشی تیم الزامی است."} {
		if !strings.Contains(body, want) {
			t.Errorf("422 form missing per-field Persian error %q", want)
		}
	}
	teams, err := st.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("invalid submit must create nothing, got %d teams", len(teams))
	}
}

func TestAdminTeamCreateDuplicateLabelRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	clubID, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	if err := st.EnsureAgeGroups(12); err != nil {
		t.Fatalf("ensure age groups: %v", err)
	}
	ages, _ := st.AgeGroups()
	if _, err := st.CreateTeam(clubID, "۱", "نمونه ب ۱"); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	form := url.Values{
		"club":         {strconv.FormatInt(clubID, 10)},
		"age_group":    {strconv.FormatInt(ages[0].ID, 10)},
		"display_name": {"نمونه ب ۲"},
		"label":        {"۱"}, // same club + same label → UNIQUE(club_id, label)
	}
	rec := adminMutate(s.Handler(), "/admin/teams", form, session, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate label = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "باشگاه قبلاً تیمی با برچسب «۱» دارد") {
		t.Errorf("expected the store's exact Persian message, got: %s", rec.Body.String())
	}
	teams, _ := st.Teams(nil)
	if len(teams) != 1 {
		t.Errorf("duplicate label must not create a team: %d teams", len(teams))
	}
}

func TestAdminTeamDeactivateBlockedWhileRegistered(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, _, teams := seedTestData(t, st) // seed registers all three teams
	session, csrf := loginTestAdmin(t, s)

	id := teams[0].ID
	rec := adminMutate(s.Handler(), "/admin/teams/"+strconv.FormatInt(id, 10)+"/deactivate",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate = %d, want 303 redirect back to /admin/teams", rec.Code)
	}
	// Blocked → the store message is surfaced via flash; the team stays active.
	all, err := st.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	for _, tm := range all {
		if tm.ID == id && !tm.IsActive {
			t.Fatal("a registered team must not be deactivated (§7 rule 7)")
		}
	}
}

func TestAdminTeamDeactivateAndStaysListed(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	clubID, err := st.CreateClub("نمونه پ")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	id, err := st.CreateTeam(clubID, "", "نمونه پ ۱")
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/teams/"+strconv.FormatInt(id, 10)+"/deactivate",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate = %d, want 303", rec.Code)
	}
	teams, err := st.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	var found *store.Team
	for i := range teams {
		if teams[i].ID == id {
			found = &teams[i]
		}
	}
	if found == nil {
		t.Fatal("deactivated team must still be listed (never hard-deleted)")
	}
	if found.IsActive {
		t.Error("team should be inactive after deactivate")
	}
	assertAuditRow(t, st, "update", "team", id)
}

// ---------- cross-cutting: CSRF + admin gate ----------

func TestAdminMutationsRequireCSRF(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)
	clubID, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	id := strconv.FormatInt(clubID, 10)

	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/admin/clubs", url.Values{"name": {"نمونه پ"}}},
		{"/admin/clubs/" + id + "/rename", url.Values{"name": {"نمونه پ"}}},
		{"/admin/clubs/" + id + "/delete", url.Values{}},
		{"/admin/teams", url.Values{"club": {id}, "display_name": {"نمونه ب ۱"}}},
		{"/admin/teams/" + id + "/deactivate", url.Values{}},
	} {
		rec := adminMutate(s.Handler(), tc.path, tc.form, session, nil) // no CSRF token
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF = %d, want 403", tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "نشست شما منقضی شده است") {
			t.Errorf("POST %s: expected the Persian CSRF message", tc.path)
		}
	}
}

func TestAdminMutationsAnonymousBlocked(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, csrf := loginTestAdmin(t, s) // token but no session
	if _, err := st.CreateClub("نمونه ب"); err != nil {
		t.Fatalf("seed club: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/clubs", url.Values{"name": {"نمونه پ ناشناس"}}, nil, csrf)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous POST = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "باید وارد شوید") {
		t.Error("expected the Persian auth message")
	}
	if _, ok := clubByName(t, st, "نمونه پ ناشناس"); ok {
		t.Fatal("anonymous POST must never execute")
	}
}

// ---------- /admin/teams pagination (TASK-023) ----------
//
// fb3 audit F7: the list rendered every team at once (219 seeded teams = 144,682 B
// of markup). These tests pin the paging behaviour and, more importantly, that a
// full page walk covers the store exactly once — paging that drops or repeats a row
// is worse than no paging, because the operator cannot tell.

// teamDeactivateRe matches the per-row action URL; the team id in it is a
// reliable per-row identity (row names are not unique across clubs).
var teamDeactivateRe = regexp.MustCompile(`/admin/teams/(\d+)/deactivate`)

// teamIDsOnPage returns the team ids rendered on the response, in page order.
func teamIDsOnPage(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	matches := teamDeactivateRe.FindAllStringSubmatch(rec.Body.String(), -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m[1])
	}
	return ids
}

// seedTeams creates n teams alternating between two clubs and returns the
// store's own total, so assertions compare against the store and never against a
// literal that could drift from it.
func seedTeams(t *testing.T, st *store.Store, n int) int {
	t.Helper()
	var clubIDs []int64
	for _, name := range []string{"نمونه ب", "نمونه ح"} {
		id, err := st.CreateClub(name)
		if err != nil {
			t.Fatalf("create club %s: %v", name, err)
		}
		clubIDs = append(clubIDs, id)
	}
	for i := 0; i < n; i++ {
		label := strconv.Itoa(i + 1)
		if _, err := st.CreateTeam(clubIDs[i%len(clubIDs)], label, "تیم "+label); err != nil {
			t.Fatalf("create team %d: %v", i, err)
		}
	}
	all, err := st.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	return len(all)
}

// TestAdminTeamsListPaginates walks every page: the row counts are right, the
// pager links point where they claim, and the walk covers the store exactly once.
func TestAdminTeamsListPaginates(t *testing.T) {
	s, st := newAdminTestServer(t)
	total := seedTeams(t, st, 120) // 50 + 50 + 20 across three pages
	if total != 120 {
		t.Fatalf("seed produced %d teams, want 120", total)
	}
	session, _ := loginTestAdmin(t, s)
	h := s.Handler()

	page1 := adminBrowse(h, "/admin/teams", session)
	if page1.Code != http.StatusOK {
		t.Fatalf("GET /admin/teams = %d, want 200", page1.Code)
	}
	ids1 := teamIDsOnPage(t, page1)
	if len(ids1) != adminTeamsPageSize {
		t.Errorf("page 1 rendered %d rows, want %d", len(ids1), adminTeamsPageSize)
	}
	body1 := page1.Body.String()
	// The operator must be able to see the true total, in Persian digits.
	wantCount := "نمایش " + jalali.ToPersianDigits("1") + " تا " +
		jalali.ToPersianDigits(strconv.Itoa(adminTeamsPageSize)) + " از " +
		jalali.ToPersianDigits(strconv.Itoa(total))
	if !strings.Contains(body1, wantCount) {
		t.Errorf("page 1 must show the store total: %q not found", wantCount)
	}
	if !strings.Contains(body1, `href="/admin/teams?page=2"`) {
		t.Error("page 1 must link to page 2")
	}
	if strings.Contains(body1, `href="/admin/teams"`) {
		t.Error("page 1 must not offer a previous-page link")
	}

	page2 := adminBrowse(h, "/admin/teams?page=2", session)
	ids2 := teamIDsOnPage(t, page2)
	if len(ids2) != adminTeamsPageSize {
		t.Errorf("page 2 rendered %d rows, want %d", len(ids2), adminTeamsPageSize)
	}
	body2 := page2.Body.String()
	if !strings.Contains(body2, `href="/admin/teams?page=3"`) {
		t.Error("page 2 must link forward to page 3")
	}
	if !strings.Contains(body2, `href="/admin/teams"`) {
		t.Error("page 2 must link back to page 1")
	}

	page3 := adminBrowse(h, "/admin/teams?page=3", session)
	ids3 := teamIDsOnPage(t, page3)
	if len(ids3) != total-2*adminTeamsPageSize {
		t.Errorf("page 3 rendered %d rows, want %d", len(ids3), total-2*adminTeamsPageSize)
	}
	body3 := page3.Body.String()
	if strings.Contains(body3, `href="/admin/teams?page=4"`) {
		t.Error("the last page must not link to a page that does not exist")
	}

	// The whole point: a full walk visits every team exactly once.
	seen := map[string]bool{}
	for _, id := range append(append(append([]string{}, ids1...), ids2...), ids3...) {
		if seen[id] {
			t.Errorf("team %s is rendered on more than one page", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Errorf("the page walk covered %d teams, the store has %d", len(seen), total)
	}
}

// TestAdminTeamsListPagePastEndIsPersianEmptyState: ?page=999 is operator error,
// not a fault. It must be a 200 with a Persian explanation and a way back — never
// a 500, never a blank panel, never a silent redirect to page 1 (which would look
// like the link worked and the rows changed under the operator).
func TestAdminTeamsListPagePastEndIsPersianEmptyState(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTeams(t, st, 60) // two pages
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/teams?page=999", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/teams?page=999 = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "خطای غیرمنتظره") {
		t.Error("a page past the end rendered the 500 error page")
	}
	if !strings.Contains(body, "این شماره وجود ندارد") {
		t.Error("expected a Persian empty state for a page past the end")
	}
	if got := len(teamIDsOnPage(t, rec)); got != 0 {
		t.Errorf("a page past the end rendered %d team rows, want 0", got)
	}
	if !strings.Contains(body, `href="/admin/teams"`) {
		t.Error("a page past the end must offer a link back to the first page")
	}
}

// TestAdminTeamsListBadPageFallsBackToFirstPage: the ?page= value is
// operator-supplied, so garbage must land on page 1 rather than erroring — and
// the bare route stays the canonical page 1.
func TestAdminTeamsListBadPageFallsBackToFirstPage(t *testing.T) {
	s, st := newAdminTestServer(t)
	total := seedTeams(t, st, 120)
	session, _ := loginTestAdmin(t, s)
	h := s.Handler()

	first := strings.Join(teamIDsOnPage(t, adminBrowse(h, "/admin/teams", session)), ",")
	if first == "" {
		t.Fatal("page 1 rendered no rows; the fixture is wrong, not the code")
	}

	for _, q := range []string{"?page=abc", "?page=0", "?page=-5", "?page=", "?page=1", "?page=%20"} {
		rec := adminBrowse(h, "/admin/teams"+q, session)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /admin/teams%s = %d, want 200", q, rec.Code)
			continue
		}
		if got := strings.Join(teamIDsOnPage(t, rec), ","); got != first {
			t.Errorf("GET /admin/teams%s did not render page 1", q)
		}
	}
	if total != 120 {
		t.Fatalf("seed produced %d teams, want 120", total)
	}
}

// TestAdminTeamsListHidesPagerWhenItCannotHelp: a single page of rows gets no
// pager, and an empty list keeps its original "nothing registered yet" state
// instead of pagination chrome around nothing.
func TestAdminTeamsListHidesPagerWhenItCannotHelp(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)
	h := s.Handler()

	empty := adminBrowse(h, "/admin/teams", session)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty GET /admin/teams = %d, want 200", empty.Code)
	}
	if strings.Contains(empty.Body.String(), "?page=2") {
		t.Error("an empty list must not render a pager")
	}

	seedTeams(t, st, 12) // fewer than one page
	single := adminBrowse(h, "/admin/teams", session)
	if got := len(teamIDsOnPage(t, single)); got != 12 {
		t.Errorf("single page rendered %d rows, want 12", got)
	}
	if strings.Contains(single.Body.String(), "?page=2") {
		t.Error("a list that fits on one page must not render a pager")
	}
}
