// Seasons admin UI (TASK-016) — the root of every other record (PROJECT_SPEC §4).
//
// Why this screen exists: a fresh install had no way to create a season without a
// SQL client, and every competition registration hangs off one (fb3 audit F2). The
// store already owns the single-active rule (`ActivateSeason` deactivates the rest
// inside one transaction) and the audit rows; this file only drives it.
//
// Deliberate omissions:
//   - There is NO delete route, and no disabled button hinting at one. §7 rule 7:
//     seasons are never deleted (their competitions, registrations and matches
//     would be orphaned), so the surface must not offer the idea.
//   - Activate is a plain form POST + redirect, not an htmx row swap: it changes
//     the badge on TWO rows at once, and only a full reload can guarantee the
//     operator never sees two active seasons on screen.
//
// Conventions copied from TASK-015's competition_handlers.go: an own registrar
// wrapping every route in requireAdmin, an own cached template set (render.go's
// adminPageFiles is TASK-010's lane), Persian validation errors on 422 with the
// same page re-rendered (htmx 4 swaps 4xx bodies — D2), and row-level swaps for
// the inline rename form.
package web

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// RegisterSeasonRoutes mounts the seasons surface. Every handler is wrapped in
// requireAdmin here (the TASK-013/014 pattern); wiring this into Handler() is the
// Lead's one-line integration.
func (s *Server) RegisterSeasonRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/seasons", s.requireAdmin(http.HandlerFunc(s.handleSeasonsList)))
	mux.Handle("GET /admin/seasons/new", s.requireAdmin(http.HandlerFunc(s.handleSeasonsList)))
	mux.Handle("POST /admin/seasons", s.requireAdmin(http.HandlerFunc(s.handleSeasonCreate)))
	mux.Handle("POST /admin/seasons/{id}/edit", s.requireAdmin(http.HandlerFunc(s.handleSeasonEdit)))
	mux.Handle("POST /admin/seasons/{id}/activate", s.requireAdmin(http.HandlerFunc(s.handleSeasonActivate)))
	// No POST /admin/seasons/{id}/delete — see the package comment.
}

// ---------- view shapes ----------

type seasonRowView struct {
	ID           int64
	Name         string
	IsActive     bool
	StartsOn     string // canonical ISO date, "" when unset
	EndsOn       string
	StartsLabel  string // Jalali display, "" when unset
	EndsLabel    string
	StartsInput  string // Jalali for the edit input, "" when unset
	EndsInput    string
	Competitions int
	IsEmpty      bool   // no competitions yet — the activate warning
	CSRFToken    string // the row's no-JS form posts (base handles the htmx path)
}

type seasonsView struct {
	adminPage

	Seasons    []seasonRowView
	HasSeasons bool
	ActiveName string

	SeasonsPath string // "/admin/seasons" — the form's action, kept out of the template

	FormName     string
	FormStartsOn string
	FormEndsOn   string
	FieldErrors  map[string]string
	FormError    string
}

// seasonRowError pairs a row with a Persian message for htmx error swaps
// (mirrors clubRowError / competitionRowError).
type seasonRowError struct {
	Season  seasonRowView
	Message string
}

// ---------- template set ----------

// Own page set: admin_base + partials + the single page file, cached per
// templates dir. seasons.html renders with block overrides in a clone, exactly
// like renderAdmin and competitionPageSet do.
var (
	seasonTmplMu    sync.Mutex
	seasonTmplCache = map[string]map[string]*template.Template{}
	seasonTmplErr   = map[string]error{}
)

func seasonPageSet(dir string) (map[string]*template.Template, error) {
	seasonTmplMu.Lock()
	defer seasonTmplMu.Unlock()

	if pages, ok := seasonTmplCache[dir]; ok {
		return pages, nil
	}
	if err := seasonTmplErr[dir]; err != nil {
		return nil, err
	}
	fsys := os.DirFS(dir)
	funcs := templateFuncMap(nil)

	base, err := template.New("seasonbase").Funcs(funcs).ParseFS(fsys, "admin_base.html", "partials/*.html")
	if err != nil {
		seasonTmplErr[dir] = err
		return nil, err
	}
	parsed, err := base.Clone()
	if err != nil {
		seasonTmplErr[dir] = err
		return nil, err
	}
	if _, err := parsed.ParseFS(fsys, "admin/seasons.html"); err != nil {
		seasonTmplErr[dir] = err
		return nil, err
	}
	pages := map[string]*template.Template{"seasons": parsed}
	seasonTmplCache[dir] = pages
	return pages, nil
}

func (s *Server) renderSeasonPage(w http.ResponseWriter, page string, data any, status int) {
	if status == 0 {
		status = http.StatusOK
	}
	pages, err := seasonPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("season templates unavailable", "error", err, "page", page)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	tmpl, ok := pages[page]
	if !ok {
		s.log.Error("unknown season page", "page", page)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "admin_base.html", data); err != nil {
		s.log.Error("render season page failed", "page", page, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderSeasonPartial renders a named {{define}} from seasons.html (season_row,
// season_row_error) for htmx row swaps.
func (s *Server) renderSeasonPartial(w http.ResponseWriter, name string, status int, data any) {
	pages, err := seasonPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("season templates unavailable", "error", err, "partial", name)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	var buf bytes.Buffer
	if err := pages["seasons"].ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render season partial failed", "partial", name, "error", err)
		writePersianError(w, http.StatusInternalServerError, "خطایی رخ داد؛ لطفاً دوباره تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// ---------- list ----------

func (s *Server) handleSeasonsList(w http.ResponseWriter, r *http.Request) {
	view := seasonsView{adminPage: s.adminChrome(w, r, "seasons"), SeasonsPath: "/admin/seasons"}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "فصل‌ها"}}
	if rows, err := s.seasonRows(s.CSRFToken(r)); err != nil {
		s.log.Error("seasons: list", "error", err)
		view.Flash.Error = "خواندن فهرست فصل‌ها ناموفق بود؛ دوباره تلاش کنید."
	} else {
		view.Seasons = rows
		view.HasSeasons = len(rows) > 0
		for _, row := range rows {
			if row.IsActive {
				view.ActiveName = row.Name
			}
		}
	}
	s.renderSeasonPage(w, "seasons", view, http.StatusOK)
}

// seasonRows loads every season with its competition count. The Jalali display
// forms are computed here (not in the template) so the list and the post-edit row
// swap can never disagree — the same reason competitionRowViewFor is shared.
// csrf is attached per row because every row carries its own no-JS form.
func (s *Server) seasonRows(csrf string) ([]seasonRowView, error) {
	seasons, err := s.store.Seasons()
	if err != nil {
		return nil, err
	}
	rows := make([]seasonRowView, 0, len(seasons))
	for _, se := range seasons {
		row := seasonRowView{ID: se.ID, Name: se.Name, IsActive: se.IsActive, CSRFToken: csrf}
		if se.StartsOn != nil {
			row.StartsOn = *se.StartsOn
			row.StartsLabel = jalaliLabel(*se.StartsOn)
			row.StartsInput = row.StartsLabel
		}
		if se.EndsOn != nil {
			row.EndsOn = *se.EndsOn
			row.EndsLabel = jalaliLabel(*se.EndsOn)
			row.EndsInput = row.EndsLabel
		}
		if comps, err := s.store.Competitions(&se.ID, nil); err == nil {
			row.Competitions = len(comps)
		}
		row.IsEmpty = row.Competitions == 0
		rows = append(rows, row)
	}
	return rows, nil
}

// seasonRow loads one row by id, for the post-edit htmx swap.
func (s *Server) seasonRow(csrf string, id int64) (seasonRowView, error) {
	rows, err := s.seasonRows(csrf)
	if err != nil {
		return seasonRowView{ID: id}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return seasonRowView{ID: id}, nil
}

// ---------- create ----------

func (s *Server) handleSeasonCreate(w http.ResponseWriter, r *http.Request) {
	view := seasonsView{adminPage: s.adminChrome(w, r, "seasons"), SeasonsPath: "/admin/seasons"}
	view.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "فصل‌ها"}}
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, dataErrorMessage)
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	startRaw := strings.TrimSpace(r.PostFormValue("starts_on"))
	endRaw := strings.TrimSpace(r.PostFormValue("ends_on"))
	view.FormName, view.FormStartsOn, view.FormEndsOn = name, startRaw, endRaw

	// Re-render the page with the list too, so a 422 shows the form errors AND
	// the operator's context instead of an empty page.
	if rows, err := s.seasonRows(s.CSRFToken(r)); err == nil {
		view.Seasons = rows
		view.HasSeasons = len(rows) > 0
		for _, row := range rows {
			if row.IsActive {
				view.ActiveName = row.Name
			}
		}
	}

	errs := map[string]string{}
	if name == "" {
		errs["name"] = "نام فصل الزامی است."
	}
	startPtr, endPtr := s.seasonDates(startRaw, endRaw, errs)
	if len(errs) > 0 {
		view.FieldErrors = errs
		s.renderSeasonPage(w, "seasons", view, http.StatusUnprocessableEntity)
		return
	}

	if _, err := s.store.CreateSeason(name, startPtr, endPtr); err != nil {
		// The store's Persian message is surfaced verbatim (never re-worded).
		if errors.Is(err, store.ErrDuplicateName) {
			errs["name"] = err.Error()
		} else {
			view.FormError = err.Error()
		}
		view.FieldErrors = errs
		s.renderSeasonPage(w, "seasons", view, http.StatusUnprocessableEntity)
		return
	}
	setFlash(w, r, flashSuccess, "فصل «"+name+"» ثبت شد؛ برای نمایش در سایت عمومی آن را فعال کنید.")
	http.Redirect(w, r, "/admin/seasons", http.StatusSeeOther)
}

// seasonDates parses the optional Jalali start/end inputs into canonical ISO
// pointers, filling errs with the Persian field messages. internal/jalali owns
// every conversion and message (D9) — no date math lives here.
func (s *Server) seasonDates(startRaw, endRaw string, errs map[string]string) (start, end *string) {
	startT, hasStart, err := parseSeasonDate(startRaw)
	if err != nil {
		errs["starts_on"] = err.Error()
	}
	endT, hasEnd, err := parseSeasonDate(endRaw)
	if err != nil {
		errs["ends_on"] = err.Error()
	}
	if hasStart && hasEnd && endT.Before(startT) && errs["ends_on"] == "" {
		errs["ends_on"] = "تاریخ پایان نمی‌تواند پیش از تاریخ شروع باشد."
	}
	if len(errs) > 0 {
		return nil, nil
	}
	if hasStart {
		sv := jalali.FormatISO(startT)
		start = &sv
	}
	if hasEnd {
		ev := jalali.FormatISO(endT)
		end = &ev
	}
	return start, end
}

// parseSeasonDate parses one optional Jalali date input. An empty string means
// "unset" (not an error); anything else must parse, or the operator gets the
// Persian message from internal/jalali.
func parseSeasonDate(raw string) (time.Time, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false, nil
	}
	t, err := jalali.Parse(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// jalaliLabel renders a stored ISO date as the standard Persian date
// («۱۴۰۵/۰۷/۲۰»). An unparseable stored value renders as empty rather than a
// wrong date.
func jalaliLabel(iso string) string {
	t, err := time.Parse(isoLayout, iso)
	if err != nil {
		return ""
	}
	return jalali.Format(t)
}

// ---------- edit ----------

// handleSeasonEdit renames a season and/or adjusts its dates. Official names are
// authority-controlled (D7): the store trims whitespace only, and its Persian
// message is surfaced verbatim. Success answers the refreshed row; a refusal
// answers 400 with the row + message (D2: htmx 4 swaps 4xx bodies).
func (s *Server) handleSeasonEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ فصل نامعتبر است.")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		s.seasonRowReject(w, r, id, errors.New("نام فصل الزامی است."))
		return
	}
	errs := map[string]string{}
	startPtr, endPtr := s.seasonDates(strings.TrimSpace(r.PostFormValue("starts_on")), strings.TrimSpace(r.PostFormValue("ends_on")), errs)
	if len(errs) > 0 {
		msg := errs["ends_on"]
		if msg == "" {
			msg = errs["starts_on"]
		}
		s.seasonRowReject(w, r, id, errors.New(msg))
		return
	}
	if err := s.store.UpdateSeason(id, name, startPtr, endPtr); err != nil {
		s.seasonRowReject(w, r, id, err)
		return
	}
	row, err := s.seasonRow(s.CSRFToken(r), id)
	if err != nil {
		row = seasonRowView{ID: id, Name: name}
	}
	s.renderSeasonPartial(w, "season_row", http.StatusOK, row)
}

// ---------- activate ----------

// handleSeasonActivate makes one season the single active season. The store
// performs the deactivate-others + activate in one transaction, so this handler
// never touches the rule itself. Plain POST + redirect (no htmx): activation
// rewrites the badge on two rows, and only a full reload proves there is exactly
// one active season on screen.
//
// A season with zero competitions still activates — the operator may legitimately
// activate first and build the competitions after (the brief's guard) — but the
// flash says so in Persian. It is a success flash, not an error: nothing failed.
func (s *Server) handleSeasonActivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || s.store == nil {
		writePersianError(w, http.StatusBadRequest, "شناسهٔ فصل نامعتبر است.")
		return
	}

	seasons, err := s.store.Seasons()
	if err != nil {
		s.log.Error("activate season: list", "error", err, "id", id)
		writePersianError(w, http.StatusInternalServerError, dataErrorMessage)
		return
	}
	var target *store.Season
	for i := range seasons {
		if seasons[i].ID == id {
			target = &seasons[i]
		}
	}
	if target == nil {
		// The store would answer ErrNotFound, but naming the season in the flash
		// requires it, so the miss is handled here.
		setFlash(w, r, flashError, "فصل انتخاب‌شده یافت نشد.")
		http.Redirect(w, r, "/admin/seasons", http.StatusSeeOther)
		return
	}

	if err := s.store.ActivateSeason(id); err != nil {
		s.log.Warn("activate season refused", "id", id, "error", err)
		setFlash(w, r, flashError, err.Error())
		http.Redirect(w, r, "/admin/seasons", http.StatusSeeOther)
		return
	}

	msg := "فصل «" + target.Name + "» فعال شد."
	if comps, cerr := s.store.Competitions(&id, nil); cerr == nil && len(comps) == 0 {
		msg += " توجه: این فصل هنوز هیچ مسابقه‌ای ندارد؛ می‌توانید همین حالا مسابقات آن را بسازید."
	}
	setFlash(w, r, flashSuccess, msg)
	http.Redirect(w, r, "/admin/seasons", http.StatusSeeOther)
}

// seasonRowReject answers a refused edit with the row plus the Persian message,
// so the operator sees why and can act.
func (s *Server) seasonRowReject(w http.ResponseWriter, r *http.Request, id int64, cause error) {
	s.log.Warn("season mutation refused", "id", id, "error", cause)
	row, err := s.seasonRow(s.CSRFToken(r), id)
	if err != nil {
		row = seasonRowView{ID: id}
	}
	s.renderSeasonPartial(w, "season_row_error", http.StatusBadRequest,
		seasonRowError{Season: row, Message: cause.Error()})
}
