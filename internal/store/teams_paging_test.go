// TASK-025 — store-side paged team list (TeamsPage).
package store

import (
	"fmt"
	"testing"
)

// seedManyTeams creates clubs×perClub teams and returns them exactly as
// Teams(nil) orders them, which is the sequence TeamsPage must reproduce.
func seedManyTeams(t *testing.T, s *Store, clubs, perClub int) []Team {
	t.Helper()
	for i := 0; i < clubs; i++ {
		clubID, err := s.CreateClub(fmt.Sprintf("باشگاه %02d", i))
		if err != nil {
			t.Fatalf("club %d: %v", i, err)
		}
		for j := 0; j < perClub; j++ {
			if _, err := s.CreateTeam(clubID, fmt.Sprintf("t%02d", j), fmt.Sprintf("تیم %02d-%02d", i, j)); err != nil {
				t.Fatalf("team %d/%d: %v", i, j, err)
			}
		}
	}
	all, err := s.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	return all
}

// TestTeamsPageWalk covers the whole ordered set a page at a time: every team
// appears on exactly one page, in Teams(nil)'s order, and the total never moves.
func TestTeamsPageWalk(t *testing.T) {
	s := newTestStore(t)
	const clubs, perClub = 12, 10 // 120 teams → 3 pages of 50/50/20
	all := seedManyTeams(t, s, clubs, perClub)
	if len(all) != clubs*perClub {
		t.Fatalf("seeded %d teams, want %d", len(all), clubs*perClub)
	}

	walked := []int64{}
	total := -1
	for offset := 0; ; offset += teamsPageDefaultLimit {
		page, n, err := s.TeamsPage(nil, teamsPageDefaultLimit, offset)
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if total == -1 {
			total = n
		} else if n != total {
			t.Errorf("total changed across pages: %d then %d", total, n)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > teamsPageDefaultLimit {
			t.Fatalf("page at offset %d returned %d rows, want ≤%d", offset, len(page), teamsPageDefaultLimit)
		}
		for _, tm := range page {
			walked = append(walked, tm.ID)
		}
	}

	if total != len(all) {
		t.Errorf("TeamsPage total = %d, want %d", total, len(all))
	}
	if len(walked) != len(all) {
		t.Fatalf("walk returned %d rows, want %d", len(walked), len(all))
	}
	for i := range all {
		if walked[i] != all[i].ID {
			t.Fatalf("row %d: walk id %d != Teams(nil) id %d (a row was dropped, duplicated, or reordered)", i, walked[i], all[i].ID)
		}
	}
}

// TestTeamsPageClampsFilterPastEnd covers the documented clamps, the club
// filter, and an offset past the end.
func TestTeamsPageClampsFilterPastEnd(t *testing.T) {
	s := newTestStore(t)
	const clubs, perClub = 52, 10 // 520 > teamsPageMaxLimit, so both clamps are observable
	all := seedManyTeams(t, s, clubs, perClub)

	def, total, err := s.TeamsPage(nil, 0, 0)
	if err != nil {
		t.Fatalf("limit=0: %v", err)
	}
	if len(def) != teamsPageDefaultLimit || total != len(all) {
		t.Fatalf("limit=0 returned %d rows (total %d), want %d", len(def), total, teamsPageDefaultLimit)
	}

	huge, hugeTotal, err := s.TeamsPage(nil, 10000, 0)
	if err != nil {
		t.Fatalf("limit=10000: %v", err)
	}
	if len(huge) != teamsPageMaxLimit || hugeTotal != len(all) {
		t.Fatalf("limit=10000 returned %d rows (total %d), want %d", len(huge), hugeTotal, teamsPageMaxLimit)
	}

	negative, negTotal, err := s.TeamsPage(nil, 5, -5)
	if err != nil {
		t.Fatalf("offset=-5: %v", err)
	}
	first, firstTotal, err := s.TeamsPage(nil, 5, 0)
	if err != nil {
		t.Fatalf("offset=0: %v", err)
	}
	if negTotal != firstTotal || len(negative) != len(first) {
		t.Fatalf("offset=-5 returned %d rows (total %d), want %d (%d)", len(negative), negTotal, len(first), firstTotal)
	}
	for i := range first {
		if negative[i].ID != first[i].ID {
			t.Fatalf("offset=-5 row %d = id %d, want id %d (negative offset must mean the first page)", i, negative[i].ID, first[i].ID)
		}
	}

	past, pastTotal, err := s.TeamsPage(nil, teamsPageDefaultLimit, 5000)
	if err != nil {
		t.Fatalf("past end: %v", err)
	}
	if len(past) != 0 || pastTotal != len(all) {
		t.Fatalf("past-end page returned %d rows (total %d), want 0 (%d)", len(past), pastTotal, len(all))
	}

	clubsList, err := s.Clubs(false)
	if err != nil || len(clubsList) == 0 {
		t.Fatalf("clubs: %v", err)
	}
	clubID := clubsList[0].ID
	filtered, filteredTotal, err := s.TeamsPage(&clubID, teamsPageDefaultLimit, 0)
	if err != nil {
		t.Fatalf("filtered page: %v", err)
	}
	if filteredTotal != perClub || len(filtered) != perClub {
		t.Fatalf("filtered page returned %d rows (total %d), want %d (the shared WHERE clause must filter both reads)", len(filtered), filteredTotal, perClub)
	}
	for _, tm := range filtered {
		if tm.ClubID != clubID {
			t.Errorf("filtered page leaked club %d, want %d", tm.ClubID, clubID)
		}
	}
}
