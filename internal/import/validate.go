package importer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
)

// Team is one team registered in the target competition (the store's identity
// for it; extracted into this package so the importer stays store-independent).
type Team struct {
	ID          int64
	DisplayName string
}

// maxSuggestions is how many near-miss names a row error offers (spec §54).
const maxSuggestions = 3

// ResolvedRow is a parsed row after team-name resolution.
type ResolvedRow struct {
	Row

	HomeTeamID   int64
	AwayTeamID   int64
	HomeTeamName string // official registered name, or the supplied text when unresolved
	AwayTeamName string

	HomeSuggestions []string
	AwaySuggestions []string

	Errors []RowError
}

// Valid reports whether the row can be imported as-is (no errors at all).
func (r ResolvedRow) Valid() bool { return len(r.Errors) == 0 }

// needsHome/AwayPick report whether the admin must choose a team for that side.
func (r ResolvedRow) NeedsHomePick() bool { return r.HomeTeamID == 0 }
func (r ResolvedRow) NeedsAwayPick() bool { return r.AwayTeamID == 0 }

// Validation is the full preview: every row plus every error, parse errors
// included, so the UI can show one honest list.
type Validation struct {
	Rows   []ResolvedRow
	Errors []RowError
}

// ValidCount is how many rows would import successfully right now.
func (v Validation) ValidCount() int {
	n := 0
	for _, r := range v.Rows {
		if r.Valid() {
			n++
		}
	}
	return n
}

// ErrorCount is how many rows still carry at least one error.
func (v Validation) ErrorCount() int {
	n := 0
	for _, r := range v.Rows {
		if !r.Valid() {
			n++
		}
	}
	return n
}

// Total is the row count in the preview.
func (v Validation) Total() int { return len(v.Rows) }

// Validate resolves every row's team names against the competition's registered
// teams and attaches Persian errors. Matching is exact after whitespace
// trimming (D7); anything else becomes an error with advisory suggestions.
//
// parseErrors are the file-level/row-level problems Parse already found, carried
// through so the preview shows them alongside resolution errors.
func Validate(rows []Row, teams []Team, parseErrors []RowError) Validation {
	ix := newTeamIndex(teams)
	v := Validation{Errors: append([]RowError{}, parseErrors...)}

	// Duplicate detection inside the uploaded file: the same (week, home, away)
	// twice is a data-entry error, and importing it would trip the store's
	// duplicate-fixture constraint anyway.
	seen := map[string]int{}

	for _, row := range rows {
		rr := ResolvedRow{Row: row}

		homeID, homeName, homeErrs, homeSug := ix.resolve(row.Home, "home")
		awayID, awayName, awayErrs, awaySug := ix.resolve(row.Away, "away")
		// Resolution runs per side, so its errors carry no line yet.
		homeErrs = stampLine(homeErrs, row.Line)
		awayErrs = stampLine(awayErrs, row.Line)
		rr.HomeTeamID, rr.AwayTeamID = homeID, awayID
		rr.HomeTeamName, rr.AwayTeamName = homeName, awayName
		rr.HomeSuggestions, rr.AwaySuggestions = homeSug, awaySug
		rr.Errors = append(rr.Errors, homeErrs...)
		rr.Errors = append(rr.Errors, awayErrs...)

		// A team cannot play itself (store CHECK backs this up).
		if row.Home != "" && row.Away != "" {
			sameTeam := homeID != 0 && homeID == awayID
			sameRaw := homeID == 0 && awayID == 0 && equalFoldTrim(row.Home, row.Away)
			if sameTeam || sameRaw {
				rr.Errors = append(rr.Errors, rowErr(row.Line, "home",
					"یک تیم نمی‌تواند با خودش بازی کند."))
			}
		}

		// Duplicates within the file.
		if row.Home != "" && row.Away != "" && !isSelfMatch(rr) {
			key := duplicateKey(row, homeID, awayID)
			if firstLine, dup := seen[key]; dup {
				rr.Errors = append(rr.Errors, rowErr(row.Line, "duplicate",
					fmt.Sprintf("این مسابقه در همین فایل تکرار شده است (نخستین بار در خط %s).", faDigits(firstLine))))
			} else {
				seen[key] = row.Line
			}
		}

		for _, e := range rr.Errors {
			v.Errors = append(v.Errors, e)
		}
		v.Rows = append(v.Rows, rr)
	}

	sort.SliceStable(v.Errors, func(i, j int) bool { return v.Errors[i].Line < v.Errors[j].Line })
	return v
}

// isSelfMatch reports whether the row is already flagged as a self-match.
func isSelfMatch(rr ResolvedRow) bool {
	if rr.HomeTeamID != 0 && rr.HomeTeamID == rr.AwayTeamID {
		return true
	}
	return rr.HomeTeamID == 0 && rr.AwayTeamID == 0 && equalFoldTrim(rr.Home, rr.Away)
}

// duplicateKey identifies a fixture inside the uploaded file. Team ids are used
// when both sides resolved; otherwise the official-name text is used, so an
// unresolved pair still cannot be imported twice.
func duplicateKey(row Row, homeID, awayID int64) string {
	week := 0
	if row.Week != nil {
		week = *row.Week
	}
	home, away := strings.TrimSpace(row.Home), strings.TrimSpace(row.Away)
	if homeID != 0 {
		home = fmt.Sprintf("#%d", homeID)
	}
	if awayID != 0 {
		away = fmt.Sprintf("#%d", awayID)
	}
	return fmt.Sprintf("%d|%s|%s", week, home, away)
}

// ---------- team index ----------

// teamIndex resolves official team names. Exact (trimmed) matches win; a name
// shared by several registered teams is ambiguous and must be picked by hand.
type teamIndex struct {
	byName map[string][]Team
	all    []Team
}

func newTeamIndex(teams []Team) teamIndex {
	ix := teamIndex{byName: map[string][]Team{}, all: append([]Team{}, teams...)}
	for _, t := range teams {
		key := strings.TrimSpace(t.DisplayName)
		ix.byName[key] = append(ix.byName[key], t)
	}
	sort.SliceStable(ix.all, func(i, j int) bool { return ix.all[i].DisplayName < ix.all[j].DisplayName })
	return ix
}

// resolve maps one supplied name onto a registered team.
func (ix teamIndex) resolve(raw, field string) (id int64, name string, errs []RowError, suggestions []string) {
	raw = strings.TrimSpace(raw)
	name = raw
	if raw == "" {
		return 0, raw, append(errs, rowErr(0, field, "نام تیم خالی است.")), nil
	}

	switch matches := ix.byName[raw]; len(matches) {
	case 1:
		return matches[0].ID, matches[0].DisplayName, nil, nil
	case 0:
		// No exact match: advisory suggestions only. The name is never corrected.
		sug := ix.nearest(raw)
		msg := fmt.Sprintf("تیم «%s» در این مسابقات ثبت‌نام نشده است.", raw)
		return 0, raw, append(errs, rowErr(0, field, msg)), sug
	default:
		return 0, raw, append(errs, rowErr(0, field,
			fmt.Sprintf("نام «%s» بین چند تیم مشترک است؛ لطفاً از فهرست انتخاب کنید.", raw))), nil
	}
}

// nearest returns up to maxSuggestions registered names closest to raw,
// compared on a normalised form so orthographic variants still match usefully.
func (ix teamIndex) nearest(raw string) []string {
	target := normalizeForCompare(raw)
	type scored struct {
		name     string
		distance int
	}
	scoredAll := make([]scored, 0, len(ix.all))
	for _, t := range ix.all {
		scoredAll = append(scoredAll, scored{name: t.DisplayName, distance: editDistance(target, normalizeForCompare(t.DisplayName))})
	}
	sort.SliceStable(scoredAll, func(i, j int) bool {
		if scoredAll[i].distance != scoredAll[j].distance {
			return scoredAll[i].distance < scoredAll[j].distance
		}
		return scoredAll[i].name < scoredAll[j].name
	})
	if len(scoredAll) > maxSuggestions {
		scoredAll = scoredAll[:maxSuggestions]
	}
	out := make([]string, 0, len(scoredAll))
	for _, s := range scoredAll {
		out = append(out, s.name)
	}
	return out
}

// ---------- small helpers ----------

// normalizeForCompare folds only orthographic variance (Arabic yeh/kaf, ZWNJ,
// digits, whitespace) so similarity scoring is forgiving. It is NEVER used to
// decide an import — only to rank suggestions (D7).
func normalizeForCompare(s string) string {
	s = toLatinDigits(s)
	replacer := strings.NewReplacer(
		"ي", "ی", "ك", "ک", "ۀ", "ه", "ة", "ه",
		"\u200c", "", "\u200f", "", "\u200e", "",
	)
	s = replacer.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// editDistance is the Levenshtein distance over runes.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// equalFoldTrim compares two names ignoring case and surrounding whitespace.
func equalFoldTrim(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// stampLine attaches the physical line number to errors produced without one.
func stampLine(errs []RowError, line int) []RowError {
	for i := range errs {
		if errs[i].Line == 0 {
			errs[i].Line = line
		}
	}
	return errs
}

// faDigits renders a small integer with Persian digits for UI messages. The
// conversion goes through internal/jalali — the single place digits and dates
// are rendered (D9).
func faDigits(n int) string {
	return jalali.ToPersianDigits(strconv.Itoa(n))
}
