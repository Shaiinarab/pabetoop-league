// Competitions + registrations admin UI (TASK-015) — the missing link in the
// season loop (PROJECT_SPEC §4/§5; §11 resolves the brief's old §-references).
//
// Scope note (frozen contract): the store exposes CreateCompetition, Competition,
// Competitions, Register, Unregister, Registrations, RegisteredTeamIDs and
// MatchCount — and NOTHING to rename or delete a competition. Edit/delete are
// therefore not implemented here: the only place SQL may live is internal/store,
// and inventing writes in a handler would break DATABASE.md's discipline. Both
// are reported as contract gaps (NAG) rather than faked.
//
// Conventions are copied from TASK-010's admin_handlers.go: an own registrar
// wrapping every route in requireAdmin, flash cookies for redirect outcomes,
// per-field Persian validation errors on 422 (htmx 4 swaps 4xx bodies — D2), and
// row-level htmx swaps via hx-target="closest tr".
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

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// Persian level labels used by the competitions table.
const (
	levelPremierLabel = "لیگ برتر"
	levelLeague1Label = "لیگ ۱"
)

// RegisterCompetitionRoutes mounts the competitions + registrations surfaces.
// Every handler is wrapped in requireAdmin here (the TASK-013/014 pattern);
// wiring this into Handler() is the Lead's one-line integration.
func (s *Server) RegisterCompetitionRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/competitions", s.requireAdmin(http.HandlerFunc(s.handleCompetitionsList)))
	mux.Handle("GET /admin/competitions/new", s.requireAdmin(http.HandlerFunc(s.handleCompetitionsList)))
	mux.Handle("POST /admin/competitions", s.requireAdmin(http.HandlerFunc(s.handleCompetitionCreate)))
	mux.Handle("GET /admin/competitions/{id}/registrations", s.requireAdmin(http.HandlerFunc(s.handleRegistrations)))
	mux.Handle("POST /admin/competitions/{id}/register", s.requireAdmin(http.HandlerFunc(s.handleRegister)))
	mux.Handle("POST /admin/competitions/{id}/unregister", s.requireAdmin(http.HandlerFunc(s.handleUnregister)))
	mux.Handle("POST /admin/competitions/{id}/edit", s.requireAdmin(http.HandlerFunc(s.handleCompetitionEdit)))
	mux.Handle("POST /admin/competitions/{id}/delete", s.requireAdmin(http.HandlerFunc(s.handleCompetitionDelete)))
}

// ---------- view shapes ----------

type competitionRowView struct {
	ID            int64
	DisplayName   string
	LevelLabel    string
	GroupName     string // display form — empty means "no group" (premier)
	GroupInput    string // raw value for the edit input; always empty for premier
	IsLeague1     bool   // the edit form shows the group input only for league1
	Registrations int
	Matches       int
	Finished      int
}

type competitionGroupView struct {
	DisplayName string
	Premier     []competitionRowView
	League1     []competitionRowView
}

// competitionsView carries BOTH the grouped list and the create form (the form
// lives on the same page so a 422 can re-render it with per-field errors, and so
// the allowlist needs a single competitions.html).
type competitionsView struct {
	adminPage
	SeasonName string
	HasSeason  bool
	Groups     []competitionGroupView

	Seasons   []store.Season
	AgeGroups []ageOption

	FormSeasonID    int64
	FormAgeGroupID  int64
	FormLevel       string
	FormGroupName   string
	FormDisplayName string
	FieldErrors     map[string]string
	FormError       string
}

type registrationRowView struct {
	RegistrationID int64
	CompetitionID  int64
	TeamName       string
	ClubName       string
	CSRFToken      string
}

type registrationsView struct {
	adminPage
	Competition store.Competition
	Rows        []registrationRowView
	Eligible    []teamOption
}

// ---------- template set ----------

// Own page set (render.go's adminPageFiles is TASK-010's lane): admin_base +
// partials + the single page file, cached per templates dir. competitions.html
// renders with block overrides in a clone, exactly like renderAdmin does.
var (
	compTmplMu    sync.Mutex
	compTmplCache = map[string]map[string]*template.Template{}
	compTmplErr   = map[string]error{}
)

var competitionPageFiles = map[string]string{
	"competitions":  "admin/competitions.html",
	"registrations": "admin/registrations.html",
}

func competitionPageSet(dir string) (map[string]*template.Template, error) {
	compTmplMu.Lock()
	defer compTmplMu.Unlock()

	if pages, ok := compTmplCache[dir]; ok {
		return pages, nil
	}
	if err := compTmplErr[dir]; err != nil {
		return nil, err
	}
	fsys := os.DirFS(dir)
	funcs := templateFuncMap(nil)

	base, err := template.New("compbase").Funcs(funcs).ParseFS(fsys, "admin_base.html", "partials/*.html")
	if err != nil {
		compTmplErr[dir] = err
		return nil, err
	}
	pages := map[string]*template.Template{}
	for name, file := range competitionPageFiles {
		clone, err := base.Clone()
		if err != nil {
			compTmplErr[dir] = err
			return nil, err
		}
		parsed, err := clone.ParseFS(fsys, file)
		if err != nil {
			compTmplErr[dir] = err
			return nil, err
		}
		pages[name] = parsed
	}
	compTmplCache[dir] = pages
	return pages, nil
}

func (s *Server) renderCompetitionPage(w http.ResponseWriter, page string, data any, status int) {
	if status == 0 {
		status = http.StatusOK
	}
	pages, err := competitionPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("competition templates unavailable", "error", err, "page", page)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	tmpl, ok := pages[page]
	if !ok {
		s.log.Error("unknown competition page", "page", page)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "admin_base.html", data); err != nil {
		s.log.Error("render competition page failed", "page", page, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) renderCompetitionPartial(w http.ResponseWriter, name string, status int, data any) {
	pages, err := competitionPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("competition templates unavailable", "error", err, "partial", name)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	tmpl := pages["registrations"]
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render competition partial failed", "partial", name, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// ---------- competitions ----------

func (s *Server) handleCompetitionsList(w http.ResponseWriter, r *http.Request) {
	view := competitionsView{adminPage: s.adminChrome(w, r, "competitions"), FormLevel: levelPremier}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "مسابقات"}}
	if s.store == nil {
		s.renderCompetitionPage(w, "competitions", view, http.StatusOK)
		return
	}

	seasons, err := s.store.Seasons()
	if err != nil {
		s.log.Error("competitions: seasons", "error", err)
	}
	view.Seasons = seasons
	ages, err := s.store.AgeGroups()
	if err != nil {
		s.log.Error("competitions: age groups", "error", err)
	}
	ageNames := map[int64]string{}
	for _, a := range ages {
		ageNames[a.ID] = a.DisplayName
		view.AgeGroups = append(view.AgeGroups, ageOption{ID: a.ID, DisplayName: a.DisplayName})
	}

	season, err := s.store.ActiveSeason()
	if err != nil {
		s.log.Error("competitions: active season", "error", err)
	}
	if season != nil {
		view.HasSeason = true
		view.SeasonName = season.Name
		view.FormSeasonID = season.ID
		comps, err := s.store.Competitions(&season.ID, nil)
		if err != nil {
			s.log.Error("competitions: list", "error", err)
		}
		byAge := map[int64]*competitionGroupView{}
		order := []int64{}
		for _, c := range comps {
			g, ok := byAge[c.AgeGroupID]
			if !ok {
				g = &competitionGroupView{DisplayName: ageNames[c.AgeGroupID]}
				byAge[c.AgeGroupID] = g
				order = append(order, c.AgeGroupID)
			}
			row := s.competitionRowViewFor(c)
			if c.Level == levelPremier {
				g.Premier = append(g.Premier, row)
			} else {
				g.League1 = append(g.League1, row)
			}
		}
		for _, id := range order {
			g := byAge[id]
			sort.SliceStable(g.League1, func(i, j int) bool { return g.League1[i].GroupName < g.League1[j].GroupName })
			view.Groups = append(view.Groups, *g)
		}
	}
	s.renderCompetitionPage(w, "competitions", view, http.StatusOK)
}

func (s *Server) handleCompetitionCreate(w http.ResponseWriter, r *http.Request) {
	view := competitionsView{adminPage: s.adminChrome(w, r, "competitions")}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "مسابقات"}}
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, dataErrorMessage)
		return
	}

	seasonID, _ := parsePositiveInt(r.PostFormValue("season"))
	ageID, _ := parsePositiveInt(r.PostFormValue("age_group"))
	level := strings.TrimSpace(r.PostFormValue("level"))
	groupName := strings.TrimSpace(r.PostFormValue("group_name"))
	displayName := strings.TrimSpace(r.PostFormValue("display_name"))

	view.FormSeasonID, view.FormAgeGroupID = int64(seasonID), int64(ageID)
	view.FormLevel, view.FormGroupName, view.FormDisplayName = level, groupName, displayName
	if view.FormLevel == "" {
		view.FormLevel = levelPremier
	}
	if seasons, err := s.store.Seasons(); err == nil {
		view.Seasons = seasons
	}
	if ages, err := s.store.AgeGroups(); err == nil {
		for _, a := range ages {
			view.AgeGroups = append(view.AgeGroups, ageOption{ID: a.ID, DisplayName: a.DisplayName})
		}
	}

	errs := map[string]string{}
	if seasonID <= 0 {
		errs["season"] = "انتخاب فصل الزامی است."
	}
	if ageID <= 0 {
		errs["age_group"] = "انتخاب ردهٔ سنی الزامی است."
	}
	switch level {
	case levelPremier, levelLeague1:
	default:
		errs["level"] = "سطح مسابقات الزامی است."
	}
	if displayName == "" {
		errs["display_name"] = "نام مسابقات الزامی است."
	}
	if level == levelLeague1 && groupName == "" {
		errs["group_name"] = "برای لیگ یک، نام گروه الزامی است (مثلاً «A»)."
	}
	if len(errs) > 0 {
		view.FieldErrors = errs
		s.renderCompetitionPage(w, "competitions", view, http.StatusUnprocessableEntity)
		return
	}

	var group *string
	if level == levelLeague1 {
		group = &groupName
	}
	if _, err := s.store.CreateCompetition(int64(seasonID), int64(ageID), level, group, displayName); err != nil {
		// The store's Persian message is surfaced verbatim (never re-worded).
		msg := err.Error()
		switch {
		case errors.Is(err, store.ErrBadGroup):
			errs["group_name"] = msg
		case errors.Is(err, store.ErrBadLevel):
			errs["level"] = msg
		default:
			view.FormError = msg
		}
		view.FieldErrors = errs
		s.renderCompetitionPage(w, "competitions", view, http.StatusUnprocessableEntity)
		return
	}
	setFlash(w, r, flashSuccess, "مسابقات ثبت شد.")
	http.Redirect(w, r, "/admin/competitions", http.StatusSeeOther)
}

// ---------- registrations ----------

func (s *Server) handleRegistrations(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	comp, err := s.store.Competition(id)
	if err != nil || comp == nil {
		writePersianError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	view, err := s.registrationsView(w, r, *comp)
	if err != nil {
		s.log.Error("registrations: load", "error", err, "competition", id)
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	s.renderCompetitionPage(w, "registrations", view, http.StatusOK)
}

// registrationsView builds the page/partial payload for one competition.
func (s *Server) registrationsView(w http.ResponseWriter, r *http.Request, comp store.Competition) (registrationsView, error) {
	view := registrationsView{
		adminPage:   s.adminChrome(w, r, "competitions"),
		Competition: comp,
	}
	view.Breadcrumb = []breadcrumbItem{
		{Label: "خانه", Href: "/admin"},
		{Label: "مسابقات", Href: "/admin/competitions"},
		{Label: comp.DisplayName},
	}

	regs, err := s.store.Registrations(comp.ID)
	if err != nil {
		return view, err
	}
	for _, reg := range regs {
		view.Rows = append(view.Rows, registrationRowView{
			RegistrationID: reg.ID,
			CompetitionID:  reg.CompetitionID,
			TeamName:       reg.TeamName,
			ClubName:       reg.ClubName,
			CSRFToken:      view.CSRFToken,
		})
	}
	registered, err := s.store.RegisteredTeamIDs(comp.ID)
	if err != nil {
		return view, err
	}
	teams, err := s.store.Teams(nil)
	if err != nil {
		return view, err
	}
	for _, t := range teams {
		if t.IsActive && !registered[t.ID] {
			view.Eligible = append(view.Eligible, teamOption{ID: t.ID, Name: t.DisplayName})
		}
	}
	return view, nil
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقات نامعتبر است.")
		return
	}
	teamID, ok := parsePositiveInt(r.PostFormValue("team_id"))
	if !ok {
		s.registrationOutcome(w, r, id, "انتخاب تیم الزامی است.")
		return
	}
	if _, err := s.store.Register(id, int64(teamID)); err != nil {
		s.registrationOutcome(w, r, id, err.Error())
		return
	}
	if !hasHTMX(r) {
		setFlash(w, r, flashSuccess, "تیم ثبت‌نام شد.")
		http.Redirect(w, r, registrationsPath(id), http.StatusSeeOther)
		return
	}
	// htmx: refresh the registered-teams table in place.
	comp, err := s.store.Competition(id)
	if err != nil || comp == nil {
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	view, err := s.registrationsView(w, r, *comp)
	if err != nil {
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	s.renderCompetitionPartial(w, "registered_teams", http.StatusOK, view)
}

// registrationOutcome answers a refused register/unregister: an htmx 422 with a
// Persian message into #register-errors (D2 hx-status routing), or a flash +
// redirect for the no-JS path.
func (s *Server) registrationOutcome(w http.ResponseWriter, r *http.Request, compID int64, message string) {
	if hasHTMX(r) {
		s.renderCompetitionPartial(w, "register_error", http.StatusUnprocessableEntity,
			map[string]string{"Message": message})
		return
	}
	setFlash(w, r, flashError, message)
	http.Redirect(w, r, registrationsPath(compID), http.StatusSeeOther)
}

func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	compID, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقات نامعتبر است.")
		return
	}
	regID, ok := parsePositiveInt(r.PostFormValue("registration_id"))
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ ثبت‌نام نامعتبر است.")
		return
	}
	if err := s.store.Unregister(int64(regID)); err != nil {
		// 400 keeps the row intact: the unregister form routes 400 bodies to
		// #register-errors via hx-status:400 (see the template).
		if hasHTMX(r) {
			s.renderCompetitionPartial(w, "register_error", http.StatusBadRequest,
				map[string]string{"Message": err.Error()})
			return
		}
		setFlash(w, r, flashError, err.Error())
		http.Redirect(w, r, registrationsPath(compID), http.StatusSeeOther)
		return
	}
	if hasHTMX(r) {
		w.WriteHeader(http.StatusOK) // empty body: the row disappears
		return
	}
	setFlash(w, r, flashSuccess, "ثبت‌نام حذف شد.")
	http.Redirect(w, r, registrationsPath(compID), http.StatusSeeOther)
}

// ---------- edit / delete ----------

// handleCompetitionEdit renames a competition and/or changes its League 1 group
// label. Official names are authority-controlled (D7) — the store trims
// whitespace only and its Persian message is surfaced verbatim. Success answers
// the refreshed row; a refusal answers 400 with the row + message, the
// club-rename pattern (D2: htmx 4 swaps 4xx bodies, so no redirect is needed).
func (s *Server) handleCompetitionEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقات نامعتبر است.")
		return
	}
	comp, err := s.store.Competition(id)
	if err != nil || comp == nil {
		s.renderCompetitionPartial(w, "competition_row", http.StatusBadRequest,
			competitionRowView{ID: id, DisplayName: "مسابقات یافت نشد"})
		return
	}
	// Premier carries no group by schema; only forward one for league1 so the
	// store never has to guess whether a missing value means "clear" or "keep".
	var group *string
	if comp.Level == levelLeague1 {
		g := strings.TrimSpace(r.PostFormValue("group_name"))
		group = &g
	}
	if err := s.store.RenameCompetition(id, strings.TrimSpace(r.PostFormValue("display_name")), group); err != nil {
		s.competitionRowReject(w, id, err)
		return
	}
	s.renderCompetitionRow(w, id)
}

// handleCompetitionDelete: success → empty body (htmx removes the row);
// blocked → 400 with the row + the store's Persian refusal.
func (s *Server) handleCompetitionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ مسابقات نامعتبر است.")
		return
	}
	if err := s.store.DeleteCompetition(id); err != nil {
		s.competitionRowReject(w, id, err)
		return
	}
	w.WriteHeader(http.StatusOK) // empty: the row disappears
}

// competitionRowError pairs the row with a Persian message for htmx error
// swaps (mirrors admin_handlers.go's clubRowError).
type competitionRowError struct {
	Competition competitionRowView
	Message     string
}

// competitionRowViewFor builds one row from a competition, including the match
// counters the table shows. Shared by the list and the post-edit row swap so the
// two can never disagree.
func (s *Server) competitionRowViewFor(c store.Competition) competitionRowView {
	row := competitionRowView{
		ID:            c.ID,
		DisplayName:   c.DisplayName,
		GroupName:     groupKey(c),
		Registrations: c.Registrations,
		IsLeague1:     c.Level == levelLeague1,
	}
	if c.GroupName != nil {
		row.GroupInput = strings.TrimSpace(*c.GroupName)
	}
	if total, finished, err := s.store.MatchCount(c.ID); err == nil {
		row.Matches, row.Finished = total, finished
	}
	if c.Level == levelPremier {
		row.LevelLabel = levelPremierLabel
	} else {
		row.LevelLabel = levelLeague1Label
	}
	return row
}

// competitionRow loads the current row for a competition id.
func (s *Server) competitionRow(id int64) (competitionRowView, error) {
	c, err := s.store.Competition(id)
	if err != nil || c == nil {
		return competitionRowView{ID: id}, err
	}
	return s.competitionRowViewFor(*c), nil
}

// renderCompetitionRow answers a successful edit/unregister-style mutation with
// the refreshed row (hx-target="closest tr", outerHTML).
func (s *Server) renderCompetitionRow(w http.ResponseWriter, id int64) {
	row, err := s.competitionRow(id)
	if err != nil {
		s.log.Error("competition row reload failed", "id", id, "error", err)
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	s.renderCompetitionPartial(w, "competition_row", http.StatusOK, row)
}

// competitionRowReject answers a refused edit/delete with the row plus the
// store's verbatim Persian message, so the operator sees why and can act.
func (s *Server) competitionRowReject(w http.ResponseWriter, id int64, cause error) {
	s.log.Warn("competition mutation refused", "id", id, "error", cause)
	row, err := s.competitionRow(id)
	if err != nil {
		row = competitionRowView{ID: id}
	}
	s.renderCompetitionPartial(w, "competition_row_error", http.StatusBadRequest,
		competitionRowError{Competition: row, Message: cause.Error()})
}

// ---------- helpers ----------

func registrationsPath(compID int64) string {
	return "/admin/competitions/" + strconv.FormatInt(compID, 10) + "/registrations"
}

// hasHTMX reports whether the request came from htmx (bodies of 4xx swaps).
func hasHTMX(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("HX-Request"), "true")
}
