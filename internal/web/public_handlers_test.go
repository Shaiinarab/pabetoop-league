// Package web tests the public handlers (TASK-013) with a real store + migrations
// + seed-like in-test data, against the real templates.
package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/standing"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// newTestServer creates a server backed by a fresh in-memory store with
// migrations applied. Each test gets its own isolated DB.
func newPublicTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewWithConfig(st, testConfig(t))
	return s, st
}

// seedTestData creates an active season + age groups + a premier competition
// with registered teams and some finished/scheduled matches for testing.
func seedTestData(t *testing.T, st *store.Store) (season *store.Season, age store.AgeGroup, comp *store.Competition, teams []store.Team) {
	t.Helper()

	// Season
	sid, err := st.CreateSeason("۱۴۰۵–۱۴۰۶", nil, nil)
	if err != nil {
		t.Fatalf("create season: %v", err)
	}
	if err := st.ActivateSeason(sid); err != nil {
		t.Fatalf("activate season: %v", err)
	}
	season, err = st.ActiveSeason()
	if err != nil {
		t.Fatalf("active season: %v", err)
	}

	// Age group
	if err := st.EnsureAgeGroups(12); err != nil {
		t.Fatalf("ensure age groups: %v", err)
	}
	ages, err := st.AgeGroups()
	if err != nil {
		t.Fatalf("age groups: %v", err)
	}
	age = ages[0]

	// Premier competition
	cid, err := st.CreateCompetition(season.ID, age.ID, "premier", nil, "لیگ برتر ۱۲ سال")
	if err != nil {
		t.Fatalf("create competition: %v", err)
	}
	comp, err = st.Competition(cid)
	if err != nil {
		t.Fatalf("get competition: %v", err)
	}

	// Clubs + teams
	clubA, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("create club A: %v", err)
	}
	clubB, err := st.CreateClub("نمونه پ")
	if err != nil {
		t.Fatalf("create club B: %v", err)
	}
	clubC, err := st.CreateClub("نمونه ث")
	if err != nil {
		t.Fatalf("create club C: %v", err)
	}

	tA, err := st.CreateTeam(clubA, "", "نمونه ب ۱")
	if err != nil {
		t.Fatalf("create team A: %v", err)
	}
	tB, err := st.CreateTeam(clubB, "", "نمونه پ ۱")
	if err != nil {
		t.Fatalf("create team B: %v", err)
	}
	tC, err := st.CreateTeam(clubC, "", "نمونه ث ۱")
	if err != nil {
		t.Fatalf("create team C: %v", err)
	}
	teams = []store.Team{{ID: tA, ClubName: "نمونه ب", DisplayName: "نمونه ب ۱"},
		{ID: tB, ClubName: "نمونه پ", DisplayName: "نمونه پ ۱"},
		{ID: tC, ClubName: "نمونه ث", DisplayName: "نمونه ث ۱"}}

	// Registrations
	if _, err := st.Register(cid, tA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if _, err := st.Register(cid, tB); err != nil {
		t.Fatalf("register B: %v", err)
	}
	if _, err := st.Register(cid, tC); err != nil {
		t.Fatalf("register C: %v", err)
	}

	// Matches: 2 finished, 1 scheduled
	// Match 1: نمونه ب ۱ 2-1 نمونه پ ۱ (finished, week 1)
	m1, err := st.CreateMatch(cid, tA, tB, intPtr(1), stringPtr("2026-10-12"), nil, nil)
	if err != nil {
		t.Fatalf("create match 1: %v", err)
	}
	if err := st.SetResult(m1, 2, 1); err != nil {
		t.Fatalf("set result 1: %v", err)
	}

	// Match 2: نمونه پ ۱ 1-1 نمونه ث ۱ (finished, week 2)
	m2, err := st.CreateMatch(cid, tB, tC, intPtr(2), stringPtr("2026-10-19"), nil, nil)
	if err != nil {
		t.Fatalf("create match 2: %v", err)
	}
	if err := st.SetResult(m2, 1, 1); err != nil {
		t.Fatalf("set result 2: %v", err)
	}

	// Match 3: نمونه ث ۱ vs نمونه ب ۱ (scheduled, week 3)
	_, err = st.CreateMatch(cid, tC, tA, intPtr(3), stringPtr("2026-10-26"), nil, nil)
	if err != nil {
		t.Fatalf("create match 3: %v", err)
	}
	return
}

func intPtr(i int) *int          { return &i }
func stringPtr(s string) *string { return &s }

// ---------- home page ----------

func TestPublicHomeEmptyState(t *testing.T) {
	// Nil store renders the empty home page (foundation behaviour).
	s := NewWithConfig(nil, testConfig(t))
	_ = s
	if err := s.TemplateErr(); err != nil {
		t.Fatalf("template parse failed: %v", err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	// Empty states present.
	if !strings.Contains(body, "هنوز نتیجه‌ای ثبت نشده است.") {
		t.Error("home should show empty results state")
	}
	if !strings.Contains(body, "مسابقه‌ای در پیش نیست.") {
		t.Error("home should show empty upcoming state")
	}
	// Basic chrome.
	// The site name is configuration, not a literal: assert against the value the
	// running deployment resolved, so this test survives a re-brand.
	for _, want := range []string{`dir="rtl"`, `/static/css/main.css`, SiteName} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in home page", want)
		}
	}
}

func TestPublicHomeWithData(t *testing.T) {
	s, st := newPublicTestServer(t)
	seedTestData(t, st)

	// Wire up the public routes (normally done in Handler(), but that's
	// TASK-008's file — outside this task's allowlist).
	mux := http.NewServeMux()
	s.RegisterPublicRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}

	// Finished match appears.
	if !strings.Contains(body, "نمونه ب ۱") || !strings.Contains(body, "نمونه پ ۱") {
		t.Error("home should list finished matches")
	}
	// Scores rendered as Persian digits.
	if !strings.Contains(body, "۲") || !strings.Contains(body, "۱") {
		t.Error("home should show Persian-digit scores")
	}
	// Stats populated.
	if !strings.Contains(body, "۳") { // 3 clubs
		t.Error("home stats should show club count")
	}
}

// ---------- age group page ----------

func TestPublicAgeGroupUnknownID(t *testing.T) {
	s, st := newPublicTestServer(t)
	seedTestData(t, st)

	// Wire up routes.
	mux := http.NewServeMux()
	s.RegisterPublicRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/age/9999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /age/9999 = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), notFoundMessage) {
		t.Error("should return Persian 404 message")
	}
}

func TestPublicAgeGroupListsCompetitions(t *testing.T) {
	s, st := newPublicTestServer(t)
	_, age, comp, _ := seedTestData(t, st)

	// Wire up routes.
	mux := http.NewServeMux()
	s.RegisterPublicRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/age/"+itoa(age.ID), nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /age/%d = %d, want 200", age.ID, rec.Code)
	}
	// Age group display name present.
	if !strings.Contains(body, age.DisplayName) {
		t.Error("age page should show age group name")
	}
	// Premier competition present (verify the link renders, not just the name).
	if !strings.Contains(body, "/competition/"+itoa(comp.ID)) {
		t.Error("age page should link to premier competition")
	}
	// Age-first navigation present (the age tab link).
	if !strings.Contains(body, "/age/"+itoa(age.ID)) {
		t.Error("age page should link to itself in nav")
	}
}

// ---------- competition page ----------

func TestPublicCompetitionUnknownID(t *testing.T) {
	s, st := newPublicTestServer(t)
	seedTestData(t, st)

	// Wire up routes.
	mux := http.NewServeMux()
	s.RegisterPublicRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/competition/9999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /competition/9999 = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), notFoundMessage) {
		t.Error("should return Persian 404 message")
	}
}

func TestPublicCompetitionStandingsMatchCompute(t *testing.T) {
	s, st := newPublicTestServer(t)
	_, _, comp, _ := seedTestData(t, st)

	// Compute expected standings independently.
	regInfos, err := st.Registrations(comp.ID)
	if err != nil {
		t.Fatalf("registrations: %v", err)
	}
	teamInputs := make([]standing.TeamInput, 0, len(regInfos))
	for _, r := range regInfos {
		teamInputs = append(teamInputs, standing.TeamInput{ID: r.TeamID, Name: r.TeamName})
	}
	finished, err := st.FinishedMatches(comp.ID)
	if err != nil {
		t.Fatalf("finished matches: %v", err)
	}
	matchInputs := make([]standing.MatchInput, 0, len(finished))
	for _, f := range finished {
		matchInputs = append(matchInputs, standing.MatchInput{
			HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID,
			HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals,
		})
	}
	expected := standing.Compute(teamInputs, matchInputs)

	// Wire up routes.
	mux := http.NewServeMux()
	s.RegisterPublicRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/competition/"+itoa(comp.ID), nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /competition/%d = %d, want 200", comp.ID, rec.Code)
	}

	// Each team should appear with its points.
	for _, row := range expected {
		// Team name + points.
		if !strings.Contains(body, row.TeamName) {
			t.Errorf("standings missing team %s", row.TeamName)
		}
		if !strings.Contains(body, strconv.Itoa(row.Points)) {
			t.Errorf("standings missing points %d for %s", row.Points, row.TeamName)
		}
	}
}

func TestPublicCompetitionTabs(t *testing.T) {
	s, st := newPublicTestServer(t)
	_, _, comp, _ := seedTestData(t, st)

	// Wire up routes.
	mux := http.NewServeMux()
	s.RegisterPublicRoutes(mux)

	// Default (table) tab.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/competition/"+itoa(comp.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /competition/%d = %d", comp.ID, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "جدول") {
		t.Error("default tab should show standings section")
	}

	// Results tab.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/competition/"+itoa(comp.ID)+"/results", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /competition/%d/results = %d", comp.ID, rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "نتایج") {
		t.Error("results tab should be active")
	}

	// Fixtures tab via query param.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/competition/"+itoa(comp.ID)+"?tab=fixtures", nil)
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("GET /competition/%d?tab=fixtures = %d", comp.ID, rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "برنامه") {
		t.Error("fixtures tab should be active")
	}
}

// ---------- helper ----------

func itoa(i int64) string {
	return strconv.FormatInt(i, 10)
}
