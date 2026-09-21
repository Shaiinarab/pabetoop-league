// Public site handlers (TASK-013): anonymous, read-only browsing over the
// TASK-004 store and the TASK-005 templates. No accounts, no personalization.
//
// Wiring contract: Handler() in server.go mounts these routes. Replacing the
// foundation's placeholder registrations with a single call to
// RegisterPublicRoutes(mux) completes the swap — and must happen in the same
// edit, because registering both sets panics: http.ServeMux rejects duplicate
// patterns. A route defined but never mounted is invisible to build and vet,
// which is why ROUTES.md is asserted against the routing tree by a test.
//
// Nil store: the foundation's own tests build a Server with a nil store and
// expect the public pages to render (empty states, no data access). These
// handlers keep that behaviour: with no store they render the same pages with
// empty data instead of failing. Id validation needs the store, so with a nil
// store the pages are rendered as empty shells rather than 404s.
package web

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/shaiinarab/pabetoop-league/internal/standing"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// notFoundMessage is the Persian 404 text required by the brief.
const notFoundMessage = "صفحه‌ای که خواستید پیدا نشد."

// dataErrorMessage is the Persian 500 used when the store fails mid-page.
const dataErrorMessage = "خطایی در دریافت داده‌ها رخ داد؛ لطفاً بعداً تلاش کنید."

// latestLimit caps the home page's latest-results / upcoming-fixtures lists.
const latestLimit = 10

// Competition level values (store contract, api.go).
const (
	levelPremier = "premier"
	levelLeague1 = "league1"
)

// Tab values accepted by ?tab= / the {tab} path segment.
const (
	tabTable    = "table"
	tabResults  = "results"
	tabFixtures = "fixtures"
)

// RegisterPublicRoutes mounts the public pages on mux. It is the only place the
// public routes are registered — see the wiring contract above.
func (s *Server) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handlePublicHome)
	mux.HandleFunc("GET /age/{id}", s.handlePublicAgeGroup)
	mux.HandleFunc("GET /competition/{id}", s.handlePublicCompetition)
	// The TASK-005 competition template links to /competition/{id}/{tab}
	// (path form) while the brief specifies ?tab=... — both are accepted.
	mux.HandleFunc("GET /competition/{id}/{tab}", s.handlePublicCompetition)
}

// ---------- home ----------

// handlePublicHome renders home.html: active season, season stats, the latest
// results across every competition of the active season, the next fixtures, and
// the age-group cards.
func (s *Server) handlePublicHome(w http.ResponseWriter, _ *http.Request) {
	setDynamicCache(w)
	v := homeView{viewBase: s.publicBase(true)}
	if s.store == nil {
		s.renderPage(w, "home", v)
		return
	}

	comps, err := s.activeSeasonCompetitions()
	if err != nil {
		s.renderDataError(w, "home", err)
		return
	}
	if clubs, err := s.store.Clubs(false); err == nil {
		v.Stats.Clubs = len(clubs)
	}

	ctx := s.newRowContext(comps)
	var results, upcoming []store.Match
	for _, comp := range comps {
		matches, err := s.store.Matches(comp.ID, nil, nil)
		if err != nil {
			s.renderDataError(w, "home", err)
			return
		}
		for _, m := range matches {
			v.Stats.Matches++
			switch m.Status {
			case "finished":
				v.Stats.Finished++
				if m.HomeScore != nil {
					v.Stats.Goals += *m.HomeScore
				}
				if m.AwayScore != nil {
					v.Stats.Goals += *m.AwayScore
				}
				results = append(results, m)
			case "scheduled":
				upcoming = append(upcoming, m)
			}
		}
	}

	sortResultsNewestFirst(results)
	sortFixturesOldestFirst(upcoming)
	if len(results) > latestLimit {
		results = results[:latestLimit]
	}
	if len(upcoming) > latestLimit {
		upcoming = upcoming[:latestLimit]
	}
	// Home lists mix competitions, so team names are qualified when ambiguous.
	v.LatestResults = s.rowsFor(ctx, results, true)
	v.Upcoming = s.rowsFor(ctx, upcoming, true)
	s.renderPage(w, "home", v)
}

// activeSeasonCompetitions returns the active season's competitions. No active
// season is a valid state (store contract) and yields an empty list.
func (s *Server) activeSeasonCompetitions() ([]store.Competition, error) {
	season, err := s.store.ActiveSeason()
	if err != nil {
		return nil, err
	}
	if season == nil {
		return []store.Competition{}, nil
	}
	return s.store.Competitions(&season.ID, nil)
}

// ---------- age group ----------

// handlePublicAgeGroup renders age_group.html: the premier competition and the
// League 1 group cards of one age group in the ACTIVE season.
func (s *Server) handlePublicAgeGroup(w http.ResponseWriter, r *http.Request) {
	setDynamicCache(w)
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	v := ageGroupView{viewBase: s.publicBase(false)}
	if s.store == nil {
		v.AgeGroup = store.AgeGroup{ID: id}
		s.renderPage(w, "age_group", v)
		return
	}

	groups, err := s.store.AgeGroups()
	if err != nil {
		s.renderDataError(w, "age_group", err)
		return
	}
	var found *store.AgeGroup
	for i := range groups {
		if groups[i].ID == id {
			found = &groups[i]
			break
		}
	}
	if found == nil {
		writePersianError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	v.AgeGroup = *found

	season, err := s.store.ActiveSeason()
	if err != nil {
		s.renderDataError(w, "age_group", err)
		return
	}
	if season != nil {
		comps, err := s.store.Competitions(&season.ID, &found.ID)
		if err != nil {
			s.renderDataError(w, "age_group", err)
			return
		}
		for _, comp := range comps {
			switch comp.Level {
			case levelPremier:
				if v.Premier == nil {
					c := comp
					v.Premier = &c
				}
			case levelLeague1:
				v.League1 = append(v.League1, comp)
			}
		}
		sort.SliceStable(v.League1, func(i, j int) bool {
			return groupKey(v.League1[i]) < groupKey(v.League1[j])
		})
	}
	s.renderPage(w, "age_group", v)
}

// ---------- competition ----------

// handlePublicCompetition renders competition.html. Page order is the spec's:
// identity → standings → results → fixtures, with the tab selecting the visible
// section. Standings are always recomputed from finished matches (never stored).
func (s *Server) handlePublicCompetition(w http.ResponseWriter, r *http.Request) {
	setDynamicCache(w)
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	v := competitionView{viewBase: s.publicBase(false), Tab: tabTable}
	if s.store == nil {
		s.renderPage(w, "competition", v)
		return
	}

	comp, err := s.store.Competition(id)
	if errors.Is(err, store.ErrNotFound) {
		writePersianError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	if err != nil {
		s.renderDataError(w, "competition", err)
		return
	}
	v.Comp = *comp
	v.Tab = requestedTab(r)
	// The page can show a competition outside the active season, so the season
	// pill follows the competition's own season.
	v.ActiveSeason = comp.SeasonName

	matches, err := s.store.Matches(comp.ID, nil, nil)
	if err != nil {
		s.renderDataError(w, "competition", err)
		return
	}
	var results, fixtures []store.Match
	for _, m := range matches {
		switch m.Status {
		case "finished":
			results = append(results, m)
		case "scheduled":
			fixtures = append(fixtures, m)
		}
	}
	sortResultsNewestFirst(results)
	sortFixturesOldestFirst(fixtures)
	ctx := s.newRowContext([]store.Competition{*comp})
	v.Results = s.rowsFor(ctx, results, true)
	v.Fixtures = s.rowsFor(ctx, fixtures, true)

	regs, err := s.store.Registrations(comp.ID)
	if err != nil {
		s.renderDataError(w, "competition", err)
		return
	}
	teams := make([]standing.TeamInput, 0, len(regs))
	for _, reg := range regs {
		teams = append(teams, standing.TeamInput{ID: reg.TeamID, Name: reg.TeamName})
	}
	finished, err := s.store.FinishedMatches(comp.ID)
	if err != nil {
		s.renderDataError(w, "competition", err)
		return
	}
	inputs := make([]standing.MatchInput, 0, len(finished))
	for _, f := range finished {
		inputs = append(inputs, standing.MatchInput{
			HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID,
			HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals,
		})
	}
	v.Table = standingsView{Rows: standingRows(standing.Compute(teams, inputs))}
	s.renderPage(w, "competition", v)
}

// requestedTab resolves the active section: the {tab} path segment wins over
// ?tab=, anything unknown falls back to the table.
func requestedTab(r *http.Request) string {
	tab := strings.ToLower(strings.TrimSpace(r.PathValue("tab")))
	if tab == "" {
		tab = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	}
	switch tab {
	case tabResults, tabFixtures:
		return tab
	default:
		return tabTable
	}
}

// ---------- shared helpers ----------

// publicBase builds the chrome every public page carries (templates' data
// contract). Store errors degrade to empty chrome: navigation is never worth a
// failed page.
func (s *Server) publicBase(isHome bool) viewBase {
	v := viewBase{SiteName: SiteName, SiteSubtitle: SiteSubtitle, AgeGroups: []ageGroupNav{}, IsHome: isHome}
	if s.store == nil {
		return v
	}
	if season, err := s.store.ActiveSeason(); err == nil && season != nil {
		v.ActiveSeason = season.Name
	}
	if groups, err := s.store.AgeGroups(); err == nil {
		for _, g := range groups {
			v.AgeGroups = append(v.AgeGroups, ageGroupNav{ID: g.ID, DisplayName: g.DisplayName})
		}
	}
	return v
}

// setDynamicCache marks a page as live data (nothing is written to disk, the
// templates re-render on every request).
func setDynamicCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
}

// renderDataError logs the failure and renders the Persian 500 page.
func (s *Server) renderDataError(w http.ResponseWriter, page string, err error) {
	s.log.Error("public page data failed", "page", page, "error", err)
	writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
}

// groupKey is the sort key for League 1 group cards.
func groupKey(c store.Competition) string {
	if c.GroupName == nil {
		return ""
	}
	return strings.TrimSpace(*c.GroupName)
}

// standingRows maps engine rows onto the standings partial's contract.
// []emptyRow is TASK-008's placeholder shape (server.go, outside this
// allowlist); its fields are exactly standing.Row's minus TeamID.
func standingRows(rows []standing.Row) []emptyRow {
	out := make([]emptyRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, emptyRow{
			Rank:         row.Rank,
			TeamName:     row.TeamName,
			Played:       row.Played,
			Won:          row.Won,
			Drawn:        row.Drawn,
			Lost:         row.Lost,
			GoalsFor:     row.GoalsFor,
			GoalsAgainst: row.GoalsAgainst,
			GoalDiff:     row.GoalDiff,
			Points:       row.Points,
		})
	}
	return out
}

// ---------- ordering ----------

func matchWeek(m store.Match) int {
	if m.Week == nil {
		return 0
	}
	return *m.Week
}

func matchDate(m store.Match) string {
	if m.ScheduledDate == nil {
		return ""
	}
	return *m.ScheduledDate
}

// sortResultsNewestFirst orders finished matches: newest week, then newest date
// (undated last), then highest id. Stable, so the store's order breaks ties.
func sortResultsNewestFirst(ms []store.Match) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if aw, bw := matchWeek(a), matchWeek(b); aw != bw {
			return aw > bw
		}
		ad, bd := matchDate(a), matchDate(b)
		if ad != bd {
			if ad == "" {
				return false
			}
			if bd == "" {
				return true
			}
			return ad > bd
		}
		return a.ID > b.ID
	})
}

// sortFixturesOldestFirst orders scheduled matches: earliest week, then earliest
// date; missing week/date values sort last rather than first.
func sortFixturesOldestFirst(ms []store.Match) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		aw, bw := matchWeek(a), matchWeek(b)
		if (aw == 0) != (bw == 0) {
			return bw == 0 // unknown week sorts after known weeks
		}
		if aw != bw {
			return aw < bw
		}
		ad, bd := matchDate(a), matchDate(b)
		if (ad == "") != (bd == "") {
			return bd == "" // unknown date sorts after known dates
		}
		if ad != bd {
			return ad < bd
		}
		return a.ID < b.ID
	})
}

// ---------- cross-competition identity (spec §27 as quoted in the brief) ----------

// rowContext resolves the full team identity rule: when a rendered list mixes
// competitions, a team name that occurs in more than one of them is qualified
// with its age group and, for League 1, its group label. Unambiguous names are
// left exactly as stored (official names are never rewritten — D7).
type rowContext struct {
	competitions map[int64]store.Competition
	ageLabels    map[int64]string
}

func (s *Server) newRowContext(comps []store.Competition) *rowContext {
	ctx := &rowContext{
		competitions: make(map[int64]store.Competition, len(comps)),
		ageLabels:    map[int64]string{},
	}
	for _, c := range comps {
		ctx.competitions[c.ID] = c
	}
	if s.store != nil {
		if groups, err := s.store.AgeGroups(); err == nil {
			for _, g := range groups {
				ctx.ageLabels[g.ID] = g.DisplayName
			}
		}
	}
	return ctx
}

// qualify returns the name to render for one team in a list.
func (ctx *rowContext) qualify(name string, competitionID int64, seen map[string]map[int64]bool) string {
	if len(seen[name]) <= 1 {
		return name
	}
	comp, ok := ctx.competitions[competitionID]
	if !ok {
		return name
	}
	label := name
	if age := ctx.ageLabels[comp.AgeGroupID]; age != "" {
		label += " — " + age
	}
	if group := groupKey(comp); group != "" {
		label += " گروه " + group
	}
	return label
}

// rowsFor wraps matches for the match_row partial. Ambiguity is judged over the
// list actually rendered, so short lists stay unqualified whenever they can be,
// and longer ones disambiguate exactly the names that need it.
func (s *Server) rowsFor(ctx *rowContext, matches []store.Match, showDate bool) []matchRowView {
	seen := make(map[string]map[int64]bool, 2*len(matches))
	note := func(name string, competitionID int64) {
		set, ok := seen[name]
		if !ok {
			set = map[int64]bool{}
			seen[name] = set
		}
		set[competitionID] = true
	}
	for _, m := range matches {
		note(m.HomeTeamName, m.CompetitionID)
		note(m.AwayTeamName, m.CompetitionID)
	}
	out := make([]matchRowView, 0, len(matches))
	for _, m := range matches {
		m.HomeTeamName = ctx.qualify(m.HomeTeamName, m.CompetitionID, seen)
		m.AwayTeamName = ctx.qualify(m.AwayTeamName, m.CompetitionID, seen)
		out = append(out, matchRowView{Match: m, ShowDate: showDate})
	}
	return out
}
