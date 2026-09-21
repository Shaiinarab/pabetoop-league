package standing

import (
	"reflect"
	"testing"
)

func team(id int64, name string) TeamInput { return TeamInput{ID: id, Name: name} }

func TestBasicWinDrawLoss(t *testing.T) {
	teams := []TeamInput{team(1, "الف"), team(2, "ب"), team(3, "پ")}
	matches := []MatchInput{
		{1, 2, 2, 0}, // 1 beats 2
		{1, 3, 1, 1}, // draw
		{2, 3, 0, 3}, // 3 beats 2
	}
	rows := Compute(teams, matches)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	byID := map[int64]Row{}
	for _, r := range rows {
		byID[r.TeamID] = r
	}
	// Team 1: W1 D1 → 4 pts, GF3 GA1
	if r := byID[1]; r.Points != 4 || r.Won != 1 || r.Drawn != 1 || r.Played != 2 || r.GoalsFor != 3 || r.GoalsAgainst != 1 || r.GoalDiff != 2 {
		t.Errorf("team1 = %+v", r)
	}
	// Team 2: L2 → 0 pts, GF0 GA5
	if r := byID[2]; r.Points != 0 || r.Lost != 2 || r.GoalsFor != 0 || r.GoalsAgainst != 5 || r.GoalDiff != -5 {
		t.Errorf("team2 = %+v", r)
	}
	// Team 3: W1 D1 → 4 pts, GF4 GA1
	if r := byID[3]; r.Points != 4 || r.GoalsFor != 4 || r.GoalDiff != 3 {
		t.Errorf("team3 = %+v", r)
	}
	// Ranking: team3 first (4pts, GD+3, GF4), then team1 (4pts, GD+2), then team2
	if rows[0].TeamID != 3 || rows[1].TeamID != 1 || rows[2].TeamID != 2 {
		t.Errorf("rank order wrong: %+v", rows)
	}
	if rows[0].Rank != 1 || rows[1].Rank != 2 || rows[2].Rank != 3 {
		t.Errorf("ranks not assigned: %+v", rows)
	}
}

func TestTieBreakGoalDiffThenGoalsFor(t *testing.T) {
	teams := []TeamInput{team(1, "الف"), team(2, "ب"), team(3, "پ"), team(4, "ت")}
	matches := []MatchInput{
		// everyone beats team 4 differently — equal points, distinct GD/GF
		{1, 4, 3, 0}, // 1: GF3 GD+3
		{2, 4, 3, 1}, // 2: GF3 GD+2
		{3, 4, 2, 0}, // 3: GF2 GD+2
	}
	rows := Compute(teams, matches)
	got := []int64{}
	for _, r := range rows {
		got = append(got, r.TeamID)
	}
	want := []int64{1, 2, 3, 4} // 3pts+3GD > 3pts+2GD&GF3 > 3pts+2GD&GF2 > 0pts
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestTieBreakPersianAlphabetical(t *testing.T) {
	// Identical records → name decides. نمونه ب < نمونه ب نوین after normalization.
	teams := []TeamInput{team(2, "نمونه ب نوین"), team(1, "نمونه ب")}
	matches := []MatchInput{{1, 2, 1, 1}} // draw, identical totals
	rows := Compute(teams, matches)
	if rows[0].TeamID != 1 || rows[1].TeamID != 2 {
		t.Errorf("alphabetical fallback wrong: %+v", rows)
	}
}

func TestZeroMatchesAllTeamsPresent(t *testing.T) {
	teams := []TeamInput{team(1, "الف"), team(2, "ب")}
	rows := Compute(teams, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Played != 0 || r.Points != 0 || r.Rank == 0 {
			t.Errorf("zeroed row wrong: %+v", r)
		}
	}
}

func TestUnknownTeamIgnored(t *testing.T) {
	teams := []TeamInput{team(1, "الف")}
	rows := Compute(teams, []MatchInput{{1, 99, 2, 1}})
	if len(rows) != 1 || rows[0].Played != 0 {
		t.Errorf("unknown opponent must be ignored: %+v", rows)
	}
}

func TestDeterminism(t *testing.T) {
	teams := []TeamInput{team(1, "س"), team(2, "ش"), team(3, "ص"), team(4, "ض")}
	matches := []MatchInput{
		{1, 2, 2, 1}, {3, 4, 0, 0}, {2, 3, 1, 1}, {4, 1, 2, 2},
		{1, 3, 1, 0}, {2, 4, 3, 3},
	}
	first := Compute(teams, matches)
	for i := 0; i < 20; i++ {
		// shuffle match order; result must be identical
		rotated := append(append([]MatchInput{}, matches[3:]...), matches[:3]...)
		got := Compute(teams, rotated)
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("non-deterministic standings at iteration %d", i)
		}
	}
}

func TestPersianNormalizeVariants(t *testing.T) {
	// Arabic ي/ك must equal Persian ی/ک for ordering
	if !PersianLess("نمونه ب", "نمونه ب نوین") {
		t.Error("base ordering broken")
	}
	if PersianLess("كیان", "کیان") {
		t.Error("Arabic keheh must equal Persian keheh")
	}
	if PersianLess("پيام", "پیام") {
		t.Error("Arabic yeh must equal Persian yeh")
	}
	// ب vs پ are genuinely different letters: ب sorts before پ
	if !PersianLess("بيام", "پیام") {
		t.Error("be must sort before pe")
	}
	// ZWNJ must not affect order
	if PersianLess("می‌گوید", "میگوید") {
		t.Error("ZWNJ should be transparent for ordering")
	}
}
