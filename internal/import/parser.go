// Package importer parses and validates external fixture lists for the
// administrator's import pipeline (TASK-012; PROJECT_SPEC §10/§11, §43, §54).
//
// The directory is internal/import as the brief specifies, but the package
// cannot be *named* "import" — that is a Go keyword — so it declares `importer`.
//
// Pipeline:
//
//	Parse(data)                -> rows + row-numbered parse errors
//	Validate(rows, teams)      -> resolved rows + Persian errors + suggestions
//	internal/web/import_handlers.go stashes the result behind a one-shot token
//	and imports only after the administrator explicitly confirms.
//
// D7 discipline (official names are authority-controlled): matching is EXACT
// after whitespace trimming only. Looser normalisation (Arabic yeh/kaf forms,
// ZWNJ, digits) exists solely to produce advisory suggestions that the
// administrator resolves by hand — never to rewrite or merge a name.
//
// Fixtures are never generated here or anywhere else in the product (D8); this
// package only understands lists the administrator supplies.
package importer

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
)

// isoLayout is the canonical machine date format (D9).
const isoLayout = "2006-01-02"

// RowError is one problem tied to a physical line of the uploaded file, so the
// UI can render «خط ۱۲: …». Line 0 means the problem is file-wide.
type RowError struct {
	Line    int
	Field   string
	Message string
}

// Row is one parsed fixture line. Team names keep their official bytes exactly
// as supplied (D7); only surrounding whitespace is trimmed.
type Row struct {
	Line    int
	Week    *int
	Date    string // canonical ISO Gregorian; "" when absent or invalid
	DateRaw string // what the admin typed, echoed back on error
	Time    string // «HH:MM» or ""
	Home    string
	Away    string
	Venue   string

	HomeScore *int
	AwayScore *int
}

// Result is Parse's output: the rows it could understand plus every problem it
// found. Rows with fatal problems are reported in Errors and omitted from Rows.
type Result struct {
	Rows   []Row
	Errors []RowError
}

// Headerless files are mapped by column count, because a 4-column file cannot
// be told apart from an 8-column one by position alone. Both shapes are
// documented on the import page:
//
//	4 columns  -> week, date, home, away               (the common case)
//	5+ columns -> week, date, time, home, away, venue, home_score, away_score
//
// A header row is always preferred: it is self-describing and order-independent.
var (
	positionalFull    = []string{"week", "date", "time", "home", "away", "venue", "home_score", "away_score"}
	positionalCompact = []string{"week", "date", "home", "away"}
)

// headerAliasGroups lists the accepted header spellings per logical field. They
// are normalised at init (spaces/ZWNJ stripped, lowercased), which means a
// repeated spelling collapses to one key instead of becoming a duplicate map
// literal — and only the plainly spaced Persian forms need to be written here.
var headerAliasGroups = []struct {
	field   string
	aliases []string
}{
	{"week", []string{"هفته", "دوره", "week"}},
	{"date", []string{"تاریخ", "روز", "date"}},
	{"time", []string{"ساعت", "زمان", "time"}},
	{"home", []string{"میزبان", "تیم میزبان", "home"}},
	{"away", []string{"مهمان", "تیم مهمان", "away"}},
	{"venue", []string{"زمین", "مکان", "ورزشگاه", "venue"}},
	{"home_score", []string{"گل میزبان", "امتیاز میزبان", "homescore"}},
	{"away_score", []string{"گل مهمان", "امتیاز مهمان", "awayscore"}},
}

// headerAliases is the normalised lookup used by columnMap.
var headerAliases = buildHeaderAliases()

func buildHeaderAliases() map[string]string {
	const total = 24
	m := make(map[string]string, total)
	for _, group := range headerAliasGroups {
		for _, alias := range group.aliases {
			m[normalizeForHeader(alias)] = group.field
		}
	}
	return m
}

// delimiters are tried in this order when sniffing the separator.
var delimiters = []rune{',', ';', '\t', '|', '،'}

// Parse reads a fixture list. Both CSV and simple structured text are accepted;
// the separator is sniffed, a header row is optional (recognised by name), and
// Persian or Latin digits are accepted for every number.
func Parse(data []byte) Result {
	res := Result{Rows: []Row{}}

	records, err := readRecords(data)
	if err != nil {
		res.Errors = append(res.Errors, RowError{Message: "خواندن فایل ممکن نشد؛ قالب فایل پشتیبانی نمی‌شود."})
		return res
	}
	if len(records) == 0 {
		res.Errors = append(res.Errors, RowError{Message: "فایل خالی است؛ هیچ سطری برای خواندن پیدا نشد."})
		return res
	}

	// Skip leading blank lines and comments before looking for the header: a
	// «# ...» note on line 1 is the common way these lists arrive, and treating
	// it as the header candidate mis-read the real header as a data row.
	first := -1
	for i, rec := range records {
		if rec.blank() || rec.isComment() {
			continue
		}
		first = i
		break
	}
	if first == -1 {
		res.Errors = append(res.Errors, RowError{Message: "فایل خالی است؛ هیچ سطری برای خواندن پیدا نشد."})
		return res
	}

	fieldFor, headerRecords := columnMap(records[first].cells)
	if fieldFor == nil {
		// No header: fall back to the documented positional order, chosen by the
		// first data row's width.
		fieldFor = positionalMapFor(len(records[first].cells))
		headerRecords = 0
	}

	for _, rec := range records[first+headerRecords:] {
		if rec.blank() || rec.isComment() {
			continue
		}
		row, rowErrs := buildRow(rec, fieldFor)
		res.Errors = append(res.Errors, rowErrs...)
		if hasFatal(rowErrs) {
			continue
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// record is one CSV record with its physical line number.
type record struct {
	line  int
	cells []string
}

func (r record) blank() bool {
	for _, c := range r.cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// isComment reports whether the first cell marks the line as a comment.
func (r record) isComment() bool {
	return len(r.cells) > 0 && strings.HasPrefix(strings.TrimSpace(r.cells[0]), "#")
}

// readRecords sniffs the delimiter and parses the file with encoding/csv
// (stdlib; quoted fields and embedded separators are handled for free).
func readRecords(data []byte) ([]record, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	comma := detectDelimiter(text)

	r := csv.NewReader(strings.NewReader(text))
	r.Comma = comma
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1 // ragged rows are the admin's problem, reported per row

	var out []record
	for {
		cells, err := r.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// A read error ends the file; rows already read are still useful.
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		// FieldPos gives the physical line of the record's first field, so line
		// numbers stay honest even with quoted multi-line fields.
		line, _ := r.FieldPos(0)
		out = append(out, record{line: line, cells: cells})
	}
	return out, nil
}

// detectDelimiter picks the candidate separator that appears most often in the
// first few non-blank lines (headers included).
func detectDelimiter(text string) rune {
	scanner := bufio.NewScanner(strings.NewReader(text))
	counts := map[rune]int{}
	examined := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, d := range delimiters {
			counts[d] += strings.Count(line, string(d))
		}
		examined++
		if examined >= 5 {
			break
		}
	}
	best, bestCount := ',', 0
	for _, d := range delimiters {
		if counts[d] > bestCount {
			best, bestCount = d, counts[d]
		}
	}
	return best
}

// columnMap recognises a header row. It returns the field-per-column mapping and
// the index of the first data record (0 when there is no header).
func columnMap(cells []string) (map[int]string, int) {
	normalised := make([]string, len(cells))
	recognised := 0
	for i, c := range cells {
		normalised[i] = normalizeForHeader(c)
		if _, ok := headerAliases[normalised[i]]; ok {
			recognised++
		}
	}
	// Require at least two recognised columns: one accidental match (e.g. a team
	// literally called «میزبان») must not turn a data row into a header.
	if recognised < 2 {
		return nil, 0
	}
	mapping := map[int]string{}
	for i, n := range normalised {
		if field, ok := headerAliases[n]; ok {
			mapping[i] = field
		}
	}
	return mapping, 1
}

// positionalMapFor is the no-header mapping for a row of the given width.
func positionalMapFor(cells int) map[int]string {
	order := positionalFull
	if cells <= len(positionalCompact) {
		order = positionalCompact
	}
	m := make(map[int]string, len(order))
	for i, f := range order {
		m[i] = f
	}
	return m
}

// buildRow converts one record into a Row plus everything wrong with it.
func buildRow(rec record, fieldFor map[int]string) (Row, []RowError) {
	row := Row{Line: rec.line}
	var errs []RowError

	get := func(field string) (string, bool) {
		for i, f := range fieldFor {
			if f != field {
				continue
			}
			if i < len(rec.cells) {
				return strings.TrimSpace(rec.cells[i]), true
			}
			return "", true
		}
		return "", false
	}

	if raw, ok := get("week"); ok && raw != "" {
		if n, err := strconv.Atoi(toLatinDigits(raw)); err == nil && n > 0 {
			row.Week = &n
		} else {
			errs = append(errs, rowErr(rec.line, "week", fmt.Sprintf("شمارهٔ هفته نامعتبر است: «%s»", raw)))
		}
	}

	if raw, ok := get("date"); ok {
		row.DateRaw = raw
		if raw == "" {
			errs = append(errs, rowErr(rec.line, "date", "تاریخ مسابقه خالی است (نمونه: ۱۴۰۵/۰۷/۲۰)."))
		} else if iso, ok := parseFlexibleDate(raw); ok {
			row.Date = iso
		} else {
			errs = append(errs, rowErr(rec.line, "date", fmt.Sprintf("تاریخ نامعتبر است: «%s» (نمونه: ۱۴۰۵/۰۷/۲۰).", raw)))
		}
	} else {
		errs = append(errs, rowErr(rec.line, "date", "ستون تاریخ در فایل پیدا نشد."))
	}

	if raw, ok := get("time"); ok && raw != "" {
		if t, ok := parseClock(raw); ok {
			row.Time = t
		} else {
			errs = append(errs, rowErr(rec.line, "time", fmt.Sprintf("ساعت نامعتبر است: «%s» (نمونه: ۱۶:۳۰).", raw)))
		}
	}

	if raw, ok := get("home"); ok {
		row.Home = raw
	}
	if raw, ok := get("away"); ok {
		row.Away = raw
	}
	if row.Home == "" {
		errs = append(errs, rowErr(rec.line, "home", "نام تیم میزبان خالی است."))
	}
	if row.Away == "" {
		errs = append(errs, rowErr(rec.line, "away", "نام تیم مهمان خالی است."))
	}

	if raw, ok := get("venue"); ok {
		row.Venue = raw
	}

	homeScoreRaw, homeHas := get("home_score")
	awayScoreRaw, awayHas := get("away_score")
	homeScoreRaw, awayScoreRaw = strings.TrimSpace(homeScoreRaw), strings.TrimSpace(awayScoreRaw)
	if homeHas && awayHas && (homeScoreRaw != "" || awayScoreRaw != "") {
		if homeScoreRaw == "" || awayScoreRaw == "" {
			errs = append(errs, rowErr(rec.line, "score", "برای مسابقهٔ انجام‌شده هر دو نتیجه لازم است؛ یکی از نتیجه‌ها خالی است."))
		} else {
			h, okH := parseGoals(homeScoreRaw)
			a, okA := parseGoals(awayScoreRaw)
			if !okH || !okA {
				errs = append(errs, rowErr(rec.line, "score", "نتیجه باید عددی بین ۰ تا ۹۹ باشد."))
			} else {
				row.HomeScore, row.AwayScore = &h, &a
			}
		}
	}
	return row, errs
}

// hasFatal reports whether any error makes the row impossible to import. When a
// field is fatal the row is dropped from Rows and appears only in Errors.
func hasFatal(errs []RowError) bool {
	switch len(errs) {
	case 0:
		return false
	}
	for _, e := range errs {
		switch e.Field {
		case "date", "home", "away", "score", "time", "week":
			return true
		}
	}
	return false
}

func rowErr(line int, field, msg string) RowError {
	return RowError{Line: line, Field: field, Message: msg}
}

// parseFlexibleDate accepts ISO «2026-10-12» or Jalali «۱۴۰۵/۰۷/۲۰» (Persian or
// Latin digits, «/» or «-») and returns the canonical ISO Gregorian date.
func parseFlexibleDate(raw string) (string, bool) {
	s := strings.TrimSpace(toLatinDigits(raw))
	if t, err := time.Parse(isoLayout, s); err == nil {
		return t.Format(isoLayout), true
	}
	if t, err := jalali.Parse(raw); err == nil {
		return jalali.FormatISO(t), true
	}
	return "", false
}

// parseClock accepts «HH:MM» (Persian or Latin digits) and normalises to a
// zero-padded Latin «HH:MM». Times are plain text in the schema (spec A4).
func parseClock(raw string) (string, bool) {
	s := strings.TrimSpace(toLatinDigits(raw))
	s = strings.ReplaceAll(s, ".", ":")
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return "", false
	}
	h, errH := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, errM := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

// parseGoals parses one team's goals (0..99).
func parseGoals(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(toLatinDigits(raw)))
	if err != nil || n < 0 || n > 99 {
		return 0, false
	}
	return n, true
}

// toLatinDigits maps Persian (۰-۹) and Arabic-Indic (٠-٩) digits to ASCII.
// Unlike the web layer's strict parser this never rejects: the original string
// is validated by the caller afterwards.
func toLatinDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeForHeader lowercases and strips spaces/ZWNJ so «تیم میزبان» and
// «تیم‌میزبان» compare equal.
func normalizeForHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(" ", "", "\u200c", "", "\u200f", "", "\u200e", "", "_", "")
	return replacer.Replace(s)
}
