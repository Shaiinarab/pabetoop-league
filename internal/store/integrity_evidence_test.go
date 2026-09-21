// TASK-032 — evidence tests that close three gaps TESTING.md §5.3 records
// against itself. Each test is written so it fails when its enforcement
// mechanism is neutered (the mutation proofs live in TASK-032's report), not
// merely so that it passes today.
//
// Companion helpers (newTestStore, seedLeague, strPtr, intPtr) live in
// store_test.go — same package, do not redefine them.
package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/standing"
)

// TestFinishedMatchesExcludesScheduledMatches pins rule 9's finished-only
// filter (TESTING.md §5.3: dropping `status = 'finished'` from FinishedMatches
// left TestStandingsRecompute green because that fixture holds only finished
// matches).
//
// The finished slice is the sole input to internal/standing.Compute behind the
// public standings page, so the assertion is deliberately made on the standings
// output the operator reads — not only on the slice. A third team whose only
// fixture is the scheduled one carries the "unplayed" signal: it must stay at
// played == 0.
func TestFinishedMatchesExcludesScheduledMatches(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))

	// Team C has exactly one fixture, and it is the scheduled one, so its
	// standings row must remain untouched by FinishedMatches.
	clubC, _ := s.CreateClub("نمونه ت")
	teamC, _ := s.CreateTeam(clubC, "۱", "نمونه ت ۱")
	if _, err := s.Register(compID, teamC); err != nil {
		t.Fatalf("register C: %v", err)
	}

	// Finished fixture: A 2-0 B (week 1).
	finishedID, err := s.CreateMatch(compID, teamA, teamB, intPtr(1), nil, nil, nil)
	if err != nil {
		t.Fatalf("create finished fixture: %v", err)
	}
	if err := s.SetResult(finishedID, 2, 0); err != nil {
		t.Fatalf("set result: %v", err)
	}

	// Scheduled fixture between a different pairing (C vs A), no result. Week 2
	// is deliberately distinct: two equal non-NULL weeks in one competition are
	// a duplicate fixture and the 0001 partial unique index (rule 5) would
	// refuse the insert, failing this test for the wrong reason.
	if _, err := s.CreateMatch(compID, teamC, teamA, intPtr(2), nil, nil, nil); err != nil {
		t.Fatalf("create scheduled fixture: %v", err)
	}

	fin, err := s.FinishedMatches(compID)
	if err != nil {
		t.Fatalf("FinishedMatches: %v", err)
	}
	if len(fin) != 1 {
		t.Fatalf("FinishedMatches = %d matches (%+v), want exactly 1 (the finished pairing)", len(fin), fin)
	}
	if fin[0].HomeTeamID != teamA || fin[0].AwayTeamID != teamB {
		t.Fatalf("FinishedMatches[0] = %d vs %d, want the finished pairing %d vs %d",
			fin[0].HomeTeamID, fin[0].AwayTeamID, teamA, teamB)
	}
	for _, f := range fin {
		if (f.HomeTeamID == teamC && f.AwayTeamID == teamA) ||
			(f.HomeTeamID == teamA && f.AwayTeamID == teamC) {
			t.Fatalf("scheduled pairing (%d vs %d) leaked into FinishedMatches: %+v",
				teamC, teamA, fin)
		}
	}

	// Build the standings exactly the way the store contract feeds them:
	// registrations as teams, FinishedMatches as the match input.
	regs, err := s.Registrations(compID)
	if err != nil {
		t.Fatalf("Registrations: %v", err)
	}
	teams := make([]standing.TeamInput, 0, len(regs))
	for _, r := range regs {
		teams = append(teams, standing.TeamInput{ID: r.TeamID, Name: r.TeamName})
	}
	matches := make([]standing.MatchInput, 0, len(fin))
	for _, f := range fin {
		matches = append(matches, standing.MatchInput{
			HomeTeamID: f.HomeTeamID,
			AwayTeamID: f.AwayTeamID,
			HomeGoals:  f.HomeGoals,
			AwayGoals:  f.AwayGoals,
		})
	}
	rows := standing.Compute(teams, matches)

	var teamCRows []standing.Row
	for _, r := range rows {
		if r.TeamID == teamC {
			teamCRows = append(teamCRows, r)
		}
	}
	if len(teamCRows) != 1 {
		t.Fatalf("standings rows for team C = %d, want 1; full=%+v", len(teamCRows), rows)
	}
	if teamCRows[0].Played != 0 {
		t.Fatalf("team C played = %d, want 0: the scheduled fixture leaked into the standings table",
			teamCRows[0].Played)
	}
}

// TestMatchTableCheckRefusesInconsistentScoreState isolates rule 4's raw table
// CHECK (TESTING.md §5.3: the scheduled-with-a-score pairing was verified only
// by the manual sqlite3 scenario in DATABASE.md; the Go tests asserted the
// store's refusal, never the table's).
//
// It drives raw SQL on s.db on purpose — SetResult refuses before SQLite sees
// anything, so the store path cannot exercise the DDL constraint. Case 3 is the
// positive control: a single UPDATE that flips status and sets both scores must
// succeed, otherwise cases 1–2 could be passing because of some other
// constraint rather than the score/status pairing CHECK.
func TestMatchTableCheckRefusesInconsistentScoreState(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))

	matchID, err := s.CreateMatch(compID, teamA, teamB, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create scheduled match: %v", err)
	}

	// 1) A score on a still-scheduled row must be refused by the table CHECK.
	_, err = s.db.Exec(`UPDATE matches SET home_score = 3 WHERE id = ?`, matchID)
	if err == nil {
		t.Fatalf("raw UPDATE set home_score on a scheduled row, want the matches CHECK to refuse it")
	}
	if !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("scheduled-with-a-score error = %v, want it to name CHECK", err)
	}

	// 2) finished with both scores left NULL must be refused too, so the CHECK
	//    bites in both directions.
	_, err = s.db.Exec(`UPDATE matches SET status = 'finished' WHERE id = ?`, matchID)
	if err == nil {
		t.Fatalf("raw UPDATE set status='finished' with NULL scores, want the matches CHECK to refuse it")
	}
	if !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("finished-without-scores error = %v, want it to name CHECK", err)
	}

	// 3) Positive control: status + both scores in ONE UPDATE is legal.
	if _, err := s.db.Exec(
		`UPDATE matches SET status = 'finished', home_score = 1, away_score = 0 WHERE id = ?`,
		matchID,
	); err != nil {
		t.Fatalf("legal finished UPDATE refused: %v", err)
	}
}

// TestRegisterDuplicateRefusedByTheStorePreCheck asserts rule 2's store
// duplicate pre-check in isolation (TESTING.md §5.3: it was exercised only
// through TestCompetitionRegisterDuplicateRefused, the web handler test).
//
// What this deliberately does NOT prove: that the DB
// `UNIQUE (competition_id, team_id)` backstop also bites. §5.3 records that
// swapping the UNIQUE left the handler test green because the store pre-check
// refuses first; this test asserts only the guard that bites in production and
// makes no mutation claim about the backstop.
func TestRegisterDuplicateRefusedByTheStorePreCheck(t *testing.T) {
	s := newTestStore(t)
	compID, _, _ := seedLeague(t, s, "league1", strPtr("A"))

	// A fresh team so the two Register calls below are unambiguously the first
	// and the duplicate.
	clubC, _ := s.CreateClub("نمونه ت")
	teamC, _ := s.CreateTeam(clubC, "۱", "نمونه ت ۱")

	if _, err := s.Register(compID, teamC); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := s.Register(compID, teamC); !errors.Is(err, ErrInUse) {
		t.Fatalf("second Register = %v, want ErrInUse from the store pre-check", err)
	}
}
