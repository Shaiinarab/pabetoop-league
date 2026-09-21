// Property-based and adversarial tests for the standing engine (TASK-006).
// Conservation invariants, rank-permutation, ordering axioms, determinism
// under shuffling, and extreme scores. The engine is NEVER modified here —
// a violated invariant is reported, not patched.
package standing

import (
	"math/rand"
	"reflect"
	"testing"
)

// reproducible seasons; change intentionally only with Lead sign-off.
const standingRandSeed = int64(1405)

type propSeason struct {
	teamIDs []int64
	names   map[int64]string
	matches []MatchInput
}

func genSeason(rnd *rand.Rand) propSeason {
	n := 3 + rnd.Intn(18) // 3..20 teams
	ps := propSeason{
		teamIDs: make([]int64, n),
		names:   make(map[int64]string, n),
	}
	for i := range ps.teamIDs {
		id := int64(100 + i)
		ps.teamIDs[i] = id
		ps.names[id] = teamName(rnd.Intn(6))
	}
	m := rnd.Intn(n*(n-1)/2 + 1) // up to full single round-robin count
	ps.matches = make([]MatchInput, 0, m)
	for k := 0; k < m; k++ {
		h := rnd.Intn(n)
		a := rnd.Intn(n)
		if h == a {
			continue // skip self-matches (inputs are filtered/valid by contract)
		}
		ps.matches = append(ps.matches, MatchInput{
			HomeTeamID: ps.teamIDs[h],
			AwayTeamID: ps.teamIDs[a],
			HomeGoals:  rnd.Intn(8), // 0..7
			AwayGoals:  rnd.Intn(8),
		})
	}
	return ps
}

// teamName produces a small Persian name pool to exercise the alpha fallback.
func teamName(i int) string {
	names := []string{"نمونه آ", "نمونه ب", "نمونه ث", "نمونه ت", "نمونه ج", "هما"}
	return names[i%len(names)]
}

func computeFromSeason(ps propSeason) []Row {
	teams := make([]TeamInput, 0, len(ps.teamIDs))
	for _, id := range ps.teamIDs {
		teams = append(teams, TeamInput{ID: id, Name: ps.names[id]})
	}
	return Compute(teams, ps.matches)
}

// 1 — conservation: ΣWon == ΣLost, ΣPoints == 3ΣWon + ΣDrawn, ΣGF == ΣGA,
// ΣPlayed == 2·(counted matches).
func TestPropertyStandingConservation(t *testing.T) {
	rnd := rand.New(rand.NewSource(standingRandSeed))
	for season := 0; season < 50; season++ {
		ps := genSeason(rnd)
		rows := computeFromSeason(ps)

		var won, lost, drawn, pts, gf, ga, played int64
		for _, r := range rows {
			won += int64(r.Won)
			lost += int64(r.Lost)
			drawn += int64(r.Drawn)
			pts += int64(r.Points)
			gf += int64(r.GoalsFor)
			ga += int64(r.GoalsAgainst)
			played += int64(r.Played)
		}
		if won != lost {
			t.Fatalf("season %d: ΣWon(%d) != ΣLost(%d)", season, won, lost)
		}
		if pts != 3*won+drawn {
			t.Fatalf("season %d: ΣPoints(%d) != 3·ΣWon(%d)+ΣDrawn(%d)", season, pts, won, drawn)
		}
		if gf != ga {
			t.Fatalf("season %d: ΣGF(%d) != ΣGA(%d)", season, gf, ga)
		}
		if played != 2*int64(len(ps.matches)) {
			t.Fatalf("season %d: ΣPlayed(%d) != 2·%d", season, played, len(ps.matches))
		}
		// each team's Played == Won+Drawn+Lost
		for _, r := range rows {
			if r.Played != r.Won+r.Drawn+r.Lost {
				t.Fatalf("season %d team %d: Played(%d) != W+D+L(%d)", season, r.TeamID, r.Played, r.Won+r.Drawn+r.Lost)
			}
			if r.GoalDiff != r.GoalsFor-r.GoalsAgainst {
				t.Fatalf("season %d team %d: GD mismatch", season, r.TeamID)
			}
		}
	}
}

// 2 — ranks form exactly the permutation 1..N.
func TestPropertyRanksArePermutation(t *testing.T) {
	rnd := rand.New(rand.NewSource(standingRandSeed + 1))
	for season := 0; season < 50; season++ {
		ps := genSeason(rnd)
		rows := computeFromSeason(ps)
		seen := make(map[int]bool, len(rows))
		for i, r := range rows {
			if r.Rank != i+1 {
				t.Fatalf("season %d: row %d has rank %d (ranks must be dense 1..N)", season, i, r.Rank)
			}
			if seen[r.Rank] {
				t.Fatalf("season %d: duplicate rank %d", season, r.Rank)
			}
			seen[r.Rank] = true
		}
		if len(rows) != len(ps.teamIDs) {
			t.Fatalf("season %d: rows = %d, want %d", season, len(rows), len(ps.teamIDs))
		}
	}
}

// 3 — ordering axiom: for every adjacent pair, rowLess semantics hold.
func TestPropertyOrderingAxiom(t *testing.T) {
	rnd := rand.New(rand.NewSource(standingRandSeed + 2))
	for season := 0; season < 50; season++ {
		ps := genSeason(rnd)
		rows := computeFromSeason(ps)
		for i := 0; i+1 < len(rows); i++ {
			a, b := rows[i], rows[i+1]
			if rowLess(b, a) {
				t.Fatalf("season %d: adjacent rows violate order: %v should come after %v", season, b, a)
			}
			if !rowLess(a, b) && a.TeamID != b.TeamID {
				// rowLess(a,b) false and rowLess(b,a) false ⇒ full tie on all
				// keys; names must then be Persian-equal.
				if a.Points == b.Points && a.GoalDiff == b.GoalDiff &&
					a.GoalsFor == b.GoalsFor && PersianLess(a.TeamName, b.TeamName) {
					t.Fatalf("season %d: PersianLess tie broken wrongly: %q vs %q", season, a.TeamName, b.TeamName)
				}
			}
		}
	}

	// Hand-built direct check of the four-level key via exported SortRows.
	hand := []Row{
		{Rank: 0, TeamID: 1, TeamName: "الف", Points: 9, GoalDiff: 2, GoalsFor: 5},
		{Rank: 0, TeamID: 2, TeamName: "ب", Points: 9, GoalDiff: 2, GoalsFor: 6},   // GF higher → first
		{Rank: 0, TeamID: 3, TeamName: "ج", Points: 9, GoalDiff: 3, GoalsFor: 1},   // GD higher → first
		{Rank: 0, TeamID: 4, TeamName: "د", Points: 10, GoalDiff: -5, GoalsFor: 0}, // pts higher → first
		{Rank: 0, TeamID: 5, TeamName: "ه", Points: 9, GoalDiff: 2, GoalsFor: 5},   // full tie w/ ID1 → alpha
		{Rank: 0, TeamID: 6, TeamName: "ا", Points: 9, GoalDiff: 2, GoalsFor: 5},   // alpha before «الف»
	}
	SortRows(hand)
	// Axiom order: Points → GD → GF → alpha. Team 3 (GD 3) precedes team 2
	// (GD 2, GF 6): GD outranks GF. Full-tie block sorts by normalized name:
	// «ا" (ا) < «الف" (اﻟﻓ: second letter ل 0x644) < «ه" (ه 0x647).
	wantOrder := []int64{4, 3, 2, 6, 1, 5}
	for i, w := range wantOrder {
		if hand[i].TeamID != w {
			t.Fatalf("hand ordering: pos %d = team %d, want %d (rows=%+v)", i, hand[i].TeamID, w, hand)
		}
	}
}

// 4 — determinism: 30 shuffles of the same input → identical tables.
func TestPropertyDeterminismUnderShuffle(t *testing.T) {
	rnd := rand.New(rand.NewSource(standingRandSeed + 3))
	ps := genSeason(rnd)
	teams := make([]TeamInput, 0, len(ps.teamIDs))
	for _, id := range ps.teamIDs {
		teams = append(teams, TeamInput{ID: id, Name: ps.names[id]})
	}
	ref := Compute(teams, ps.matches)
	for shuffle := 0; shuffle < 30; shuffle++ {
		rnd.Shuffle(len(ps.matches), func(i, j int) {
			ps.matches[i], ps.matches[j] = ps.matches[j], ps.matches[i]
		})
		rnd.Shuffle(len(teams), func(i, j int) {
			teams[i], teams[j] = teams[j], teams[i]
		})
		got := Compute(teams, ps.matches)
		if !reflect.DeepEqual(ref, got) {
			t.Fatalf("shuffle %d: table differs\nref=%+v\ngot=%+v", shuffle, ref, got)
		}
	}
}

// 5 — extreme scores: 0-0, 15-0, 0-15, up to 99 — no overflow/panic, GD right.
func TestPropertyExtremeScores(t *testing.T) {
	teams := []TeamInput{
		{ID: 1, Name: "الف"}, {ID: 2, Name: "ب"}, {ID: 3, Name: "ج"}, {ID: 4, Name: "د"},
	}
	matches := []MatchInput{
		{HomeTeamID: 1, AwayTeamID: 2, HomeGoals: 0, AwayGoals: 0},
		{HomeTeamID: 3, AwayTeamID: 4, HomeGoals: 15, AwayGoals: 0},
		{HomeTeamID: 3, AwayTeamID: 2, HomeGoals: 0, AwayGoals: 15},
		{HomeTeamID: 4, AwayTeamID: 1, HomeGoals: 99, AwayGoals: 99},
	}
	rows := Compute(teams, matches)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	byID := map[int64]Row{}
	for _, r := range rows {
		byID[r.TeamID] = r
	}
	// team 3: W(15-0) + L(0-15) → GF15 GA15 GD0 pts3
	if r := byID[3]; r.Points != 3 || r.GoalsFor != 15 || r.GoalsAgainst != 15 || r.GoalDiff != 0 {
		t.Fatalf("team 3 = %+v, want pts3 GF15 GA15 GD0", r)
	}
	// team 1: D(0-0) + D(99-99) → GD0 pts2 GF99
	if r := byID[1]; r.Points != 2 || r.GoalDiff != 0 || r.GoalsFor != 99 {
		t.Fatalf("team 1 = %+v, want pts2 GD0 GF99", r)
	}
	// team 4: L(0-15) + D(99-99) → pts1 GA114
	if r := byID[4]; r.Points != 1 || r.GoalsAgainst != 114 {
		t.Fatalf("team 4 = %+v, want pts1 GA114", r)
	}
	// Points ordering: team2 (4) > team3 (3) > team1 (2) > team4 (1).
	wantRanks := map[int64]int{2: 1, 3: 2, 1: 3, 4: 4}
	for id, want := range wantRanks {
		if byID[id].Rank != want {
			t.Fatalf("team %d rank = %d, want %d (rows=%+v)", id, byID[id].Rank, want, rows)
		}
	}

	// overflow-adjacent: huge goals on many matches
	rnd := rand.New(rand.NewSource(standingRandSeed + 4))
	for round := 0; round < 100; round++ {
		big := make([]MatchInput, 0, 50)
		for i := 0; i < 50; i++ {
			big = append(big, MatchInput{
				HomeTeamID: 1, AwayTeamID: 2,
				HomeGoals: rnd.Intn(100), AwayGoals: rnd.Intn(100),
			})
		}
		rows := Compute(teams[:2], big)
		if len(rows) != 2 {
			t.Fatalf("rows = %d", len(rows))
		}
		var gf, ga int64
		for _, r := range rows {
			gf += int64(r.GoalsFor)
			ga += int64(r.GoalsAgainst)
		}
		if gf != ga {
			t.Fatalf("round %d: ΣGF(%d) != ΣGA(%d)", round, gf, ga)
		}
	}
}
