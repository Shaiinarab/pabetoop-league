// Quick result entry (TASK-011) — the administrator's flagship workflow
// (PROJECT_SPEC §5/§20: hundreds of saves per season, speed is the feature).
//
// Design notes:
//   - Every match of the selected week renders as one htmx form
//     (`.quick-entry-row` + `.score-input`); Enter submits the row, and app.ts
//     moves focus to the next row's empty score input after a successful save.
//   - Finished rows stay editable in place: SetResult accepts SCHEDULED matches
//     only (frozen store contract), so saving over a finished row performs the
//     supported clear-then-set sequence server-side and surfaces a Persian
//     error row if either step fails (never a silent half-state).
//   - Handlers hold no business logic: validation/audit live in the store.
//   - Standings are recomputed, never stored (D6). The standings panel listens
//     for the htmx `resultSaved` event emitted via the HX-Trigger response
//     header by both the save and the clear responses.
//
// Routes (all behind requireAdmin + the global CSRF middleware):
//
//	GET  /admin/results?competition=&week=   picker + rows + standings
//	GET  /admin/results/standings?competition=  standings table partial
//	POST /admin/results/{id}                 set/repair a result (row partial)
//	POST /admin/results/{id}/clear           clear a result (row partial)
package web

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shaiinarab/pabetoop-league/internal/standing"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// Match status values (store contract, api.go).
const (
	matchStatusFinished  = "finished"
	matchStatusScheduled = "scheduled"
)

// resultSavedEvent is the htmx 4 custom event name (lowercase, no colons —
// DECISIONS.md D2) sent through the HX-Trigger response header so the standings
// panel refreshes after a result changes.
const resultSavedEvent = "resultSaved"

// maxScore bounds one team's goals; far above any youth match and keeps the
// digit parser bounded.
const maxScore = 99

// RegisterResultRoutes mounts the quick result entry workflow on mux. Called by
// Handler() in server.go (TASK-008's file — the integration line the Lead
// wires, same as RegisterAdminRoutes).
func (s *Server) RegisterResultRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/results", s.requireAdmin(http.HandlerFunc(s.handleAdminResults)))
	mux.Handle("GET /admin/results/standings", s.requireAdmin(http.HandlerFunc(s.handleAdminResultsStandings)))
	mux.Handle("POST /admin/results/{id}", s.requireAdmin(http.HandlerFunc(s.handleAdminResultSet)))
	mux.Handle("POST /admin/results/{id}/clear", s.requireAdmin(http.HandlerFunc(s.handleAdminResultClear)))
}

// ---------- view shapes (documented in the template headers) ----------

// resultOption is one selectable competition.
type resultOption struct {
	ID   int64
	Name string
}

// resultAgeGroup groups one age group's competitions for the <optgroup> picker.
type resultAgeGroup struct {
	DisplayName  string
	Competitions []resultOption
}

// resultRowView is one match row (scheduled = inputs, finished = editable too).
type resultRowView struct {
	Match        store.Match
	HomeValue    string // Latin digits (main.css contract for .score-input values)
	AwayValue    string
	Finished     bool
	Saved        bool
	ErrorMessage string
	CSRFToken    string
}

// resultsView is the full page payload.
type resultsView struct {
	adminPage
	SeasonName            string
	AgeGroups             []resultAgeGroup
	SelectedCompetitionID int64
	SelectedName          string
	Week                  int
	Weeks                 []int
	Rows                  []resultRowView
	StandingsURL          string
	Table                 standingsView
}

// ---------- template set ----------

// The results pages are parsed as their own set: admin_base.html (chrome) +
// admin_result_base.html (the swappable board fragment) + partials + the page.
// render.go's adminPageFiles map is TASK-010's lane, so the results set keeps
// its own cache instead of extending it.
var (
	resultTmplMu    sync.Mutex
	resultTmplCache = map[string]*template.Template{}
	resultTmplErr   = map[string]error{}
)

func resultPageSet(dir string) (*template.Template, error) {
	resultTmplMu.Lock()
	defer resultTmplMu.Unlock()

	if t, ok := resultTmplCache[dir]; ok {
		return t, nil
	}
	if err := resultTmplErr[dir]; err != nil {
		return nil, err
	}
	t, err := template.New("resultbase").Funcs(templateFuncMap(nil)).ParseFS(os.DirFS(dir),
		"admin_base.html", "admin_result_base.html", "partials/*.html", "admin/results.html")
	if err != nil {
		resultTmplErr[dir] = err
		return nil, err
	}
	resultTmplCache[dir] = t
	return t, nil
}

// renderResultsPage renders the full page buffer-first so a late template error
// still yields a Persian 500 instead of a half-written page.
func (s *Server) renderResultsPage(w http.ResponseWriter, view resultsView, status int) {
	if status == 0 {
		status = http.StatusOK
	}
	t, err := resultPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("result templates unavailable", "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "admin_base.html", view); err != nil {
		s.log.Error("render results page failed", "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderResultPartial swaps one htmx fragment (a row or the standings table).
func (s *Server) renderResultPartial(w http.ResponseWriter, name string, status int, data any) {
	t, err := resultPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("result templates unavailable", "error", err, "partial", name)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render result partial failed", "partial", name, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// ---------- page handler ----------

func (s *Server) handleAdminResults(w http.ResponseWriter, r *http.Request) {
	// Chrome is built inline (not via adminChrome) so the page still renders on
	// a nil store, matching the foundation's nil-store contract for the
	// unauthenticated shell; adminChrome dereferences s.store unconditionally.
	view := resultsView{adminPage: adminPage{
		SiteName:  SiteName,
		NavActive: "results",
		CSRFToken: s.CSRFToken(r),
		Flash:     takeFlash(w, r),
	}}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "ثبت نتیجه"}}
	if s.store == nil {
		s.renderResultsPage(w, view, http.StatusOK)
		return
	}
	if season, err := s.store.ActiveSeason(); err == nil && season != nil {
		view.SeasonName = season.Name
	}

	comps, err := s.activeSeasonCompetitions()
	if err != nil {
		s.log.Error("results: competitions", "error", err)
	}
	ageNames := map[int64]string{}
	if ages, err := s.store.AgeGroups(); err == nil {
		for _, a := range ages {
			ageNames[a.ID] = a.DisplayName
		}
	}

	// Group competitions by age group, premier first then League 1 groups.
	byAge := map[int64][]resultOption{}
	for _, c := range comps {
		byAge[c.AgeGroupID] = append(byAge[c.AgeGroupID], resultOption{ID: c.ID, Name: c.DisplayName})
	}
	sort.SliceStable(comps, func(i, j int) bool {
		if comps[i].AgeGroupAge != comps[j].AgeGroupAge {
			return comps[i].AgeGroupAge < comps[j].AgeGroupAge
		}
		if comps[i].Level != comps[j].Level {
			return comps[i].Level == levelPremier
		}
		return groupKey(comps[i]) < groupKey(comps[j])
	})
	view.AgeGroups = []resultAgeGroup{}
	seenAge := map[int64]bool{}
	for _, c := range comps {
		if seenAge[c.AgeGroupID] {
			continue
		}
		seenAge[c.AgeGroupID] = true
		view.AgeGroups = append(view.AgeGroups, resultAgeGroup{
			DisplayName:  ageNames[c.AgeGroupID],
			Competitions: byAge[c.AgeGroupID],
		})
	}
	if len(comps) == 0 {
		s.renderResultsPage(w, view, http.StatusOK)
		return
	}

	// Selected competition: explicit ?competition= when valid, otherwise the
	// first competition that has a scheduled match, else the first one.
	sel := int64(0)
	if q, ok := parsePositiveInt(r.URL.Query().Get("competition")); ok {
		for _, c := range comps {
			if c.ID == int64(q) {
				sel = c.ID
				break
			}
		}
	}
	if sel == 0 {
		for _, c := range comps {
			if ms, err := s.store.Matches(c.ID, nil, statusPtr(matchStatusScheduled)); err == nil && len(ms) > 0 {
				sel = c.ID
				break
			}
		}
	}
	if sel == 0 {
		sel = comps[0].ID
	}
	view.SelectedCompetitionID = sel
	for _, c := range comps {
		if c.ID == sel {
			view.SelectedName = c.DisplayName
			break
		}
	}
	if view.SelectedName != "" {
		view.Breadcrumb = append(view.Breadcrumb, breadcrumbItem{Label: view.SelectedName})
	}

	all, err := s.store.Matches(sel, nil, nil)
	if err != nil {
		s.log.Error("results: matches", "error", err, "competition", sel)
		s.renderResultsPage(w, view, http.StatusOK)
		return
	}

	view.Weeks = distinctWeeks(all)
	week := 0
	if q, ok := parsePositiveInt(r.URL.Query().Get("week")); ok {
		week = q
	}
	if week == 0 {
		week = firstScheduledWeek(all)
	}
	if week == 0 && len(view.Weeks) > 0 {
		week = view.Weeks[0]
	}
	view.Week = week

	rows := all
	if week > 0 {
		rows = nil
		for _, m := range all {
			if m.Week != nil && *m.Week == week {
				rows = append(rows, m)
			}
		}
	}
	for _, m := range rows {
		row := newResultRow(m)
		row.CSRFToken = view.CSRFToken
		view.Rows = append(view.Rows, row)
	}

	view.StandingsURL = "/admin/results/standings?competition=" + strconv.FormatInt(sel, 10)
	if table, err := s.computeStandings(sel); err == nil {
		view.Table = table
	} else {
		s.log.Error("results: standings", "error", err, "competition", sel)
	}
	s.renderResultsPage(w, view, http.StatusOK)
}

// ---------- mutation handlers ----------

func (s *Server) handleAdminResultSet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقه نامعتبر است.")
		return
	}
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, dataErrorMessage)
		return
	}
	m, err := s.store.Match(id)
	if errors.Is(err, store.ErrNotFound) {
		writePersianError(w, http.StatusNotFound, "مسابقه پیدا نشد.")
		return
	}
	if err != nil {
		s.log.Error("result set: load match", "error", err, "match", id)
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}

	home, okHome := parseScore(r.PostFormValue("home_score"))
	away, okAway := parseScore(r.PostFormValue("away_score"))
	if !okHome || !okAway {
		row := resultRowWithValues(*m, home, away)
		row.ErrorMessage = "نتیجه باید عددی بین ۰ تا ۹۹ باشد."
		row.CSRFToken = s.CSRFToken(r)
		s.renderResultPartial(w, "result_row", http.StatusUnprocessableEntity, row)
		return
	}

	// Repairs: SetResult accepts SCHEDULED matches only (frozen contract), so a
	// finished row is cleared first — the clear-then-set sequence the store
	// supports. If the set then fails the match stays scheduled; the row is
	// returned in that honest state instead of pretending the edit landed.
	if m.Status == matchStatusFinished {
		if err := s.store.ClearResult(id); err != nil {
			row := resultRowWithValues(*m, home, away)
			row.ErrorMessage = err.Error()
			row.CSRFToken = s.CSRFToken(r)
			s.renderResultPartial(w, "result_row", http.StatusUnprocessableEntity, row)
			return
		}
	}
	if err := s.store.SetResult(id, home, away); err != nil {
		row := resultRowWithValues(*m, home, away)
		row.ErrorMessage = err.Error()
		row.CSRFToken = s.CSRFToken(r)
		s.renderResultPartial(w, "result_row", http.StatusUnprocessableEntity, row)
		return
	}

	updated, err := s.store.Match(id)
	if err != nil || updated == nil {
		updated = m
		updated.Status = matchStatusFinished
		updated.HomeScore = &home
		updated.AwayScore = &away
	}
	row := newResultRow(*updated)
	row.Saved = true
	row.CSRFToken = s.CSRFToken(r)
	w.Header().Set("HX-Trigger", resultSavedEvent)
	s.renderResultPartial(w, "result_row", http.StatusOK, row)
}

func (s *Server) handleAdminResultClear(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقه نامعتبر است.")
		return
	}
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, dataErrorMessage)
		return
	}
	m, err := s.store.Match(id)
	if errors.Is(err, store.ErrNotFound) {
		writePersianError(w, http.StatusNotFound, "مسابقه پیدا نشد.")
		return
	}
	if err != nil {
		s.log.Error("result clear: load match", "error", err, "match", id)
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	if err := s.store.ClearResult(id); err != nil {
		row := newResultRow(*m)
		row.ErrorMessage = err.Error()
		row.CSRFToken = s.CSRFToken(r)
		s.renderResultPartial(w, "result_row", http.StatusUnprocessableEntity, row)
		return
	}
	updated, err := s.store.Match(id)
	if err != nil || updated == nil {
		updated = m
		updated.Status = matchStatusScheduled
		updated.HomeScore = nil
		updated.AwayScore = nil
	}
	row := newResultRow(*updated)
	row.CSRFToken = s.CSRFToken(r)
	w.Header().Set("HX-Trigger", resultSavedEvent)
	s.renderResultPartial(w, "result_row", http.StatusOK, row)
}

// ---------- standings partial ----------

func (s *Server) handleAdminResultsStandings(w http.ResponseWriter, r *http.Request) {
	compID, ok := parsePositiveInt(r.URL.Query().Get("competition"))
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقه نامعتبر است.")
		return
	}
	table, err := s.computeStandings(int64(compID))
	if err != nil {
		s.log.Error("results standings", "error", err, "competition", compID)
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	s.renderResultPartial(w, "standings_table", http.StatusOK, table)
}

// computeStandings rebuilds the table from registrations + finished matches via
// the single ranking authority (internal/standing); it never re-implements the
// ordering rule.
func (s *Server) computeStandings(competitionID int64) (standingsView, error) {
	var out standingsView
	if comp, err := s.store.Competition(competitionID); err == nil && comp != nil {
		out.CompetitionName = comp.DisplayName
	}
	regs, err := s.store.Registrations(competitionID)
	if err != nil {
		return out, err
	}
	teams := make([]standing.TeamInput, 0, len(regs))
	for _, reg := range regs {
		teams = append(teams, standing.TeamInput{ID: reg.TeamID, Name: reg.TeamName})
	}
	finished, err := s.store.FinishedMatches(competitionID)
	if err != nil {
		return out, err
	}
	inputs := make([]standing.MatchInput, 0, len(finished))
	for _, f := range finished {
		inputs = append(inputs, standing.MatchInput{
			HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID,
			HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals,
		})
	}
	out.Rows = standingRows(standing.Compute(teams, inputs))
	return out, nil
}

// ---------- row + parsing helpers ----------

// newResultRow maps a stored match onto the row partial's payload.
func newResultRow(m store.Match) resultRowView {
	row := resultRowView{Match: m, Finished: m.Status == matchStatusFinished}
	if m.HomeScore != nil {
		row.HomeValue = strconv.Itoa(*m.HomeScore)
	}
	if m.AwayScore != nil {
		row.AwayValue = strconv.Itoa(*m.AwayScore)
	}
	return row
}

// resultRowWithValues keeps the admin's typed input visible on a 422.
func resultRowWithValues(m store.Match, home, away int) resultRowView {
	row := newResultRow(m)
	row.HomeValue = strconv.Itoa(home)
	row.AwayValue = strconv.Itoa(away)
	return row
}

func statusPtr(s string) *string { return &s }

// distinctWeeks returns the sorted set of week numbers present in matches.
func distinctWeeks(matches []store.Match) []int {
	set := map[int]bool{}
	for _, m := range matches {
		if m.Week != nil {
			set[*m.Week] = true
		}
	}
	weeks := make([]int, 0, len(set))
	for w := range set {
		weeks = append(weeks, w)
	}
	sort.Ints(weeks)
	return weeks
}

// firstScheduledWeek is the lowest week that still has a scheduled match.
func firstScheduledWeek(matches []store.Match) int {
	best := 0
	for _, m := range matches {
		if m.Status != matchStatusScheduled || m.Week == nil {
			continue
		}
		if best == 0 || *m.Week < best {
			best = *m.Week
		}
	}
	return best
}

// normalizeDigits maps Persian (۰-۹) and Arabic-Indic (٠-٩) digits to Latin.
// It reports false on any other character so callers can reject the input.
func normalizeDigits(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹': // U+06F0..U+06F9
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩': // U+0660..U+0669
			b.WriteRune('0' + (r - '٠'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	return b.String(), true
}

// parsePositiveInt parses a path/query id, tolerating Persian digits.
func parsePositiveInt(raw string) (int, bool) {
	digits, ok := normalizeDigits(raw)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parseScore parses one team's goals: empty/invalid/out-of-range → false.
func parseScore(raw string) (int, bool) {
	digits, ok := normalizeDigits(raw)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 || n > maxScore {
		return 0, false
	}
	return n, true
}
