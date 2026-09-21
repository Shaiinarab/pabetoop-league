// Acceptance tests for the TASK-015 competitions + registrations admin surface.
//
// Harness: a real store (`store.OpenMemory` + `Migrate`) behind
// `NewWithConfig(st, testConfig(t))`, driven through `Server.Handler()` so the
// route table AND the `requireAdmin` wrapping are part of what is proven —
// `RegisterCompetitionRoutes` is wired there (server.go). Auth is the real login
// flow (`loginTestAdmin`, shared with TASK-011/018); every mutation POST is an
// htmx request, exactly as admin_base.html issues them.
//
// Assertions use the store's real Persian strings (the api.go/store.go
// contract), never paraphrases — a re-worded message is a product bug, not a
// test detail.
package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// competitionByName returns a competition by exact official display name.
func competitionByName(t *testing.T, st *store.Store, name string) (store.Competition, bool) {
	t.Helper()
	comps, err := st.Competitions(nil, nil)
	if err != nil {
		t.Fatalf("competitions: %v", err)
	}
	for _, c := range comps {
		if c.DisplayName == name {
			return c, true
		}
	}
	return store.Competition{}, false
}

// registrationForTeam returns the registration row of one team in a competition.
func registrationForTeam(t *testing.T, st *store.Store, compID, teamID int64) (store.Registration, bool) {
	t.Helper()
	regs, err := st.Registrations(compID)
	if err != nil {
		t.Fatalf("registrations: %v", err)
	}
	for _, r := range regs {
		if r.TeamID == teamID {
			return r, true
		}
	}
	return store.Registration{}, false
}

// ---------- list ----------

func TestCompetitionListRendersGroupedRows(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/competitions", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/competitions = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"لیگ برتر ۱۲ سال",      // the seeded competition's authority-controlled name
		"لیگ برتر",             // level label column
		"۱۲ سال",               // age category heading
		"/admin/competitions/", // the registrations link per row
		"عملیات",               // the edit/delete column
	} {
		if !strings.Contains(body, want) {
			t.Errorf("competitions list missing %q", want)
		}
	}
}

func TestCompetitionListEmptyStateWithoutSeason(t *testing.T) {
	s, _ := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/competitions", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/competitions = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "هنوز فصلی ثبت نشده است") {
		t.Errorf("empty list must explain the missing season in Persian, got: %s", rec.Body.String())
	}
}

// ---------- create ----------

func TestCompetitionCreateValidationError(t *testing.T) {
	s, st := newAdminTestServer(t)
	season, age, _, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)
	before, _ := st.Competitions(nil, nil)

	rec := adminMutate(s.Handler(), "/admin/competitions", url.Values{
		"season":       {strconv.FormatInt(season.ID, 10)},
		"age_group":    {strconv.FormatInt(age.ID, 10)},
		"level":        {"league1"},
		"group_name":   {"A"},
		"display_name": {""}, // the field under test
	}, session, csrf)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty display_name = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "نام مسابقات الزامی است.") {
		t.Errorf("422 body must carry the Persian field error, got: %s", rec.Body.String())
	}
	after, _ := st.Competitions(nil, nil)
	if len(after) != len(before) {
		t.Errorf("refused create must not write a row (before=%d, after=%d)", len(before), len(after))
	}
}

func TestCompetitionCreateLeague1Succeeds(t *testing.T) {
	s, st := newAdminTestServer(t)
	season, age, _, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions", url.Values{
		"season":       {strconv.FormatInt(season.ID, 10)},
		"age_group":    {strconv.FormatInt(age.ID, 10)},
		"level":        {"league1"},
		"group_name":   {"A"},
		"display_name": {"لیگ ۱ گروه A ۱۲ سال"},
	}, session, csrf)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid create = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	comp, ok := competitionByName(t, st, "لیگ ۱ گروه A ۱۲ سال")
	if !ok {
		t.Fatal("created competition not persisted")
	}
	if comp.GroupName == nil || *comp.GroupName != "A" {
		t.Errorf("group label = %v, want A", comp.GroupName)
	}
	assertAuditRow(t, st, "insert", "competition", comp.ID)
}

// TestCompetitionCreateDuplicatePremierRefused pins the §7 rule that an age
// category has ONE Premier League. It is deliberately strict: SQLite treats
// NULLs as distinct in a UNIQUE index, and premier rows carry group_name = NULL,
// so the table-level UNIQUE (season, age, level, group_name) cannot enforce this
// on its own — a partial unique index is the real backstop.
func TestCompetitionCreateDuplicatePremierRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	season, age, comp, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions", url.Values{
		"season":       {strconv.FormatInt(season.ID, 10)},
		"age_group":    {strconv.FormatInt(age.ID, 10)},
		"level":        {"premier"},
		"group_name":   {""},
		"display_name": {"لیگ برتر تکراری"},
	}, session, csrf)

	if _, ok := competitionByName(t, st, "لیگ برتر تکراری"); ok {
		t.Fatalf("second premier competition for age %d must not be created (first: #%d)", age.ID, comp.ID)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate premier = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "لیگ برتر قبلاً ساخته شده است") {
		t.Errorf("422 body must carry the store's Persian refusal, got: %s", rec.Body.String())
	}
}

// ---------- edit ----------

// TestCompetitionRenamePreservesOfficialName: official names are
// authority-controlled (D7) — trimmed at the edges, never normalised inside.
func TestCompetitionRenamePreservesOfficialName(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	// Two interior spaces and a ZWNJ: a normaliser would fold these away.
	const typed = "لیگ  برتر ۱۲ سال (بازبینی)"
	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/edit",
		url.Values{"display_name": {"  " + typed + "  "}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	got, err := st.Competition(comp.ID)
	if err != nil {
		t.Fatalf("competition: %v", err)
	}
	if got.DisplayName != typed {
		t.Errorf("display name = %q, want %q (whitespace-trimmed only, never normalised)", got.DisplayName, typed)
	}
	if !strings.Contains(rec.Body.String(), typed) {
		t.Errorf("swapped row must show the new name, got: %s", rec.Body.String())
	}
	assertAuditRow(t, st, "update", "competition", comp.ID)
}

func TestCompetitionEditLeague1GroupLabel(t *testing.T) {
	s, st := newAdminTestServer(t)
	season, age, _, _ := seedTestData(t, st)
	cid, err := st.CreateCompetition(season.ID, age.ID, "league1", stringPtr("A"), "لیگ ۱ ۱۲ سال")
	if err != nil {
		t.Fatalf("create league1: %v", err)
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(cid, 10)+"/edit",
		url.Values{"display_name": {"لیگ ۱ ۱۲ سال"}, "group_name": {"B"}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("group edit = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	got, err := st.Competition(cid)
	if err != nil {
		t.Fatalf("competition: %v", err)
	}
	if got.GroupName == nil || *got.GroupName != "B" {
		t.Errorf("group = %v, want B", got.GroupName)
	}
	assertAuditRow(t, st, "update", "competition", cid)
}

// TestCompetitionEditRefusedKeepsRow: a refused edit answers 400 with the row and
// the store's verbatim message (the club-rename pattern); the old name survives.
func TestCompetitionEditRefusedKeepsRow(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/edit",
		url.Values{"display_name": {"   "}}, session, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank rename = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "نمی‌تواند خالی باشد") {
		t.Errorf("400 body must carry the store's Persian message, got: %s", rec.Body.String())
	}
	got, err := st.Competition(comp.ID)
	if err != nil {
		t.Fatalf("competition: %v", err)
	}
	if got.DisplayName != comp.DisplayName {
		t.Errorf("refused edit changed the name: %q → %q", comp.DisplayName, got.DisplayName)
	}
}

// ---------- delete ----------

func TestCompetitionDeleteBlockedByRegistrations(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/delete",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete with registrations = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ثبت‌نام") {
		t.Errorf("400 must explain the blocking registrations, got: %s", rec.Body.String())
	}
	if _, err := st.Competition(comp.ID); err != nil {
		t.Errorf("blocked delete must leave the competition in place: %v", err)
	}
}

func TestCompetitionDeleteEmptySucceeds(t *testing.T) {
	s, st := newAdminTestServer(t)
	season, age, _, _ := seedTestData(t, st)
	cid, err := st.CreateCompetition(season.ID, age.ID, "league1", stringPtr("Z"), "لیگ ۱ گروه Z")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(cid, 10)+"/delete",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete empty = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("successful delete must answer an empty body (htmx removes the row), got %d bytes", rec.Body.Len())
	}
	if _, err := st.Competition(cid); err == nil {
		t.Error("deleted competition still present")
	}
	assertAuditRow(t, st, "delete", "competition", cid)
}

// ---------- registrations ----------

func TestCompetitionRegisterSucceeds(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	clubID, err := st.CreateClub("باشگاه نمونه")
	if err != nil {
		t.Fatalf("club: %v", err)
	}
	teamID, err := st.CreateTeam(clubID, "", "نمونه ۱")
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/register",
		url.Values{"team_id": {strconv.FormatInt(teamID, 10)}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("register = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "نمونه ۱") {
		t.Errorf("swapped table must show the newly registered team, got: %s", rec.Body.String())
	}
	if _, ok := registrationForTeam(t, st, comp.ID, teamID); !ok {
		t.Error("registration not persisted")
	}
}

func TestCompetitionRegisterDuplicateRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/register",
		url.Values{"team_id": {strconv.FormatInt(teams[0].ID, 10)}}, session, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate register = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "قبلاً در") {
		t.Errorf("422 must carry the store's duplicate message, got: %s", rec.Body.String())
	}
}

// TestCompetitionRegisterPremierClubRuleRefused: one club, one team per Premier
// League per age (§7 rule 3). The message must NAME the blocking club so the
// operator can act instead of guessing.
func TestCompetitionRegisterPremierClubRuleRefused(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	club, ok := clubByName(t, st, "نمونه ب")
	if !ok {
		t.Fatal("seeded club نمونه ب missing")
	}
	// A distinct label — CreateTeam refuses a second team with the same label
	// for one club (the seeded team already holds the empty label).
	secondTeam, err := st.CreateTeam(club.ID, "2", "نمونه ب ۲")
	if err != nil {
		t.Fatalf("second team: %v", err)
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/register",
		url.Values{"team_id": {strconv.FormatInt(secondTeam, 10)}}, session, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("premier club-rule violation = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد") {
		t.Errorf("422 must carry the store's premier rule message, got: %s", body)
	}
	if !strings.Contains(body, "نمونه ب") {
		t.Errorf("422 must name the blocking club, got: %s", body)
	}
	if _, ok := registrationForTeam(t, st, comp.ID, secondTeam); ok {
		t.Error("refused registration must not be persisted")
	}
}

func TestCompetitionUnregisterBlockedWhenTeamHasMatch(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	reg, ok := registrationForTeam(t, st, comp.ID, teams[0].ID)
	if !ok {
		t.Fatal("seeded registration missing")
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/unregister",
		url.Values{"registration_id": {strconv.FormatInt(reg.ID, 10)}}, session, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unregister with a match = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "بازی ثبت‌شده دارد") {
		t.Errorf("400 must carry the store's match message, got: %s", rec.Body.String())
	}
	if _, ok := registrationForTeam(t, st, comp.ID, teams[0].ID); !ok {
		t.Error("blocked unregister must leave the registration in place")
	}
}

func TestCompetitionUnregisterSucceedsWithoutMatches(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	clubID, err := st.CreateClub("باشگاه نمونه")
	if err != nil {
		t.Fatalf("club: %v", err)
	}
	teamID, err := st.CreateTeam(clubID, "", "نمونه ۱")
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	regID, err := st.Register(comp.ID, teamID)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	session, csrf := loginTestAdmin(t, s)

	rec := adminMutate(s.Handler(), "/admin/competitions/"+strconv.FormatInt(comp.ID, 10)+"/unregister",
		url.Values{"registration_id": {strconv.FormatInt(regID, 10)}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("unregister = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, ok := registrationForTeam(t, st, comp.ID, teamID); ok {
		t.Error("unregister not persisted")
	}
	assertAuditRow(t, st, "delete", "registration", regID)
}

// ---------- CSRF ----------

// TestCompetitionMutationsRequireCSRF: every write is a csrfProtect subject; a
// token-less POST must not reach a handler (§7 rule / security.go).
func TestCompetitionMutationsRequireCSRF(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	reg, ok := registrationForTeam(t, st, comp.ID, teams[0].ID)
	if !ok {
		t.Fatal("seeded registration missing")
	}
	session, _ := loginTestAdmin(t, s)
	compID := strconv.FormatInt(comp.ID, 10)

	cases := []struct {
		name string
		path string
		form url.Values
	}{
		{"create", "/admin/competitions", url.Values{"display_name": {"x"}}},
		{"edit", "/admin/competitions/" + compID + "/edit", url.Values{"display_name": {"x"}}},
		{"delete", "/admin/competitions/" + compID + "/delete", url.Values{}},
		{"register", "/admin/competitions/" + compID + "/register", url.Values{"team_id": {strconv.FormatInt(teams[0].ID, 10)}}},
		{"unregister", "/admin/competitions/" + compID + "/unregister", url.Values{"registration_id": {strconv.FormatInt(reg.ID, 10)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := adminMutate(s.Handler(), tc.path, tc.form, session, nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s without CSRF = %d, want 403", tc.path, rec.Code)
			}
		})
	}
}
