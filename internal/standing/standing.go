// Package standing computes league tables from finished matches.
//
// The engine is deterministic: the same competition + registrations + finished
// matches always produce exactly the same table (PROJECT_SPEC §53). Business
// rules (points, tie-break order) are isolated here — nowhere else in the
// codebase may ranking logic be duplicated (PROJECT_SPEC §52).
//
// Tie-break order (current official rule, §14):
//
//	Points → Goal Difference → Goals For → Persian alphabetical fallback
//
// The away-goal question (§15) is intentionally unresolved: home/away goals are
// preserved separately in the domain; when an official rule is known, it slots
// into Compare/rowOrder without touching any other package.
package standing

import (
	"sort"
	"strings"
	"unicode"
)

// TeamInput identifies a team participating in the competition.
type TeamInput struct {
	ID   int64
	Name string // official name, Persian
}

// MatchInput is one FINISHED match. Scores must be non-negative.
type MatchInput struct {
	HomeTeamID int64
	AwayTeamID int64
	HomeGoals  int
	AwayGoals  int
}

// Row is one computed standings row.
type Row struct {
	Rank         int
	TeamID       int64
	TeamName     string
	Played       int
	Won          int
	Drawn        int
	Lost         int
	GoalsFor     int
	GoalsAgainst int
	GoalDiff     int
	Points       int
}

// Compute builds the full standings table for the given teams and finished matches.
// Teams with no matches appear with zeroed stats. Unknown team IDs in matches
// are ignored (callers validate against registrations before persisting).
func Compute(teams []TeamInput, matches []MatchInput) []Row {
	stats := make(map[int64]*Row, len(teams))
	for _, t := range teams {
		stats[t.ID] = &Row{TeamID: t.ID, TeamName: t.Name}
	}
	for _, m := range matches {
		home, okH := stats[m.HomeTeamID]
		away, okA := stats[m.AwayTeamID]
		if !okH || !okA || home == away {
			continue
		}
		home.Played++
		away.Played++
		home.GoalsFor += m.HomeGoals
		home.GoalsAgainst += m.AwayGoals
		away.GoalsFor += m.AwayGoals
		away.GoalsAgainst += m.HomeGoals
		switch {
		case m.HomeGoals > m.AwayGoals:
			home.Won++
			home.Points += 3
			away.Lost++
		case m.HomeGoals < m.AwayGoals:
			away.Won++
			away.Points += 3
			home.Lost++
		default:
			home.Drawn++
			away.Drawn++
			home.Points++
			away.Points++
		}
	}
	rows := make([]Row, 0, len(stats))
	for _, r := range stats {
		r.GoalDiff = r.GoalsFor - r.GoalsAgainst
		rows = append(rows, *r)
	}
	SortRows(rows)
	for i := range rows {
		rows[i].Rank = i + 1
	}
	return rows
}

// SortRows orders rows by the official rules. Isolated so a future rule change
// (e.g. an away-goal rule, head-to-head) modifies exactly one function.
func SortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rowLess(rows[i], rows[j])
	})
}

// rowLess is the single authority for row ordering (PROJECT_SPEC §14):
// Points → Goal Difference → Goals For → Persian alphabetical.
func rowLess(a, b Row) bool {
	if a.Points != b.Points {
		return a.Points > b.Points
	}
	if a.GoalDiff != b.GoalDiff {
		return a.GoalDiff > b.GoalDiff
	}
	if a.GoalsFor != b.GoalsFor {
		return a.GoalsFor > b.GoalsFor
	}
	return PersianLess(a.TeamName, b.TeamName)
}

// persianNormalize prepares a Persian team name for deterministic ordering:
// unified Arabic/Persian letter variants, ZWNJ and diacritics removed,
// Arabic-Indic/Persian digits unified, whitespace collapsed.
// Normalization affects ordering ONLY — never displayed or stored names (D7).
func persianNormalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\u200c', '\u200d', '\u200e', '\u200f': // ZWNJ, ZWJ, LRM, RLM
			continue
		case 'ي':
			r = 'ی'
		case 'ك':
			r = 'ک'
		case 'ۀ':
			r = 'ه'
		case 'أ', 'إ', 'آ':
			r = 'ا'
		case 'ؤ':
			r = 'و'
		case '٠', '۰':
			r = '0'
		case '١', '۱':
			r = '1'
		case '٢', '۲':
			r = '2'
		case '٣', '۳':
			r = '3'
		case '٤', '۴':
			r = '4'
		case '٥', '۵':
			r = '5'
		case '٦', '۶':
			r = '6'
		case '٧', '۷':
			r = '7'
		case '٨', '۸':
			r = '8'
		case '٩', '۹':
			r = '9'
		}
		if unicode.Is(unicode.Mn, r) { // Arabic diacritics
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PersianLess orders two Persian names by code point after normalization,
// which matches the practical Persian alphabetical ordering used in
// sports tables (letters sort before punctuation; variant letters unify).
func PersianLess(a, b string) bool {
	return persianNormalize(a) < persianNormalize(b)
}
