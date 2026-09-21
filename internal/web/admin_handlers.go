package web

import (
	"errors"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// RegisterAdminRoutes mounts the admin area onto mux. Called by Handler() in
// server.go (TASK-008's file — outside this task's allowlist, so the Lead wires
// it; every route is wrapped in requireAdmin here).
func (s *Server) RegisterAdminRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin", s.requireAdmin(http.HandlerFunc(s.handleAdminDashboard)))
	mux.Handle("GET /admin/{$}", s.requireAdmin(http.HandlerFunc(s.handleAdminDashboard)))

	mux.Handle("GET /admin/clubs", s.requireAdmin(http.HandlerFunc(s.handleAdminClubsList)))
	mux.Handle("GET /admin/clubs/new", s.requireAdmin(http.HandlerFunc(s.handleAdminClubNew)))
	mux.Handle("POST /admin/clubs", s.requireAdmin(http.HandlerFunc(s.handleAdminClubCreate)))
	mux.Handle("POST /admin/clubs/{id}/rename", s.requireAdmin(http.HandlerFunc(s.handleAdminClubRename)))
	mux.Handle("POST /admin/clubs/{id}/delete", s.requireAdmin(http.HandlerFunc(s.handleAdminClubDelete)))

	mux.Handle("GET /admin/teams", s.requireAdmin(http.HandlerFunc(s.handleAdminTeamsList)))
	mux.Handle("GET /admin/teams/new", s.requireAdmin(http.HandlerFunc(s.handleAdminTeamNew)))
	mux.Handle("POST /admin/teams", s.requireAdmin(http.HandlerFunc(s.handleAdminTeamCreate)))
	// The U in teams CRUD (TASK-037): the edit form is /admin/teams/{id}/edit
	// and the POST target is the resource itself, /admin/teams/{id}.
	mux.Handle("GET /admin/teams/{id}/edit", s.requireAdmin(http.HandlerFunc(s.handleAdminTeamEdit)))
	mux.Handle("POST /admin/teams/{id}", s.requireAdmin(http.HandlerFunc(s.handleAdminTeamUpdate)))
	mux.Handle("POST /admin/teams/{id}/deactivate", s.requireAdmin(http.HandlerFunc(s.handleAdminTeamDeactivate)))
}

// ---------- view shapes (must match the TASK-009 template headers exactly) ----------

type dashAgeGroup struct {
	ID           int64
	DisplayName  string
	Competitions int
	Teams        int
}

type dashboardView struct {
	adminPage
	SeasonName      string
	AgeGroups       []dashAgeGroup
	LastResults     []matchRowView
	NextFixtures    []matchRowView
	LastBackup      *string
	QuickResultsURL string
}

type clubView struct {
	ID        int64
	Name      string
	IsActive  bool
	TeamCount int
}

type clubsView struct {
	adminPage
	Clubs []clubView
}

type teamOption struct {
	ID   int64
	Name string
}

type ageOption struct {
	ID          int64
	DisplayName string
}

type teamFormTeam struct {
	ID          int64
	ClubID      int64
	Label       string
	DisplayName string
	AgeGroupID  int64
}

type teamFormView struct {
	adminPage
	Title       string
	ActionURL   string
	Clubs       []teamOption
	AgeGroups   []ageOption
	Team        *teamFormTeam
	FieldErrors map[string]string

	// TeamLocked marks edit mode. The club select is rendered disabled because
	// the club is not writable by store.UpdateTeam — see the note there: a
	// team's club anchors its display name and history, so a wrong club is a
	// deactivate-and-recreate, not an edit. Offering an enabled control that
	// silently does nothing would be the worse bug.
	TeamLocked bool
}

// ---------- dashboard ----------

func (s *Server) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	view := dashboardView{adminPage: s.adminChrome(w, r, "dashboard")}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه"}}
	view.QuickResultsURL = "/admin/results"

	comps, err := s.store.Competitions(nil, nil)
	if err != nil {
		s.log.Error("dashboard: competitions", "error", err)
	}
	ages, err := s.store.AgeGroups()
	if err != nil {
		s.log.Error("dashboard: age groups", "error", err)
	}
	if season, err := s.store.ActiveSeason(); err == nil && season != nil {
		view.SeasonName = season.Name
	}

	// Per-age-group activity: competitions + registered teams.
	byAge := map[int64]*dashAgeGroup{}
	for _, a := range ages {
		byAge[a.ID] = &dashAgeGroup{ID: a.ID, DisplayName: a.DisplayName}
	}
	for _, c := range comps {
		if g, ok := byAge[c.AgeGroupID]; ok {
			g.Competitions++
			if regs, err := s.store.Registrations(c.ID); err == nil {
				g.Teams += len(regs)
			}
		}
	}
	for _, a := range ages {
		if g, ok := byAge[a.ID]; ok {
			view.AgeGroups = append(view.AgeGroups, *g)
		}
	}

	finished, upcoming := s.recentMatches(comps, 5)
	view.LastResults, view.NextFixtures = finished, upcoming
	view.LastBackup = s.lastBackupDate()

	s.renderAdmin(w, r, "dashboard", view, http.StatusOK)
}

// recentMatches collects the newest finished and nearest scheduled matches
// across competitions. GAP: the frozen DataStore contract has no global
// ListRecentMatches; this aggregates per competition and is therefore O(comps)
// queries. Report asks the Lead for a narrow store helper (see Next_actions).
func (s *Server) recentMatches(comps []store.Competition, limit int) (finished, upcoming []matchRowView) {
	statusFinished, statusScheduled := "finished", "scheduled"
	var fin, sch []store.Match
	for _, c := range comps {
		if ms, err := s.store.Matches(c.ID, nil, &statusFinished); err == nil {
			fin = append(fin, ms...)
		}
		if ms, err := s.store.Matches(c.ID, nil, &statusScheduled); err == nil {
			sch = append(sch, ms...)
		}
	}
	sort.SliceStable(fin, func(i, j int) bool { return matchDateAfter(fin[i], fin[j]) })
	sort.SliceStable(sch, func(i, j int) bool { return matchDateAfter(sch[j], sch[i]) })

	for i, m := range fin {
		if i >= limit {
			break
		}
		finished = append(finished, matchRowView{Match: m, ShowDate: true})
	}
	for i, m := range sch {
		if i >= limit {
			break
		}
		upcoming = append(upcoming, matchRowView{Match: m, ShowDate: true})
	}
	return finished, upcoming
}

// matchDateAfter reports whether a is later than b (nil dates sort last).
func matchDateAfter(a, b store.Match) bool {
	ad, bd := "", ""
	if a.ScheduledDate != nil {
		ad = *a.ScheduledDate
	}
	if b.ScheduledDate != nil {
		bd = *b.ScheduledDate
	}
	if ad == bd {
		at, bt := "", ""
		if a.ScheduledTime != nil {
			at = *a.ScheduledTime
		}
		if b.ScheduledTime != nil {
			bt = *b.ScheduledTime
		}
		return at > bt
	}
	if ad == "" {
		return false
	}
	if bd == "" {
		return true
	}
	return ad > bd
}

// lastBackupDate returns the last-backup ISO date, or nil when unknown.
// The store contract has no backup-history query yet, so the marker file
// written by the backup flow (TASK-014) is read when present. The path is not
// part of TASK-008's frozen Config, so it is read from the environment here
// (BACKUP_MARKER_FILE), defaulting to data/last-backup.txt.
func (s *Server) lastBackupDate() *string {
	marker := os.Getenv("BACKUP_MARKER_FILE")
	if marker == "" {
		marker = filepath.Join("data", "last-backup.txt")
	}
	if b, err := os.ReadFile(marker); err == nil {
		txt := strings.TrimSpace(string(b))
		if txt != "" {
			return &txt
		}
	}
	return nil
}

// ---------- clubs ----------

// clubList loads clubs with their team counts (shape shared by page + row swaps).
func (s *Server) clubList() ([]clubView, error) {
	clubs, err := s.store.Clubs(true)
	if err != nil {
		return nil, err
	}
	teams, err := s.store.Teams(nil)
	if err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	for _, t := range teams {
		counts[t.ClubID]++
	}
	out := make([]clubView, 0, len(clubs))
	for _, c := range clubs {
		out = append(out, clubView{ID: c.ID, Name: c.Name, IsActive: c.IsActive, TeamCount: counts[c.ID]})
	}
	return out, nil
}

func (s *Server) handleAdminClubsList(w http.ResponseWriter, r *http.Request) {
	view := clubsView{adminPage: s.adminChrome(w, r, "clubs")}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "باشگاهها"}}
	clubs, err := s.clubList()
	if err != nil {
		s.log.Error("clubs list", "error", err)
		view.Flash.Error = "خواندن فهرست باشگاهها ناموفق بود؛ دوباره تلاش کنید."
	}
	view.Clubs = clubs
	s.renderAdmin(w, r, "clubs", view, http.StatusOK)
}

// handleAdminClubNew renders a minimal create form. STOPGAP: TASK-009's
// clubs.html links /admin/clubs/new but ships no form template, so the form is
// built here (documented; the Lead can move it into a template later).
func (s *Server) handleAdminClubNew(w http.ResponseWriter, r *http.Request) {
	csrf := s.CSRFToken(r)
	flash := takeFlash(w, r)
	if flash.Error != "" {
		csrf = s.CSRFToken(r) // unchanged; kept explicit for clarity
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = clubNewTmpl.Execute(w, map[string]any{
		"CSRFToken": csrf,
		"Error":     flash.Error,
	})
}

func (s *Server) handleAdminClubCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		setFlash(w, r, flashError, "نام باشگاه الزامی است.")
		s.renderAdmin(w, r, "clubs", clubsView{adminPage: s.adminChrome(w, r, "clubs")}, http.StatusOK)
		return
	}
	if _, err := s.store.CreateClub(name); err != nil {
		// Store errors are Persian and user-safe (duplicate, etc).
		setFlash(w, r, flashError, err.Error())
		s.renderAdmin(w, r, "clubs", clubsView{adminPage: s.adminChrome(w, r, "clubs")}, http.StatusOK)
		return
	}
	setFlash(w, r, flashSuccess, "ثبت شد.")
	http.Redirect(w, r, "/admin/clubs", http.StatusSeeOther)
}

// handleAdminClubRename answers the htmx row swap with the updated <tr>.
func (s *Server) handleAdminClubRename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.renderAdminPartial(w, "club_row", http.StatusBadRequest, clubView{Name: "شناسهٔ نامعتبر"})
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		if row, err := s.clubRow(id); err == nil {
			s.renderAdminPartial(w, "club_row", http.StatusOK, row)
		} else {
			s.renderAdminPartial(w, "club_row", http.StatusBadRequest, clubView{ID: id, Name: "نام الزامی است"})
		}
		return
	}
	if err := s.store.RenameClub(id, name); err != nil {
		s.log.Warn("club rename rejected", "id", id, "error", err)
		row, rerr := s.clubRow(id)
		if rerr != nil {
			row = clubView{ID: id}
		}
		s.renderAdminPartial(w, "club_row_error", http.StatusBadRequest, clubRowError{Club: row, Message: err.Error()})
		return
	}
	row, err := s.clubRow(id)
	if err != nil {
		row = clubView{ID: id, Name: name}
	}
	s.renderAdminPartial(w, "club_row", http.StatusOK, row)
}

// handleAdminClubDelete: success → empty body (htmx removes the row); blocked →
// 400 with the row + a Persian message row (htmx 4 swaps 4xx bodies, D2).
func (s *Server) handleAdminClubDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ باشگاه نامعتبر است.")
		return
	}
	if err := s.store.DeactivateClub(id); err != nil {
		s.log.Warn("club deactivate blocked", "id", id, "error", err)
		row, rerr := s.clubRow(id)
		if rerr != nil {
			row = clubView{ID: id}
		}
		s.renderAdminPartial(w, "club_row_error", http.StatusBadRequest, clubRowError{Club: row, Message: err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK) // empty: the row disappears
}

// clubRowError pairs the row with a Persian message row for htmx error swaps.
type clubRowError struct {
	Club    clubView
	Message string
}

func (s *Server) clubRow(id int64) (clubView, error) {
	clubs, err := s.clubList()
	if err != nil {
		return clubView{}, err
	}
	for _, c := range clubs {
		if c.ID == id {
			return c, nil
		}
	}
	return clubView{ID: id}, nil
}

// ---------- teams ----------

// adminTeamsPageSize is the number of team rows rendered per page. The list used
// to render every team at once: 219 seeded teams produced 144,682 B of markup
// (fb3 audit finding F7), a slow and scroll-heavy screen for the operator this
// product is built for. At ~660 B per row, 50 rows keeps one page in the tens of
// kilobytes while staying long enough that page 2 is rarely reached.
const adminTeamsPageSize = 50

// teamsListView is the data contract for teamsListTmpl.
type teamsListView struct {
	Teams     []store.Team
	CSRFToken string
	Flash     flashMessage

	// Paging. Total is the store's full team count and is rendered to the
	// operator, so "1-50 of 219" can never disagree with the rows on the page.
	Page       int
	Pages      int
	ShownFrom  int
	ShownTo    int
	Total      int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
	OutOfRange bool
}

// handleAdminTeamsList: inline list (TASK-009 shipped only team_form), one
// page at a time, paged in the store (TASK-025).
//
// The store bounds both the query and the response: TeamsPage applies LIMIT /
// OFFSET and returns the true total. A nil store degrades to a Persian error
// plus the empty state instead of dereferencing it.
func (s *Server) handleAdminTeamsList(w http.ResponseWriter, r *http.Request) {
	flash := takeFlash(w, r)
	page := queryPage(r)
	if s.store == nil {
		flash.Error = "خواندن فهرست تیمها ناموفق بود؛ دوباره تلاش کنید."
	}

	view := teamsListView{
		Teams:     []store.Team{},
		CSRFToken: s.CSRFToken(r),
		Page:      page,
	}

	if s.store != nil {
		if teams, total, err := s.store.TeamsPage(nil, adminTeamsPageSize, (page-1)*adminTeamsPageSize); err != nil {
			s.log.Error("teams list", "error", err)
			flash.Error = "خواندن فهرست تیم‌ها ناموفق بود؛ دوباره تلاش کنید."
		} else {
			view.Teams = teams
			view.Total = total
		}
	}
	view.Flash = flash
	if view.Total > 0 {
		view.Pages = (view.Total + adminTeamsPageSize - 1) / adminTeamsPageSize
	}
	if view.Pages < 1 {
		view.Pages = 1
	}
	// A page past the end is neither an error nor a silent fallback to page 1:
	// the operator gets an explicit Persian empty state with a way back.
	view.OutOfRange = page > view.Pages
	if !view.OutOfRange && view.Total > 0 {
		view.ShownFrom = (page-1)*adminTeamsPageSize + 1
		view.ShownTo = (page-1)*adminTeamsPageSize + len(view.Teams)
	}
	view.HasPrev = page > 1 && page <= view.Pages
	view.HasNext = page < view.Pages
	view.PrevURL = teamsPageURL(page - 1)
	view.NextURL = teamsPageURL(page + 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := teamsListTmpl.Execute(w, view); err != nil {
		s.log.Error("render teams list", "error", err)
	}
}

// teamsPageURL is the self-link for a page of the team list. Page 1 is the bare
// route, so the canonical URL never advertises ?page=1.
func teamsPageURL(page int) string {
	if page <= 1 {
		return "/admin/teams"
	}
	return "/admin/teams?page=" + strconv.Itoa(page)
}

func (s *Server) handleAdminTeamNew(w http.ResponseWriter, r *http.Request) {
	view, err := s.teamFormView(r, nil, nil)
	if err != nil {
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	s.renderAdmin(w, r, "team_form", view, http.StatusOK)
}

// handleAdminTeamCreate validates per field and re-renders the form with 422 on
// error (htmx 4 swaps 422 bodies; DECISIONS.md D2).
func (s *Server) handleAdminTeamCreate(w http.ResponseWriter, r *http.Request) {
	fieldErrs := map[string]string{}
	clubID, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("club")), 10, 64)
	ageID, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("age_group")), 10, 64)
	displayName := strings.TrimSpace(r.PostFormValue("display_name"))
	label := strings.TrimSpace(r.PostFormValue("label"))

	if clubID <= 0 {
		fieldErrs["club"] = "انتخاب باشگاه الزامی است."
	}
	if ageID <= 0 {
		fieldErrs["age_group"] = "انتخاب ردهٔ سنی الزامی است."
	}
	if displayName == "" {
		fieldErrs["display_name"] = "نام نمایشی تیم الزامی است."
	}

	if len(fieldErrs) > 0 {
		view, err := s.teamFormView(r, &teamFormSubmission{ClubID: clubID, AgeGroupID: ageID, DisplayName: displayName, Label: label}, fieldErrs)
		if err != nil {
			writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
			return
		}
		s.renderAdmin(w, r, "team_form", view, http.StatusUnprocessableEntity)
		return
	}

	if _, err := s.store.CreateTeamWithAgeGroup(clubID, &ageID, label, displayName); err != nil {
		// Store messages are Persian and reference the offending names.
		fieldErrs["display_name"] = err.Error()
		view, verr := s.teamFormView(r, &teamFormSubmission{ClubID: clubID, AgeGroupID: ageID, DisplayName: displayName, Label: label}, fieldErrs)
		if verr != nil {
			writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
			return
		}
		s.renderAdmin(w, r, "team_form", view, http.StatusUnprocessableEntity)
		return
	}

	setFlash(w, r, flashSuccess, "ثبت شد.")
	http.Redirect(w, r, "/admin/teams", http.StatusSeeOther)
}

// handleAdminTeamEdit renders the create form in edit mode. The template is the
// same file the create path uses — its documented data contract already carries
// .Title and .ActionURL and prefills every field — so an edit screen cannot
// drift from the create screen (TASK-037).
func (s *Server) handleAdminTeamEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ تیم نامعتبر است.")
		return
	}
	if s.store == nil {
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	team, err := s.store.TeamByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writePersianError(w, http.StatusNotFound, err.Error())
			return
		}
		s.log.Error("team edit", "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	view, err := s.teamFormEditView(r, team, nil, nil)
	if err != nil {
		s.log.Error("team edit form", "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	s.renderAdmin(w, r, "team_form", view, http.StatusOK)
}

// handleAdminTeamUpdate saves an edit — the U in teams CRUD (TASK-037).
//
// Per-field validation and the 422 re-render follow the create path (D2), minus
// the club: the form locks it and store.UpdateTeam does not write it. Store
// errors land on the field that caused them rather than always on the display
// name, so «رده سنی با شناسه … یافت نشد» points at the category select.
func (s *Server) handleAdminTeamUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ تیم نامعتبر است.")
		return
	}

	fieldErrs := map[string]string{}
	ageID, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("age_group")), 10, 64)
	displayName := strings.TrimSpace(r.PostFormValue("display_name"))
	label := strings.TrimSpace(r.PostFormValue("label"))

	if ageID <= 0 {
		fieldErrs["age_group"] = "انتخاب ردهٔ سنی الزامی است."
	}
	if displayName == "" {
		fieldErrs["display_name"] = "نام نمایشی تیم الزامی است."
	}

	// Load the row first: the form must re-render with the right Title, action
	// and breadcrumb on the error path too, and an unknown id is a 404 rather
	// than a 422 on a form for a team that does not exist.
	team, err := s.store.TeamByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writePersianError(w, http.StatusNotFound, err.Error())
			return
		}
		s.log.Error("team update", "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}

	if len(fieldErrs) > 0 {
		view, verr := s.teamFormEditView(r, team, &teamFormSubmission{AgeGroupID: ageID, DisplayName: displayName, Label: label}, fieldErrs)
		if verr != nil {
			writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
			return
		}
		s.renderAdmin(w, r, "team_form", view, http.StatusUnprocessableEntity)
		return
	}

	if err := s.store.UpdateTeam(id, label, displayName, &ageID); err != nil {
		field := "display_name"
		switch {
		case errors.Is(err, store.ErrNotFound):
			field = "age_group"
		case errors.Is(err, store.ErrInUse):
			// The only reachable ErrInUse is the (club_id, label) UNIQUE.
			field = "label"
		}
		fieldErrs[field] = err.Error()
		view, verr := s.teamFormEditView(r, team, &teamFormSubmission{AgeGroupID: ageID, DisplayName: displayName, Label: label}, fieldErrs)
		if verr != nil {
			writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
			return
		}
		s.renderAdmin(w, r, "team_form", view, http.StatusUnprocessableEntity)
		return
	}

	setFlash(w, r, flashSuccess, "تغییرات ذخیره شد.")
	http.Redirect(w, r, "/admin/teams", http.StatusSeeOther)
}

func (s *Server) handleAdminTeamDeactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ تیم نامعتبر است.")
		return
	}
	if err := s.store.DeactivateTeam(id); err != nil {
		setFlash(w, r, flashError, err.Error())
		http.Redirect(w, r, "/admin/teams", http.StatusSeeOther)
		return
	}
	setFlash(w, r, flashSuccess, "تغییرات ذخیره شد.")
	http.Redirect(w, r, "/admin/teams", http.StatusSeeOther)
}

// teamFormSubmission carries user input back into the form on validation error.
type teamFormSubmission struct {
	ClubID      int64
	AgeGroupID  int64
	DisplayName string
	Label       string
}

func (s *Server) teamFormView(r *http.Request, sub *teamFormSubmission, errs map[string]string) (teamFormView, error) {
	view := teamFormView{
		adminPage:   s.adminChrome(nopWriter{}, r, "teams"),
		Title:       "تیم جدید",
		ActionURL:   "/admin/teams",
		FieldErrors: errs,
	}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "تیمها", Href: "/admin/teams"}, {Label: "تیم جدید"}}

	clubs, err := s.store.Clubs(false)
	if err != nil {
		return view, err
	}
	for _, c := range clubs {
		view.Clubs = append(view.Clubs, teamOption{ID: c.ID, Name: c.Name})
	}
	ages, err := s.store.AgeGroups()
	if err != nil {
		return view, err
	}
	for _, a := range ages {
		view.AgeGroups = append(view.AgeGroups, ageOption{ID: a.ID, DisplayName: a.DisplayName})
	}
	if sub != nil {
		view.Team = &teamFormTeam{
			ClubID: sub.ClubID, AgeGroupID: sub.AgeGroupID,
			DisplayName: sub.DisplayName, Label: sub.Label,
		}
	}
	return view, nil
}

// teamFormEditView builds team_form in edit mode. `sub` is what the operator
// just submitted on the 422 path; nil means "use the stored row" (the GET
// path). Either way the club comes from the stored team, never from the form —
// the club select is locked, and a disabled control is not submitted at all.
func (s *Server) teamFormEditView(r *http.Request, team store.Team, sub *teamFormSubmission, errs map[string]string) (teamFormView, error) {
	if sub == nil {
		ageID := int64(0)
		if team.AgeGroupID != nil {
			ageID = *team.AgeGroupID
		}
		sub = &teamFormSubmission{
			AgeGroupID:  ageID,
			DisplayName: team.DisplayName,
			Label:       team.Label,
		}
	}
	sub.ClubID = team.ClubID

	view, err := s.teamFormView(r, sub, errs)
	if err != nil {
		return view, err
	}
	view.Team.ID = team.ID
	view.Title = "ویرایش تیم"
	view.ActionURL = "/admin/teams/" + strconv.FormatInt(team.ID, 10)
	view.TeamLocked = true
	view.Breadcrumb = []breadcrumbItem{
		{Label: "خانه", Href: "/admin"},
		{Label: "تیم‌ها", Href: "/admin/teams"},
		{Label: "ویرایش تیم"},
	}
	return view, nil
}

// nopWriter swallows cookie writes: the form view is built before the real
// ResponseWriter is used, and flash cookies must only be written once.
type nopWriter struct{}

func (nopWriter) Header() http.Header       { return http.Header{} }
func (nopWriter) Write([]byte) (int, error) { return 0, nil }
func (nopWriter) WriteHeader(int)           {}

// ---------- inline stopgap templates ----------

var clubNewTmpl = template.Must(template.New("club_new").Parse(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>باشگاه جدید — بشقاب مدیریتی</title>
<link rel="stylesheet" href="/static/css/main.css"></head>
<body><main class="container">
  <nav class="breadcrumb"><a href="/admin">خانه</a><span class="breadcrumb-sep">/</span><a href="/admin/clubs">باشگاه‌ها</a><span class="breadcrumb-sep">/</span><span>باشگاه جدید</span></nav>
  <section class="card">
    <h1 class="section-title">باشگاه جدید</h1>
    <p class="muted">نام رسمی باشگاه را دقیقاً همان‌طور که مرجع اعلام کرده وارد کنید؛ سیستم آن را تغییر نمی‌دهد.</p>
    {{if .Error}}<div class="flash error">{{.Error}}</div>{{end}}
    <form method="post" action="/admin/clubs">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <div class="form-row"><div class="field">
        <label for="name">نام رسمی باشگاه</label>
        <input id="name" name="name" type="text" required placeholder="مثلاً: نمونه ب">
      </div></div>
      <div class="form-row">
        <button class="btn primary large" type="submit">ذخیره</button>
        <a class="btn ghost" href="/admin/clubs">انصراف</a>
      </div>
    </form>
  </section>
</main></body></html>`))

// teamsListTmpl renders /admin/teams. Data contract: teamsListView above --
// .Teams is the current page's rows, .Total is the store's full count, and
// .Page/.Pages drive the pager. It is an inline string template because
// TASK-009 shipped only team_form.html and no teams.html exists; converting it
// to a file template is a template-loader change, not a paging one (see the
// TASK-023 report).
var teamsListTmpl = template.Must(template.New("teams_list").Funcs(template.FuncMap{
	"toFa": jalali.ToPersianDigits,
}).Parse(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>تیم‌ها — بشقاب مدیریتی</title>
<link rel="stylesheet" href="/static/css/main.css"></head>
<body><main class="container">
  <nav class="breadcrumb"><a href="/admin">خانه</a><span class="breadcrumb-sep">/</span><span>تیم‌ها</span></nav>
  {{if .Flash.Success}}<div class="flash success">{{.Flash.Success}}</div>{{end}}
  {{if .Flash.Error}}<div class="flash error">{{.Flash.Error}}</div>{{end}}
  <section class="card">
    <h1 class="section-title">تیم‌ها</h1>
    <div class="form-row"><a class="btn primary large" href="/admin/teams/new">تیم جدید</a></div>
  </section>
  {{if not .Total}}
    <div class="empty-state">هنوز تیمی ثبت نشده است. با «تیم جدید» شروع کنید.</div>
  {{else if .OutOfRange}}
    <div class="empty-state">صفحه‌ای با این شماره وجود ندارد. فهرست تیم‌ها {{toFa (printf "%d" .Pages)}} صفحه دارد.</div>
    <div class="form-row"><a class="btn" href="/admin/teams">بازگشت به صفحهٔ اول</a></div>
  {{else}}
    <p class="muted">نمایش {{toFa (printf "%d" .ShownFrom)}} تا {{toFa (printf "%d" .ShownTo)}} از {{toFa (printf "%d" .Total)}} تیم</p>
    <div class="table-wrap">
      <table class="standings">
        <thead><tr><th>باشگاه</th><th>تیم</th><th>ردهٔ سنی</th><th>برچسب</th><th>عملیات</th></tr></thead>
        <tbody>
          {{range .Teams}}<tr>
            <td>{{.ClubName}}</td>
            <td>{{.DisplayName}}</td>
            <td>{{if .AgeGroupName}}{{.AgeGroupName}}{{else}}—{{end}}</td>
            <td>{{if .Label}}{{.Label}}{{else}}—{{end}}</td>
            <td>
              <a class="btn" href="/admin/teams/{{.ID}}/edit">ویرایش</a>
              <form method="post" action="/admin/teams/{{.ID}}/deactivate">
                <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
                <button class="btn danger" type="submit" data-confirm="با غیرفعال‌کردن تیم، سوابق آن حذف نمی‌شود ولی دیگر در مسابقات جدید قابل انتخاب نیست. مطمئن هستید؟">غیرفعال‌کردن</button>
              </form>
            </td>
          </tr>{{end}}
        </tbody>
      </table>
    </div>
    {{if gt .Pages 1}}
      <div class="form-row">
        {{if .HasPrev}}<a class="btn" href="{{.PrevURL}}">صفحهٔ قبل</a>{{end}}
        <span class="muted">صفحهٔ {{toFa (printf "%d" .Page)}} از {{toFa (printf "%d" .Pages)}}</span>
        {{if .HasNext}}<a class="btn" href="{{.NextURL}}">صفحهٔ بعد</a>{{end}}
      </div>
    {{end}}
  {{end}}
</main></body></html>`))
