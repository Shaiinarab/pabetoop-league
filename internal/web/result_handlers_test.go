// Tests for the quick result entry workflow (TASK-011): real store + migrations
// + the real templates, driven through the full middleware chain (CSRF + auth)
// via Server.Handler(), exactly as a browser/htmx client would.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/standing"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// ---------- helpers ----------

// newResultsTestServer: store-backed server with the TASK-013 seed data plus a
// logged-in admin (session + CSRF cookies).
func newResultsTestServer(t *testing.T) (s *Server, st *store.Store, comp *store.Competition, session, csrf *http.Cookie) {
	t.Helper()
	s, st = newPublicTestServer(t)
	_, _, comp, _ = seedTestData(t, st)
	session, csrf = loginTestAdmin(t, s)
	return s, st, comp, session, csrf
}

// loginTestAdmin performs the real login flow and returns the session and CSRF
// cookies the subsequent requests must carry.
func loginTestAdmin(t *testing.T, s *Server) (session, csrf *http.Cookie) {
	t.Helper()
	h := s.Handler()

	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, adminLoginPath, nil))
	for _, c := range getRec.Result().Cookies() {
		if c.Name == csrfCookie {
			csrf = c
		}
	}
	if csrf == nil {
		t.Fatal("login page did not issue a CSRF cookie")
	}

	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}, csrfField: {csrf.Value}}
	postRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, adminLoginPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	h.ServeHTTP(postRec, req)
	if postRec.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303 (body: %s)", postRec.Code, postRec.Body.String())
	}
	for _, c := range postRec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login did not set a session cookie")
	}
	return session, csrf
}

// resultGet issues an authenticated GET through the full handler chain.
func resultGet(h http.Handler, path string, session *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// resultPost issues an htmx POST with the CSRF header (the real client contract);
// nil cookies simulate anonymous/CSRF-less requests.
func resultPost(h http.Handler, path string, form url.Values, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
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

func matchIDs(t *testing.T, st *store.Store, compID int64, status string) []int64 {
	t.Helper()
	ms, err := st.Matches(compID, nil, statusPtr(status))
	if err != nil {
		t.Fatalf("matches(%s): %v", status, err)
	}
	ids := make([]int64, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

func isPersianDigits(s string) bool {
	for _, r := range s {
		if r >= '۰' && r <= '۹' {
			return true
		}
	}
	return false
}

// ---------- page ----------

func TestResultsPageRendersWeekRows(t *testing.T) {
	s, st, comp, session, _ := newResultsTestServer(t)
	scheduled := matchIDs(t, st, comp.ID, matchStatusScheduled)
	if len(scheduled) == 0 {
		t.Fatal("seed data must contain a scheduled match")
	}
	id := scheduled[0]

	rec := resultGet(s.Handler(), "/admin/results", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/results = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"ثبت سریع نتیجه",
		`name="competition"`,
		`name="week"`,
		`name="home_score"`,
		`name="away_score"`,
		"/admin/results/" + strconv.FormatInt(id, 10),
		"نمونه ث ۱",
		"نمونه ب ۱",
		"برنامه‌ریزی‌شده",
		"جدول",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("results page missing %q", want)
		}
	}
}

func TestResultsPageDefaultsToLowestScheduledWeek(t *testing.T) {
	s, st, comp, session, _ := newResultsTestServer(t)
	// Scheduled match in the seed sits in week 3.
	ms, err := st.Matches(comp.ID, statusPtr("3"), nil)
	if err != nil || len(ms) == 0 {
		t.Fatalf("expected a week-3 match (err=%v, n=%d)", err, len(ms))
	}
	rec := resultGet(s.Handler(), "/admin/results", session)
	if got := rec.Body.String(); !strings.Contains(got, `value="3" selected`) {
		t.Errorf("week select should default to 3 (lowest scheduled week)")
	}
}

func TestResultsPageNilStoreRendersEmptyState(t *testing.T) {
	s := newTestServer(t) // nil store
	session, _ := loginTestAdmin(t, s)
	rec := resultGet(s.Handler(), "/admin/results", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil-store GET /admin/results = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "مسابقه‌ای برای فصل جاری ثبت نشده است.") {
		t.Error("nil-store results page must render the Persian empty state")
	}
}

// ---------- set / clear ----------

func TestSetResultSavesRowAndTriggersStandingsRefresh(t *testing.T) {
	s, st, comp, session, csrf := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusScheduled)[0]

	rec := resultPost(s.Handler(), "/admin/results/"+strconv.FormatInt(id, 10),
		url.Values{"home_score": {"3"}, "away_score": {"2"}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("set result = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ثبت شد") {
		t.Error("saved row must carry the Persian success flash")
	}
	if !strings.Contains(body, "انجام‌شده") {
		t.Error("saved row must render the finished badge")
	}
	if got := rec.Header().Get("HX-Trigger"); got != resultSavedEvent {
		t.Errorf("HX-Trigger = %q, want %q (standings refresh)", got, resultSavedEvent)
	}

	m, err := st.Match(id)
	if err != nil {
		t.Fatalf("reload match: %v", err)
	}
	if m.Status != matchStatusFinished || m.HomeScore == nil || m.AwayScore == nil {
		t.Fatalf("match not finished after save: %+v", m)
	}
	if *m.HomeScore != 3 || *m.AwayScore != 2 {
		t.Errorf("scores = %d-%d, want 3-2", *m.HomeScore, *m.AwayScore)
	}
}

func TestSetResultAcceptsPersianDigits(t *testing.T) {
	s, st, comp, session, csrf := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusScheduled)[0]

	rec := resultPost(s.Handler(), "/admin/results/"+strconv.FormatInt(id, 10),
		url.Values{"home_score": {"۳"}, "away_score": {"۲"}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("set result with Persian digits = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	m, _ := st.Match(id)
	if m.HomeScore == nil || *m.HomeScore != 3 || m.AwayScore == nil || *m.AwayScore != 2 {
		t.Fatalf("Persian digits not decoded: %+v", m)
	}
}

func TestSetResultRejectsInvalidScore(t *testing.T) {
	s, st, comp, session, csrf := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusScheduled)[0]
	path := "/admin/results/" + strconv.FormatInt(id, 10)

	for _, bad := range []url.Values{
		{"home_score": {"abc"}, "away_score": {"1"}},
		{"home_score": {"-1"}, "away_score": {"1"}},
		{"home_score": {""}, "away_score": {"1"}},
		{"home_score": {"100"}, "away_score": {"1"}},
	} {
		rec := resultPost(s.Handler(), path, bad, session, csrf)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid score %v = %d, want 422", bad, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "عددی بین ۰ تا ۹۹") {
			t.Errorf("422 body must carry the Persian score error, got: %s", rec.Body.String())
		}
	}
	m, _ := st.Match(id)
	if m.Status != matchStatusScheduled {
		t.Errorf("match must stay scheduled after rejected input, got %q", m.Status)
	}
}

func TestSetResultRepairsFinishedRow(t *testing.T) {
	s, st, comp, session, csrf := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusFinished)[0] // seed: 2-1

	// Finished rows stay editable: the handler runs the supported clear-then-set.
	rec := resultPost(s.Handler(), "/admin/results/"+strconv.FormatInt(id, 10),
		url.Values{"home_score": {"0"}, "away_score": {"0"}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("repair = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	m, _ := st.Match(id)
	if m.Status != matchStatusFinished || m.HomeScore == nil || m.AwayScore == nil || *m.HomeScore != 0 || *m.AwayScore != 0 {
		t.Fatalf("repair did not land: %+v", m)
	}
}

func TestClearResultReturnsScheduledRow(t *testing.T) {
	s, st, comp, session, csrf := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusFinished)[0]

	rec := resultPost(s.Handler(), "/admin/results/"+strconv.FormatInt(id, 10)+"/clear",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "برنامه‌ریزی‌شده") {
		t.Error("cleared row must render the scheduled badge")
	}
	if got := rec.Header().Get("HX-Trigger"); got != resultSavedEvent {
		t.Errorf("HX-Trigger = %q, want %q", got, resultSavedEvent)
	}
	m, _ := st.Match(id)
	if m.Status != matchStatusScheduled || m.HomeScore != nil || m.AwayScore != nil {
		t.Fatalf("clear did not reset the match: %+v", m)
	}
}

func TestResultSetUnknownMatchPersianNotFound(t *testing.T) {
	s, _, _, session, csrf := newResultsTestServer(t)
	rec := resultPost(s.Handler(), "/admin/results/999999",
		url.Values{"home_score": {"1"}, "away_score": {"0"}}, session, csrf)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown match = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "مسابقه پیدا نشد.") {
		t.Error("expected Persian not-found message")
	}
}

// ---------- security ----------

func TestResultPostRequiresCSRF(t *testing.T) {
	s, st, comp, session, _ := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusScheduled)[0]
	rec := resultPost(s.Handler(), "/admin/results/"+strconv.FormatInt(id, 10),
		url.Values{"home_score": {"1"}, "away_score": {"0"}}, session, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("CSRF-less POST = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "نشست شما منقضی شده است") {
		t.Error("expected Persian CSRF message")
	}
}

func TestResultPostRequiresSession(t *testing.T) {
	s, st, comp, _, csrf := newResultsTestServer(t)
	id := matchIDs(t, st, comp.ID, matchStatusScheduled)[0]
	rec := resultPost(s.Handler(), "/admin/results/"+strconv.FormatInt(id, 10),
		url.Values{"home_score": {"1"}, "away_score": {"0"}}, nil, csrf)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session-less POST = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "باید وارد شوید") {
		t.Error("expected Persian auth message")
	}
}

func TestResultsPageRedirectsAnonymous(t *testing.T) {
	s, _, _, _, _ := newResultsTestServer(t)
	rec := resultGet(s.Handler(), "/admin/results", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != adminLoginPath {
		t.Fatalf("anonymous GET /admin/results = %d %q, want 303 → %s",
			rec.Code, rec.Header().Get("Location"), adminLoginPath)
	}
}

// ---------- standings partial ----------

func TestStandingsPartialRecomputesFromFinishedMatches(t *testing.T) {
	s, st, comp, session, _ := newResultsTestServer(t)

	rec := resultGet(s.Handler(), "/admin/results/standings?competition="+strconv.FormatInt(comp.ID, 10), session)
	if rec.Code != http.StatusOK {
		t.Fatalf("standings partial = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "امتیاز") {
		t.Error("standings partial missing header")
	}

	// The page must agree with the single ranking authority.
	regs, err := st.Registrations(comp.ID)
	if err != nil {
		t.Fatalf("registrations: %v", err)
	}
	teams := make([]standing.TeamInput, 0, len(regs))
	for _, r := range regs {
		teams = append(teams, standing.TeamInput{ID: r.TeamID, Name: r.TeamName})
	}
	finished, err := st.FinishedMatches(comp.ID)
	if err != nil {
		t.Fatalf("finished: %v", err)
	}
	inputs := make([]standing.MatchInput, 0, len(finished))
	for _, f := range finished {
		inputs = append(inputs, standing.MatchInput{
			HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID,
			HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals,
		})
	}
	rows := standing.Compute(teams, inputs)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Every team appears, in engine order.
	prev := -1
	for _, row := range rows {
		idx := strings.Index(body, row.TeamName)
		if idx < 0 {
			t.Fatalf("team %q missing from standings partial", row.TeamName)
		}
		if idx < prev {
			t.Fatalf("team %q out of engine order in partial", row.TeamName)
		}
		prev = idx
	}
	if !isPersianDigits(body) {
		t.Error("standings numbers must render as Persian digits")
	}
}

// ---------- template set ----------

func TestResultTemplatesParse(t *testing.T) {
	tmpl, err := resultPageSet(testConfig(t).TemplatesDir)
	if err != nil {
		t.Fatalf("result page set parse: %v", err)
	}
	for _, name := range []string{"results_board", "result_row", "standings_table"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q not defined in the results set", name)
		}
	}
}

// TestResultRowRendersBothStates keeps the partial's contract honest: a
// scheduled row offers inputs + ثبت, a finished row offers inputs + clear.
func TestResultRowRendersBothStates(t *testing.T) {
	iso := "2026-10-12"
	hs, as := 2, 1
	tmpl, err := resultPageSet(testConfig(t).TemplatesDir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	scheduled := resultRowView{Match: storeMatch("نمونه ب ۱", "نمونه پ ۱", matchStatusScheduled, &iso, nil, nil), CSRFToken: "tok"}
	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "result_row", scheduled); err != nil {
		t.Fatalf("execute scheduled row: %v", err)
	}
	got := rec.Body.String()
	if !strings.Contains(got, "برنامه‌ریزی‌شده") || strings.Contains(got, "پاک کردن نتیجه") {
		t.Errorf("scheduled row state wrong: %s", got)
	}
	if !strings.Contains(got, `value=""`) {
		t.Error("scheduled row inputs must start empty")
	}

	finished := resultRowView{
		Match:     storeMatch("نمونه ب ۱", "نمونه پ ۱", matchStatusFinished, &iso, &hs, &as),
		HomeValue: "2", AwayValue: "1", Finished: true, Saved: true, CSRFToken: "tok",
	}
	rec2 := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec2, "result_row", finished); err != nil {
		t.Fatalf("execute finished row: %v", err)
	}
	got2 := rec2.Body.String()
	for _, want := range []string{"انجام‌شده", "پاک کردن نتیجه", "ثبت شد", "success-flash", `value="2"`, `value="1"`} {
		if !strings.Contains(got2, want) {
			t.Errorf("finished row missing %q: %s", want, got2)
		}
	}
}
