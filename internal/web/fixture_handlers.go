// Fixture management (TASK-012 part A; PROJECT_SPEC §10/§11, D8). Fixtures are
// OFFICIAL data: the administrator enters or imports them, and the product never
// generates them. This file is the manual-entry path; import_handlers.go is the
// bulk path.
//
// Routes (all behind requireAdmin + the global CSRF middleware):
//
//	GET  /admin/fixtures?competition=&week=   picker + week list + new-match form
//	POST /admin/fixtures                      create one scheduled fixture
//	POST /admin/fixtures/{id}/delete          delete a fixture
//
// Mutations use POST-redirect-GET with a flash message, so a refresh never
// re-submits and every error the store raises (Persian) is shown verbatim.
//
// The page owns the pages it registers via init(), the same pattern TASK-014
// used for backup/audit: render.go's adminPageFiles map belongs to TASK-010 and
// is outside this task's allowlist.
package web

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

const fixturesPath = "/admin/fixtures"

func init() {
	adminPageFiles["fixtures"] = "admin/fixtures.html"
	adminPageFiles["import"] = "admin/import.html"
}

// RegisterFixtureImportRoutes mounts fixture management and the import pipeline.
// Called once from Handler() by the Lead — registering a pattern twice on a
// ServeMux panics.
func (s *Server) RegisterFixtureImportRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/fixtures", s.requireAdmin(http.HandlerFunc(s.handleAdminFixtures)))
	mux.Handle("POST /admin/fixtures", s.requireAdmin(http.HandlerFunc(s.handleAdminFixtureCreate)))
	mux.Handle("POST /admin/fixtures/{id}/delete", s.requireAdmin(http.HandlerFunc(s.handleAdminFixtureDelete)))

	mux.Handle("GET /admin/import", s.requireAdmin(http.HandlerFunc(s.handleAdminImport)))
	mux.Handle("POST /admin/import", s.requireAdmin(http.HandlerFunc(s.handleAdminImportPreview)))
	mux.Handle("POST /admin/import/confirm", s.requireAdmin(http.HandlerFunc(s.handleAdminImportConfirm)))
}

// ---------- view shapes (documented in the template headers) ----------

// fixtureTeamOption is one selectable team — always a team REGISTERED in the
// selected competition, so an unregistered pairing cannot be submitted.
type fixtureTeamOption struct {
	ID   int64
	Name string
}

// fixtureRowView is one scheduled fixture row.
type fixtureRowView struct {
	Match    store.Match
	WhenText string // Jalali date + optional time, server-rendered
}

type fixturesView struct {
	adminPage

	SeasonName            string
	AgeGroups             []resultAgeGroup
	SelectedCompetitionID int64
	SelectedName          string
	Week                  int
	Weeks                 []int
	Teams                 []fixtureTeamOption
	Rows                  []fixtureRowView

	// Form hints/echoes. DateHint is Jalali «۱۴۰۵/۰۷/۲۰»-style guidance.
	DateHint  string
	WeekInput int
}

// ---------- page ----------

func (s *Server) handleAdminFixtures(w http.ResponseWriter, r *http.Request) {
	view := fixturesView{adminPage: s.fixturesChrome(w, r)}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "برنامهٔ بازی‌ها"}}
	view.DateHint = jalali.Format(time.Now())
	if s.store == nil {
		s.renderAdmin(w, r, "fixtures", view, http.StatusOK)
		return
	}

	groups, comps := s.competitionPicker()
	view.AgeGroups = groups
	if len(comps) == 0 {
		s.renderAdmin(w, r, "fixtures", view, http.StatusOK)
		return
	}

	sel := s.selectCompetition(r, comps)
	view.SelectedCompetitionID = sel.ID
	view.SelectedName = sel.DisplayName
	view.Breadcrumb = append(view.Breadcrumb, breadcrumbItem{Label: sel.DisplayName})

	// Team dropdowns: only teams registered in THIS competition (store identity).
	regs, err := s.store.Registrations(sel.ID)
	if err != nil {
		s.log.Error("fixtures: registrations", "error", err, "competition", sel.ID)
		s.renderAdmin(w, r, "fixtures", view, http.StatusOK)
		return
	}
	for _, reg := range regs {
		view.Teams = append(view.Teams, fixtureTeamOption{ID: reg.TeamID, Name: reg.TeamName})
	}

	all, err := s.store.Matches(sel.ID, nil, nil)
	if err != nil {
		s.log.Error("fixtures: matches", "error", err, "competition", sel.ID)
		s.renderAdmin(w, r, "fixtures", view, http.StatusOK)
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
	view.Week = week
	view.WeekInput = week

	for _, m := range all {
		if week > 0 && (m.Week == nil || *m.Week != week) {
			continue
		}
		view.Rows = append(view.Rows, fixtureRowView{Match: m, WhenText: matchWhenText(m)})
	}
	s.renderAdmin(w, r, "fixtures", view, http.StatusOK)
}

// ---------- mutations ----------

func (s *Server) handleAdminFixtureCreate(w http.ResponseWriter, r *http.Request) {
	redirect := fixturesPath
	if comp, ok := parsePositiveInt(r.PostFormValue("competition")); ok {
		redirect += "?competition=" + strconv.Itoa(comp)
	}
	if s.store == nil {
		s.flashAndRedirect(w, r, flashError, "پایگاه داده در دسترس نیست.", redirect)
		return
	}
	compID, ok := parsePositiveInt(r.PostFormValue("competition"))
	if !ok {
		s.flashAndRedirect(w, r, flashError, "مسابقات انتخاب‌شده معتبر نیست.", redirect)
		return
	}
	homeID, okHome := parsePositiveInt(r.PostFormValue("home_team"))
	awayID, okAway := parsePositiveInt(r.PostFormValue("away_team"))
	if !okHome || !okAway {
		s.flashAndRedirect(w, r, flashError, "تیم میزبان و مهمان را انتخاب کنید.", redirect)
		return
	}
	if homeID == awayID {
		s.flashAndRedirect(w, r, flashError, "یک تیم نمی‌تواند با خودش بازی کند.", redirect)
		return
	}

	// Mirror the store's integrity rules before writing, so the admin gets one
	// clear Persian message instead of a constraint error.
	regs, err := s.store.Registrations(int64(compID))
	if err != nil {
		s.log.Error("fixture create: registrations", "error", err)
		s.flashAndRedirect(w, r, flashError, dataErrorMessage, redirect)
		return
	}
	registered := map[int64]bool{}
	for _, reg := range regs {
		registered[reg.TeamID] = true
	}
	if !registered[int64(homeID)] || !registered[int64(awayID)] {
		s.flashAndRedirect(w, r, flashError,
			"هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند.", redirect)
		return
	}

	// Date is required for a fixture; accept Jalali or ISO input.
	rawDate := r.PostFormValue("date")
	isoDate, err := parseDateInput(rawDate)
	if err != nil {
		s.flashAndRedirect(w, r, flashError, err.Error(), redirect)
		return
	}

	rawTime := r.PostFormValue("time")
	var timePtr *string
	if rawTime != "" {
		t, err := parseTimeInput(rawTime)
		if err != nil {
			s.flashAndRedirect(w, r, flashError, err.Error(), redirect)
			return
		}
		timePtr = &t
	}

	var weekPtr *int
	weekText := r.PostFormValue("week")
	if weekText != "" {
		wv, ok := parsePositiveInt(weekText)
		if !ok {
			s.flashAndRedirect(w, r, flashError, "شمارهٔ هفته نامعتبر است.", redirect)
			return
		}
		weekPtr = &wv
	}

	venue := r.PostFormValue("venue")
	var venuePtr *string
	if venue != "" {
		venuePtr = &venue
	}

	if _, err := s.store.CreateMatch(int64(compID), int64(homeID), int64(awayID), weekPtr, &isoDate, timePtr, venuePtr); err != nil {
		// The store's message is already Persian and specific (duplicate fixture,
		// unregistered team, …); show it as-is rather than inventing a second one.
		s.flashAndRedirect(w, r, flashError, err.Error(), redirect)
		return
	}
	s.flashAndRedirect(w, r, flashSuccess, "مسابقه ثبت شد.", redirect)
}

func (s *Server) handleAdminFixtureDelete(w http.ResponseWriter, r *http.Request) {
	redirect := fixturesPath
	if comp, ok := parsePositiveInt(r.PostFormValue("competition")); ok {
		redirect += "?competition=" + strconv.Itoa(comp)
	}
	if s.store == nil {
		s.flashAndRedirect(w, r, flashError, "پایگاه داده در دسترس نیست.", redirect)
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.flashAndRedirect(w, r, flashError, "شناسهٔ مسابقه نامعتبر است.", redirect)
		return
	}
	if err := s.store.DeleteMatch(id); err != nil {
		s.flashAndRedirect(w, r, flashError, err.Error(), redirect)
		return
	}
	s.flashAndRedirect(w, r, flashSuccess, "مسابقه حذف شد.", redirect)
}

// ---------- helpers ----------

// fixturesChrome is adminChrome with the nil-store guard the foundation's tests
// require (adminChrome dereferences s.store unconditionally).
func (s *Server) fixturesChrome(w http.ResponseWriter, r *http.Request) adminPage {
	if s.store == nil {
		return adminPage{SiteName: SiteName, CSRFToken: s.CSRFToken(r), NavActive: "fixtures", Flash: takeFlash(w, r)}
	}
	return s.adminChrome(w, r, "fixtures")
}

// flashAndRedirect stores a one-shot message and sends the browser back to a GET.
func (s *Server) flashAndRedirect(w http.ResponseWriter, r *http.Request, kind, message, location string) {
	setFlash(w, r, kind, message)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// competitionPicker groups the active season's competitions by age group, with
// the same ordering conventions as the TASK-011 results page (age ascending,
// premier before League 1 groups). It returns both the grouped <optgroup> shape
// and the flat ordered list.
func (s *Server) competitionPicker() ([]resultAgeGroup, []store.Competition) {
	comps, err := s.activeSeasonCompetitions()
	if err != nil {
		s.log.Error("competition picker", "error", err)
		return nil, nil
	}
	ageNames := map[int64]string{}
	if ages, err := s.store.AgeGroups(); err == nil {
		for _, a := range ages {
			ageNames[a.ID] = a.DisplayName
		}
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

	byAge := map[int64][]resultOption{}
	for _, c := range comps {
		byAge[c.AgeGroupID] = append(byAge[c.AgeGroupID], resultOption{ID: c.ID, Name: c.DisplayName})
	}
	var groups []resultAgeGroup
	seen := map[int64]bool{}
	for _, c := range comps {
		if seen[c.AgeGroupID] {
			continue
		}
		seen[c.AgeGroupID] = true
		groups = append(groups, resultAgeGroup{DisplayName: ageNames[c.AgeGroupID], Competitions: byAge[c.AgeGroupID]})
	}
	return groups, comps
}

// selectCompetition honours ?competition= when it names a real competition,
// otherwise falls back to the first competition that already has teams.
func (s *Server) selectCompetition(r *http.Request, comps []store.Competition) store.Competition {
	if q, ok := parsePositiveInt(r.URL.Query().Get("competition")); ok {
		for _, c := range comps {
			if c.ID == int64(q) {
				return c
			}
		}
	}
	for _, c := range comps {
		if regs, err := s.store.Registrations(c.ID); err == nil && len(regs) > 0 {
			return c
		}
	}
	return comps[0]
}

// errPersian is a validation error whose text is already the Persian the admin
// should read (the same convention the store layer uses for its errors).
func errPersian(msg string) error { return errors.New(msg) }

// parseDateInput accepts a Jalali «۱۴۰۵/۰۷/۲۰» or an ISO «2026-10-12» date and
// returns the canonical ISO form. The message is Persian and actionable.
func parseDateInput(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errPersian("تاریخ مسابقه لازم است (نمونه: ۱۴۰۵/۰۷/۲۰).")
	}
	// Try ISO first (a machine-produced value), then Jalali via the single
	// conversion authority (D9).
	if t, err := time.Parse(isoLayout, trimmed); err == nil {
		return t.Format(isoLayout), nil
	}
	t, err := jalali.Parse(raw)
	if err != nil {
		return "", errPersian("تاریخ نامعتبر است؛ نمونهٔ درست: ۱۴۰۵/۰۷/۲۰.")
	}
	return jalali.FormatISO(t), nil
}

// parseTimeInput validates an «HH:MM» clock value. Persian or Latin digits are
// accepted, as is the four-digit shorthand «۱۶۳۰».
func parseTimeInput(raw string) (string, error) {
	badClock := errPersian("ساعت نامعتبر است؛ نمونهٔ درست: ۱۶:۳۰.")
	s := strings.TrimSpace(raw)
	s = strings.NewReplacer(".", ":", "：", ":", "٫", ":").Replace(s)

	parts := strings.Split(s, ":")
	if len(parts) == 1 {
		if d, ok := normalizeDigits(parts[0]); ok && len(d) == 4 {
			parts = []string{d[:2], d[2:]}
		}
	}
	if len(parts) != 2 {
		return "", badClock
	}
	hh, okH := normalizeDigits(parts[0])
	mm, okM := normalizeDigits(parts[1])
	if !okH || !okM {
		return "", badClock
	}
	h, errH := strconv.Atoi(hh)
	m, errM := strconv.Atoi(mm)
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", badClock
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}

// matchWhenText renders a fixture's date and optional time for the row list.
func matchWhenText(m store.Match) string {
	out := ""
	if m.ScheduledDate != nil && *m.ScheduledDate != "" {
		if t, err := time.Parse(isoLayout, *m.ScheduledDate); err == nil {
			out = jalali.Format(t)
		}
	}
	if m.ScheduledTime != nil && *m.ScheduledTime != "" {
		if out != "" {
			out += " — "
		}
		out += jalali.ToPersianDigits(*m.ScheduledTime)
	}
	return out
}

// renderAppPartial executes a named fragment from one admin page set. render.go's
// renderAdminPartial is bound to the "clubs" set, so pages that need their own
// fragments use this.
func (s *Server) renderAppPartial(w http.ResponseWriter, page, name string, status int, data any) {
	pages, err := adminPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("admin templates unavailable", "error", err, "partial", name)
		writePersianError(w, http.StatusInternalServerError, adminGenericMessage)
		return
	}
	tmpl, ok := pages[page]
	if !ok {
		s.log.Error("admin page set missing", "page", page, "partial", name)
		writePersianError(w, http.StatusInternalServerError, adminGenericMessage)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render partial failed", "partial", name, "error", err)
		writePersianError(w, http.StatusInternalServerError, adminGenericMessage)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
