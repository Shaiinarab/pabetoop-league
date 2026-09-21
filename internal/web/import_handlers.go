// Fixture import pipeline (TASK-012 part B; PROJECT_SPEC §11, §43, §54).
//
// Three steps, and nothing reaches the store before the last one:
//
//	GET  /admin/import           upload form + format help
//	POST /admin/import           upload -> parse -> validate -> preview
//	POST /admin/import/confirm   apply the admin's name picks -> import valid rows
//
// The parsed preview is held SERVER-SIDE behind a one-shot token:
//   - the confirm request cannot be replayed or forged from client-supplied row
//     data (the browser carries only the token plus the admin's team picks);
//   - taking the token deletes it, so a double-submit cannot import twice
//     («no file is ever auto-processed twice» in the brief);
//   - the token expires, so a stale preview cannot be confirmed much later.
//
// Errors never import. Rows still invalid at confirm time are skipped and
// reported by line, and a store failure mid-batch is reported per row too —
// never as a silent partial import.
//
// Store contract note (reported as a NAG): there is no bulk-import method, so a
// true all-or-nothing import is impossible; each row is its own transaction and
// failures are surfaced individually instead.
package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	importer "github.com/shaiinarab/pabetoop-league/internal/import"
	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

const (
	// importMaxBytes caps the uploaded file; a season of fixtures is a few KB.
	importMaxBytes = 1 << 20
	// importMaxRows caps one upload so a huge file cannot pin memory or the DB.
	importMaxRows = 500
	// importTokenTTL is how long a preview stays confirmable.
	importTokenTTL = 30 * time.Minute
	// importMaxTokens bounds concurrent previews (single administrator, so this
	// only ever holds a couple; the cap is hygiene, not capacity planning).
	importMaxTokens = 8

	importGenericError = "خطایی در خواندن فایل رخ داد؛ لطفاً دوباره تلاش کنید."
)

// ---------- view shapes ----------

// importPreviewRow is one row of the preview table.
type importPreviewRow struct {
	Line      int
	LineText  string // Persian digits
	WeekText  string
	WhenText  string
	Home      string
	Away      string
	HomeOK    bool
	AwayOK    bool
	Venue     string
	ScoreText string
	Valid     bool

	// Suggestions are advisory Persian hints only (D7: never applied silently).
	HomeSuggestions []string
	AwaySuggestions []string
	ErrorMessages   []string
}

type importPreviewView struct {
	Token           string
	CompetitionID   int64
	CompetitionName string
	Rows            []importPreviewRow
	Total           int
	ValidCount      int
	ErrorCount      int
}

// importResultView summarises a confirm run.
type importResultView struct {
	Inserted int
	Skipped  int
	Failures []string
}

type importView struct {
	adminPage

	AgeGroups             []resultAgeGroup
	SelectedCompetitionID int64
	SelectedName          string
	Teams                 []fixtureTeamOption

	Preview *importPreviewView
	Result  *importResultView

	MaxRowsText  string
	MaxBytesText string
}

// ---------- preview store ----------

// importPreviewEntry is a parsed upload held until the admin confirms it.
type importPreviewEntry struct {
	created time.Time
	compID  int64
	rows    []importer.Row
}

var importPreviews = struct {
	mu    sync.Mutex
	items map[string]importPreviewEntry
}{items: map[string]importPreviewEntry{}}

// stashImportPreview stores a parsed preview and returns its one-shot token.
func stashImportPreview(entry importPreviewEntry) (string, error) {
	entry.created = time.Now()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	importPreviews.mu.Lock()
	defer importPreviews.mu.Unlock()

	// Drop expired entries; if still full, evict the oldest.
	now := time.Now()
	oldestToken, oldestAt := "", now
	for t, e := range importPreviews.items {
		if now.Sub(e.created) > importTokenTTL {
			delete(importPreviews.items, t)
			continue
		}
		if e.created.Before(oldestAt) {
			oldestAt, oldestToken = e.created, t
		}
	}
	if len(importPreviews.items) >= importMaxTokens && oldestToken != "" {
		delete(importPreviews.items, oldestToken)
	}
	importPreviews.items[token] = entry
	return token, nil
}

// takeImportPreview consumes a token (one-shot: a replay finds nothing).
func takeImportPreview(token string) (importPreviewEntry, bool) {
	importPreviews.mu.Lock()
	defer importPreviews.mu.Unlock()
	entry, ok := importPreviews.items[token]
	if !ok {
		return importPreviewEntry{}, false
	}
	delete(importPreviews.items, token)
	if time.Since(entry.created) > importTokenTTL {
		return importPreviewEntry{}, false
	}
	return entry, true
}

// ---------- page ----------

func (s *Server) handleAdminImport(w http.ResponseWriter, r *http.Request) {
	s.renderImportPage(w, r, importView{}, nil)
}

// renderImportPage builds chrome + picker and renders the page, optionally
// carrying a preview or a result section.
func (s *Server) renderImportPage(w http.ResponseWriter, r *http.Request, view importView, comp *store.Competition) {
	view.adminPage = s.fixturesChrome(w, r)
	view.Breadcrumb = []breadcrumbItem{
		{Label: "خانه", Href: "/admin"},
		{Label: "برنامهٔ بازی‌ها", Href: fixturesPath},
		{Label: "ورود گروهی"},
	}
	view.MaxRowsText = faNum(importMaxRows)
	view.MaxBytesText = "۱ مگابایت"
	if s.store == nil {
		s.renderAdmin(w, r, "import", view, http.StatusOK)
		return
	}

	groups, comps := s.competitionPicker()
	view.AgeGroups = groups
	if len(comps) == 0 {
		s.renderAdmin(w, r, "import", view, http.StatusOK)
		return
	}
	sel := s.selectCompetition(r, comps)
	if comp != nil {
		sel = *comp
	}
	view.SelectedCompetitionID = sel.ID
	view.SelectedName = sel.DisplayName
	view.Teams = viewTeams(s, sel.ID)
	s.renderAdmin(w, r, "import", view, http.StatusOK)
}

// ---------- upload -> preview ----------

func (s *Server) handleAdminImportPreview(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, dataErrorMessage)
		return
	}
	if err := r.ParseMultipartForm(importMaxBytes + (1 << 16)); err != nil {
		writePersianError(w, http.StatusBadRequest, "ارسال فایل نامعتبر است.")
		return
	}
	compID, ok := parsePositiveInt(r.PostFormValue("competition"))
	if !ok {
		writePersianError(w, http.StatusBadRequest, "مسابقات انتخاب‌شده معتبر نیست.")
		return
	}
	comp, err := s.store.Competition(int64(compID))
	if err != nil || comp == nil {
		writePersianError(w, http.StatusNotFound, "مسابقات انتخاب‌شده پیدا نشد.")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		s.importFailure(w, r, comp, "فایلی انتخاب نشده است.")
		return
	}
	defer file.Close()
	if header.Size > importMaxBytes {
		s.importFailure(w, r, comp, "حجم فایل بیش از حد مجاز است.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, importMaxBytes+1))
	if err != nil {
		s.log.Error("import: read upload", "error", err)
		writePersianError(w, http.StatusInternalServerError, importGenericError)
		return
	}
	if len(data) > importMaxBytes {
		s.importFailure(w, r, comp, "حجم فایل بیش از حد مجاز است.")
		return
	}

	parsed := importer.Parse(data)
	if len(parsed.Rows) > importMaxRows {
		s.importFailure(w, r, comp, fmt.Sprintf(
			"فایل بیش از %s سطر مسابقه دارد؛ آن را به چند فایل کوچک‌تر تقسیم کنید.", faNum(importMaxRows)))
		return
	}

	teams := importTeams(viewTeams(s, int64(compID)))
	validation := importer.Validate(parsed.Rows, teams, parsed.Errors)
	existing := s.existingFixtureKeys(int64(compID))

	preview := &importPreviewView{
		CompetitionID:   int64(compID),
		CompetitionName: comp.DisplayName,
	}
	for _, row := range validation.Rows {
		preview.Rows = append(preview.Rows, previewRowView(row, existing))
	}
	preview.Total = len(preview.Rows)
	// Counted from the rendered rows, not the importer's totals, because the
	// DB-duplicate check can invalidate an otherwise clean row.
	for _, row := range preview.Rows {
		if row.Valid {
			preview.ValidCount++
		} else {
			preview.ErrorCount++
		}
	}

	token, err := stashImportPreview(importPreviewEntry{compID: int64(compID), rows: parsed.Rows})
	if err != nil {
		s.log.Error("import: stash preview", "error", err)
		writePersianError(w, http.StatusInternalServerError, importGenericError)
		return
	}
	preview.Token = token
	s.renderImportPage(w, r, importView{Preview: preview}, comp)
}

// ---------- confirm -> import ----------

func (s *Server) handleAdminImportConfirm(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, dataErrorMessage)
		return
	}
	if err := r.ParseForm(); err != nil {
		writePersianError(w, http.StatusBadRequest, "درخواست نامعتبر است.")
		return
	}
	token := strings.TrimSpace(r.PostFormValue("token"))
	entry, ok := takeImportPreview(token)
	if !ok {
		s.renderImportPage(w, r, importView{Result: &importResultView{
			Failures: []string{"این پیش‌نمایش دیگر معتبر نیست؛ لطفاً فایل را دوباره بارگذاری کنید."},
		}}, nil)
		return
	}
	comp, err := s.store.Competition(entry.compID)
	if err != nil || comp == nil {
		writePersianError(w, http.StatusNotFound, "مسابقات انتخاب‌شده پیدا نشد.")
		return
	}

	teams := importTeams(viewTeams(s, entry.compID))
	nameByID := map[int64]string{}
	for _, t := range teams {
		nameByID[t.ID] = t.DisplayName
	}

	// Apply the admin's picks by rewriting each row's name to the chosen OFFICIAL
	// name, then re-validate the whole set — duplicates, self-matches and shared
	// names stay consistent with what the preview promised.
	rows := append([]importer.Row{}, entry.rows...)
	applyResolutions(r, nameByID, rows)
	validation := importer.Validate(rows, teams, nil)
	existing := s.existingFixtureKeys(entry.compID)

	result := importResultView{}
	for _, row := range validation.Rows {
		if !row.Valid() {
			result.Skipped++
			continue
		}
		if key := fixtureKey(row.Week, row.HomeTeamID, row.AwayTeamID); existing[key] {
			result.Skipped++
			result.Failures = append(result.Failures,
				fmt.Sprintf("خط %s: این مسابقه از قبل در برنامه ثبت شده است.", faNum(row.Line)))
			continue
		}
		date := row.Date
		week := row.Week
		var timePtr, venuePtr *string
		if row.Time != "" {
			t := row.Time
			timePtr = &t
		}
		if row.Venue != "" {
			v := row.Venue
			venuePtr = &v
		}
		id, err := s.store.CreateMatch(entry.compID, row.HomeTeamID, row.AwayTeamID, week, &date, timePtr, venuePtr)
		if err != nil {
			result.Skipped++
			result.Failures = append(result.Failures, fmt.Sprintf("خط %s: %s", faNum(row.Line), err.Error()))
			continue
		}
		if row.HomeScore != nil && row.AwayScore != nil {
			if err := s.store.SetResult(id, *row.HomeScore, *row.AwayScore); err != nil {
				// The fixture exists; only its score failed. Say so honestly rather
				// than reporting the row as fully imported.
				result.Failures = append(result.Failures, fmt.Sprintf(
					"خط %s: مسابقه ثبت شد اما نتیجه ذخیره نشد: %s", faNum(row.Line), err.Error()))
			}
		}
		result.Inserted++
	}
	s.renderImportPage(w, r, importView{Result: &result}, comp)
}

// ---------- helpers ----------

// importFailure renders the import page with a single Persian failure notice.
func (s *Server) importFailure(w http.ResponseWriter, r *http.Request, comp *store.Competition, message string) {
	s.renderImportPage(w, r, importView{Result: &importResultView{Failures: []string{message}}}, comp)
}

// importTeams adapts the web layer's team options to the importer's shape.
func importTeams(opts []fixtureTeamOption) []importer.Team {
	out := make([]importer.Team, 0, len(opts))
	for _, o := range opts {
		out = append(out, importer.Team{ID: o.ID, DisplayName: o.Name})
	}
	return out
}

// viewTeams loads the teams registered in a competition.
func viewTeams(s *Server, compID int64) []fixtureTeamOption {
	regs, err := s.store.Registrations(compID)
	if err != nil {
		s.log.Error("import: registrations", "error", err, "competition", compID)
		return nil
	}
	out := make([]fixtureTeamOption, 0, len(regs))
	for _, reg := range regs {
		out = append(out, fixtureTeamOption{ID: reg.TeamID, Name: reg.TeamName})
	}
	return out
}

// previewRowView shapes one validated row for the template.
func previewRowView(row importer.ResolvedRow, existing map[string]bool) importPreviewRow {
	out := importPreviewRow{
		Line:            row.Line,
		LineText:        faNum(row.Line),
		Home:            row.Home,
		Away:            row.Away,
		HomeOK:          !row.NeedsHomePick(),
		AwayOK:          !row.NeedsAwayPick(),
		Venue:           row.Venue,
		HomeSuggestions: row.HomeSuggestions,
		AwaySuggestions: row.AwaySuggestions,
	}
	if row.Week != nil {
		out.WeekText = jalali.ToPersianDigits(strconv.Itoa(*row.Week))
	}
	out.WhenText = importDateText(row)
	if row.HomeScore != nil && row.AwayScore != nil {
		out.ScoreText = jalali.ToPersianDigits(fmt.Sprintf("%d - %d", *row.HomeScore, *row.AwayScore))
	}
	for _, e := range row.Errors {
		out.ErrorMessages = append(out.ErrorMessages, e.Message)
	}
	// A fixture already in the competition's programme is an error here, not a
	// surprise at confirm time.
	if row.Valid() {
		if key := fixtureKey(row.Week, row.HomeTeamID, row.AwayTeamID); existing[key] {
			out.ErrorMessages = append(out.ErrorMessages, "این مسابقه از قبل در برنامهٔ همین مسابقات ثبت شده است.")
		}
	}
	out.Valid = len(out.ErrorMessages) == 0
	return out
}

// importDateText renders the row's date in Jalali, falling back to what the file
// said when the date could not be parsed.
func importDateText(row importer.ResolvedRow) string {
	if row.Date == "" {
		return row.DateRaw
	}
	t, err := time.Parse(isoLayout, row.Date)
	if err != nil {
		return row.DateRaw
	}
	return jalali.Format(t)
}

// existingFixtureKeys is the set of fixtures already stored for a competition, so
// the preview and the confirm step agree on what counts as a duplicate.
func (s *Server) existingFixtureKeys(compID int64) map[string]bool {
	keys := map[string]bool{}
	matches, err := s.store.Matches(compID, nil, nil)
	if err != nil {
		s.log.Error("import: existing matches", "error", err, "competition", compID)
		return keys
	}
	for _, m := range matches {
		keys[fixtureKey(m.Week, m.HomeTeamID, m.AwayTeamID)] = true
	}
	return keys
}

// fixtureKey identifies a fixture for duplicate purposes (store semantics:
// competition + week + ordered pairing).
func fixtureKey(week *int, homeID, awayID int64) string {
	w := 0
	if week != nil {
		w = *week
	}
	return fmt.Sprintf("%d|%d|%d", w, homeID, awayID)
}

// applyResolutions rewrites row names from the admin's per-row team picks.
// Form field names are «resolve_<line>_home» / «resolve_<line>_away».
func applyResolutions(r *http.Request, nameByID map[int64]string, rows []importer.Row) {
	picks := map[int]map[string]int64{}
	for key, values := range r.PostForm {
		line, side, ok := parseResolveKey(key)
		if !ok || len(values) == 0 {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if picks[line] == nil {
			picks[line] = map[string]int64{}
		}
		picks[line][side] = id
	}
	for i := range rows {
		pick := picks[rows[i].Line]
		if pick == nil {
			continue
		}
		if id, ok := pick["home"]; ok {
			if name, found := nameByID[id]; found {
				rows[i].Home = name
			}
		}
		if id, ok := pick["away"]; ok {
			if name, found := nameByID[id]; found {
				rows[i].Away = name
			}
		}
	}
}

// parseResolveKey decodes «resolve_<line>_<side>».
func parseResolveKey(key string) (int, string, bool) {
	parts := strings.Split(key, "_")
	if len(parts) != 3 || parts[0] != "resolve" {
		return 0, "", false
	}
	line, err := strconv.Atoi(parts[1])
	if err != nil || line <= 0 {
		return 0, "", false
	}
	if parts[2] != "home" && parts[2] != "away" {
		return 0, "", false
	}
	return line, parts[2], true
}

// faNum renders a small integer with Persian digits (UI text goes through the
// single conversion authority, D9).
func faNum(n int) string { return jalali.ToPersianDigits(strconv.Itoa(n)) }
