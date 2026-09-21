// Task-004 test suite. Covers the 12 mandatory areas from the brief:
// migration idempotency, season exclusivity, duplicate club names (Persian
// error), team lifecycle + deactivate blocking, competition level/group
// shapes, premier-club uniqueness, match integrity, result lifecycle,
// standings recompute (via internal/standing), audit rows, unregister
// blocking, and BackupTo validity.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/standing"
)

// newTestStore opens a migrated store backed by a temp file DB and seeds
// the baseline domain (age groups 10..14, one active season).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.EnsureAgeGroups(10, 11, 12, 13, 14); err != nil {
		t.Fatalf("age groups: %v", err)
	}
	seasonID, err := s.CreateSeason("۱۴۰۵–۱۴۰۶", nil, nil)
	if err != nil {
		t.Fatalf("season: %v", err)
	}
	if err := s.ActivateSeason(seasonID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return s
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func strPtrVal(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// seedLeague returns ids for a U12 premier competition plus two clubs with
// one team each (نمونه آ ۱، نمونه ب ۱) registered in it.
func seedLeague(t *testing.T, s *Store, level string, group *string) (compID, teamA, teamB int64) {
	t.Helper()
	ags, err := s.AgeGroups()
	if err != nil || len(ags) == 0 {
		t.Fatalf("age groups: %v (%v)", ags, err)
	}
	u12 := ags[2].ID // sorted 10..14 → index 2 = 12
	season, err := s.ActiveSeason()
	if err != nil || season == nil {
		t.Fatalf("active season: %v", err)
	}
	compID, err = s.CreateCompetition(season.ID, u12, level, group, "تست")
	if err != nil {
		t.Fatalf("competition: %v", err)
	}
	clubA, err := s.CreateClub("نمونه آ")
	if err != nil {
		t.Fatalf("club A: %v", err)
	}
	clubB, err := s.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("club B: %v", err)
	}
	teamA, err = s.CreateTeam(clubA, "۱", "نمونه آ ۱")
	if err != nil {
		t.Fatalf("team A: %v", err)
	}
	teamB, err = s.CreateTeam(clubB, "۱", "نمونه ب ۱")
	if err != nil {
		t.Fatalf("team B: %v", err)
	}
	if _, err := s.Register(compID, teamA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if _, err := s.Register(compID, teamB); err != nil {
		t.Fatalf("register B: %v", err)
	}
	return compID, teamA, teamB
}

// 1 — migration idempotency across reopen.
func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if err := s1.Migrate(); err != nil {
		t.Fatalf("migrate 1: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close 1: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer s2.Close()
	if err := s2.Migrate(); err != nil {
		t.Fatalf("migrate 2 (reopen): %v", err)
	}

	// Schema objects exist exactly once.
	var n int
	if err := s2.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='seasons'`).Scan(&n); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if n != 1 {
		t.Fatalf("seasons table count = %d, want 1", n)
	}
}

// 2 — activating season B deactivates season A.
func TestSeasonActivationExclusivity(t *testing.T) {
	s := newTestStore(t)
	idA, _ := s.CreateSeason("فصل الف", nil, nil)
	idB, err := s.CreateSeason("فصل ب", nil, nil)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if err := s.ActivateSeason(idA); err != nil {
		t.Fatalf("activate A: %v", err)
	}
	if err := s.ActivateSeason(idB); err != nil {
		t.Fatalf("activate B: %v", err)
	}
	seasons, err := s.Seasons()
	if err != nil {
		t.Fatalf("seasons: %v", err)
	}
	for _, se := range seasons {
		want := se.ID == idB
		if se.IsActive != want {
			t.Fatalf("season %s active = %v, want %v", se.Name, se.IsActive, want)
		}
	}
	active, err := s.ActiveSeason()
	if err != nil || active == nil || active.ID != idB {
		t.Fatalf("active season = %+v err=%v, want id %d", active, err, idB)
	}
	if err := s.ActivateSeason(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate unknown → %v, want ErrNotFound", err)
	}
}

// 3 — duplicate club name yields the required Persian error (create + rename).
func TestClubDuplicateNamePersian(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateClub("نمونه آ"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.CreateClub("نمونه آ")
	if err == nil || !strings.Contains(err.Error(), "باشگاهی با این نام قبلاً ثبت شده است") {
		t.Fatalf("dup create → %v, want Persian duplicate error", err)
	}
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("dup create error not ErrDuplicateName: %v", err)
	}
	id, _ := s.CreateClub("نمونه ب")
	if err := s.RenameClub(id, "نمونه آ"); err == nil ||
		!strings.Contains(err.Error(), "باشگاهی با این نام قبلاً ثبت شده است") {
		t.Fatalf("dup rename → %v, want Persian duplicate error", err)
	}
}

// 4 — team CRUD; deactivate blocked while a registration exists.
func TestTeamLifecycle(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, _ := seedLeague(t, s, "league1", strPtr("A"))

	teams, err := s.Teams(nil)
	if err != nil || len(teams) != 2 {
		t.Fatalf("teams = %d (%v), want 2", len(teams), err)
	}

	// Update label/display. nil category = leave it alone (this team was created
	// by CreateTeam, which stores no category).
	if err := s.UpdateTeam(teamA, "۲", "نمونه آ ۲", nil); err != nil {
		t.Fatalf("update team: %v", err)
	}
	got, err := s.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	var found *Team
	for i := range got {
		if got[i].ID == teamA {
			found = &got[i]
		}
	}
	if found == nil || found.DisplayName != "نمونه آ ۲" || found.Label != "۲" {
		t.Fatalf("updated team = %+v", found)
	}

	// Deactivate blocked while registered.
	err = s.DeactivateTeam(teamA)
	if err == nil || !errors.Is(err, ErrActiveRefs) || !strings.Contains(err.Error(), "فعال دارد") {
		t.Fatalf("deactivate registered → %v, want Persian blocked error", err)
	}
	if !errors.Is(err, ErrActiveRefs) {
		t.Fatalf("want ErrActiveRefs, got %v", err)
	}

	// After unregister, deactivate succeeds.
	regs, err := s.Registrations(compID)
	if err != nil || len(regs) == 0 {
		t.Fatalf("registrations: %v", err)
	}
	for _, r := range regs {
		if r.TeamID == teamA {
			if err := s.Unregister(r.ID); err != nil {
				t.Fatalf("unregister: %v", err)
			}
		}
	}
	if err := s.DeactivateTeam(teamA); err != nil {
		t.Fatalf("deactivate after unregister: %v", err)
	}
	inactive, _ := s.Teams(nil)
	for _, tm := range inactive {
		if tm.ID == teamA && tm.IsActive {
			t.Fatalf("team %d still active after deactivation", teamA)
		}
	}
}

// 5 — competition level/group shape validation, both directions.
func TestCreateCompetitionShapes(t *testing.T) {
	s := newTestStore(t)
	ags, _ := s.AgeGroups()
	u12 := ags[2].ID
	season, _ := s.ActiveSeason()

	// premier + group → rejected
	if _, err := s.CreateCompetition(season.ID, u12, "premier", strPtr("A"), "x"); err == nil ||
		!strings.Contains(err.Error(), "گروه") {
		t.Fatalf("premier with group → %v, want Persian group error", err)
	}
	// league1 without group → rejected
	if _, err := s.CreateCompetition(season.ID, u12, "league1", nil, "x"); err == nil ||
		!strings.Contains(err.Error(), "گروه") {
		t.Fatalf("league1 without group → %v, want Persian group error", err)
	}
	// unknown level → rejected
	if _, err := s.CreateCompetition(season.ID, u12, "cup", nil, "x"); !errors.Is(err, ErrBadLevel) {
		t.Fatalf("bad level → %v, want ErrBadLevel", err)
	}
	// valid shapes
	if _, err := s.CreateCompetition(season.ID, u12, "premier", nil, "لیگ برتر ۱۲ سال"); err != nil {
		t.Fatalf("premier valid: %v", err)
	}
	if _, err := s.CreateCompetition(season.ID, u12, "league1", strPtr("A"), "لیگ یک A"); err != nil {
		t.Fatalf("league1 valid: %v", err)
	}
}

// 6 — premier uniqueness: one team per club per premier competition;
// different club OK; same club may field multiple teams in league1 groups.
func TestPremierUniqueness(t *testing.T) {
	s := newTestStore(t)
	ags, _ := s.AgeGroups()
	u12 := ags[2].ID
	season, _ := s.ActiveSeason()

	compID, err := s.CreateCompetition(season.ID, u12, "premier", nil, "لیگ برتر ۱۲ سال")
	if err != nil {
		t.Fatalf("premier comp: %v", err)
	}
	clubA, _ := s.CreateClub("نمونه آ")
	clubB, _ := s.CreateClub("نمونه ب")
	a1, _ := s.CreateTeam(clubA, "۱", "نمونه آ ۱")
	a2, _ := s.CreateTeam(clubA, "۲", "نمونه آ ۲")
	b1, _ := s.CreateTeam(clubB, "۱", "نمونه ب ۱")

	if _, err := s.Register(compID, a1); err != nil {
		t.Fatalf("register A1: %v", err)
	}
	_, err = s.Register(compID, a2)
	if err == nil || !strings.Contains(err.Error(), "لیگ برتر") {
		t.Fatalf("second same-club premier team → %v, want «لیگ برتر» error", err)
	}
	if !errors.Is(err, ErrPremierClubOnce) {
		t.Fatalf("want ErrPremierClubOnce, got %v", err)
	}
	if _, err := s.Register(compID, b1); err != nil {
		t.Fatalf("different club in premier must be OK: %v", err)
	}

	// league1: same club may enter several teams (different groups).
	l1a, _ := s.CreateCompetition(season.ID, u12, "league1", strPtr("A"), "لیگ یک A")
	l1b, _ := s.CreateCompetition(season.ID, u12, "league1", strPtr("B"), "لیگ یک B")
	if _, err := s.Register(l1a, a1); err != nil {
		t.Fatalf("league1 A group: %v", err)
	}
	if _, err := s.Register(l1b, a2); err != nil {
		t.Fatalf("league1 B group (same club, second team): %v", err)
	}
}

// 7 — match integrity: unregistered team, self-match, duplicate fixture, bad week.
func TestCreateMatchIntegrity(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))

	// Unregistered opponent (fresh club/team not registered).
	clubC, _ := s.CreateClub("نمونه ت")
	teamC, _ := s.CreateTeam(clubC, "۱", "نمونه ت ۱")
	_, err := s.CreateMatch(compID, teamA, teamC, intPtr(1), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ثبت‌نام") {
		t.Fatalf("unregistered team → %v, want Persian registration error", err)
	}
	if !errors.Is(err, ErrBothTeamsNeeded) {
		t.Fatalf("want ErrBothTeamsNeeded, got %v", err)
	}

	// Self match.
	if _, err := s.CreateMatch(compID, teamA, teamA, nil, nil, nil, nil); !errors.Is(err, ErrSelfMatch) {
		t.Fatalf("self match → %v, want ErrSelfMatch", err)
	}

	// Bad week.
	if _, err := s.CreateMatch(compID, teamA, teamB, intPtr(0), nil, nil, nil); !errors.Is(err, ErrBadWeek) {
		t.Fatalf("week 0 → %v, want ErrBadWeek", err)
	}

	// Valid match, then duplicate fixture (same pairing + week).
	id1, err := s.CreateMatch(compID, teamA, teamB, intPtr(1), strPtr("2026-09-20"), strPtr("10:00"), strPtr("زمین نمونه"))
	if err != nil {
		t.Fatalf("valid match: %v", err)
	}
	if _, err := s.CreateMatch(compID, teamA, teamB, intPtr(1), nil, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "تکراری") {
		t.Fatalf("duplicate fixture → %v, want duplicate error", err)
	}

	m, err := s.Match(id1)
	if err != nil {
		t.Fatalf("get match: %v", err)
	}
	if m.HomeTeamName != "نمونه آ ۱" || m.AwayTeamName != "نمونه ب ۱" || m.Status != "scheduled" {
		t.Fatalf("match = %+v", m)
	}
	if strPtrVal(m.Venue) != "زمین نمونه" || strPtrVal(m.ScheduledTime) != "10:00" {
		t.Fatalf("match meta = %+v", m)
	}
}

// 8 — result lifecycle: set → finished; negative rejected; re-set updates;
// clear → scheduled again; wrong-state errors.
func TestResultLifecycle(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))
	matchID, err := s.CreateMatch(compID, teamA, teamB, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create match: %v", err)
	}

	// Negative score rejected.
	err = s.SetResult(matchID, -1, 0)
	if err == nil || !strings.Contains(err.Error(), "نتیجه نمی‌تواند منفی باشد") {
		t.Fatalf("negative score → %v, want Persian negative error", err)
	}

	// Clear on scheduled rejected.
	if err := s.ClearResult(matchID); !errors.Is(err, ErrNotFinished) {
		t.Fatalf("clear on scheduled → %v, want ErrNotFinished", err)
	}

	// Set result.
	if err := s.SetResult(matchID, 2, 1); err != nil {
		t.Fatalf("set result: %v", err)
	}
	m, _ := s.Match(matchID)
	if m.Status != "finished" || m.HomeScore == nil || *m.HomeScore != 2 || m.AwayScore == nil || *m.AwayScore != 1 {
		t.Fatalf("after set: %+v", m)
	}
	fin, err := s.FinishedMatches(compID)
	if err != nil || len(fin) != 1 || fin[0].HomeGoals != 2 || fin[0].AwayGoals != 1 {
		t.Fatalf("finished matches = %+v (%v)", fin, err)
	}

	// Re-set on finished rejected.
	if err := s.SetResult(matchID, 3, 3); !errors.Is(err, ErrNotScheduled) {
		t.Fatalf("re-set on finished → %v, want ErrNotScheduled", err)
	}

	// Clear result → back to scheduled, scores nil.
	if err := s.ClearResult(matchID); err != nil {
		t.Fatalf("clear result: %v", err)
	}
	m, _ = s.Match(matchID)
	if m.Status != "scheduled" || m.HomeScore != nil || m.AwayScore != nil {
		t.Fatalf("after clear: %+v", m)
	}
	total, finished, err := s.MatchCount(compID)
	if err != nil || total != 1 || finished != 0 {
		t.Fatalf("match count = %d/%d (%v), want 1/0", total, finished, err)
	}
}

// 9 — standings recompute over store output (brief scenario: 4 teams,
// 5 matches incl. draw; then a goals-for tie resolved by Persian alpha).
func TestStandingsRecompute(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))

	// Two more clubs/teams registered.
	clubC, _ := s.CreateClub("نمونه ت")
	clubD, _ := s.CreateClub("نمونه ث")
	teamC, _ := s.CreateTeam(clubC, "۱", "نمونه ت ۱")
	teamD, _ := s.CreateTeam(clubD, "۱", "نمونه ث ۱")
	for _, tid := range []int64{teamC, teamD} {
		if _, err := s.Register(compID, tid); err != nil {
			t.Fatalf("register %d: %v", tid, err)
		}
	}

	// Fixtures/results:
	//   A 2-0 B    (نمونه آ ۱ vs نمونه ب ۱)
	//   C 1-1 D    (نمونه ت ۱ vs نمونه ث ۱)
	//   B 3-1 C
	//   D 0-2 A
	//   B 0-0 D
	type fixture struct {
		home, away int64
		hs, as     int
	}
	for _, f := range []fixture{
		{teamA, teamB, 2, 0},
		{teamC, teamD, 1, 1},
		{teamB, teamC, 3, 1},
		{teamD, teamA, 0, 2},
		{teamB, teamD, 0, 0},
	} {
		id, err := s.CreateMatch(compID, f.home, f.away, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("create match: %v", err)
		}
		if err := s.SetResult(id, f.hs, f.as); err != nil {
			t.Fatalf("set result: %v", err)
		}
	}

	fin, err := s.FinishedMatches(compID)
	if err != nil || len(fin) != 5 {
		t.Fatalf("finished = %d (%v), want 5", len(fin), err)
	}

	regs, err := s.Registrations(compID)
	if err != nil {
		t.Fatalf("registrations: %v", err)
	}
	teamInputs := make([]standing.TeamInput, 0, len(regs))
	for _, r := range regs {
		teamInputs = append(teamInputs, standing.TeamInput{ID: r.TeamID, Name: r.TeamName})
	}
	matchInputs := make([]standing.MatchInput, 0, len(fin))
	for _, f := range fin {
		matchInputs = append(matchInputs, standing.MatchInput{
			HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID,
			HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals,
		})
	}
	rows := standing.Compute(teamInputs, matchInputs)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	// Expected: نمونه آ ۱ (6pts) > نمونه ب ۱ (4) > نمونه ث ۱ (2) > نمونه ت ۱ (1)
	want := []struct {
		name string
		pts  int
	}{
		{"نمونه آ ۱", 6}, {"نمونه ب ۱", 4}, {"نمونه ث ۱", 2}, {"نمونه ت ۱", 1},
	}
	for i, w := range want {
		if rows[i].TeamName != w.name || rows[i].Points != w.pts {
			t.Fatalf("rank %d = %s(%d), want %s(%d); full=%+v",
				i+1, rows[i].TeamName, rows[i].Points, w.name, w.pts, rows)
		}
	}

	// GF tie → equal pts/GD/GF → Persian alphabetical fallback.
	ags2, _ := s.AgeGroups()
	season2, _ := s.ActiveSeason()
	comp2, err := s.CreateCompetition(season2.ID, ags2[2].ID, "league1", strPtr("B"), "لیگ یک B")
	if err != nil {
		t.Fatalf("league1 B: %v", err)
	}
	clubE, _ := s.CreateClub("نمونه ا")
	clubF, _ := s.CreateClub("نمونه ج")
	tmA2, _ := s.CreateTeam(clubE, "۱", "نمونه ا ۱")
	tmB2, _ := s.CreateTeam(clubF, "۱", "نمونه ج ۱")
	for _, tid := range []int64{tmA2, tmB2} {
		if _, err := s.Register(comp2, tid); err != nil {
			t.Fatalf("register comp2 %d: %v", tid, err)
		}
	}
	m1, _ := s.CreateMatch(comp2, tmA2, tmB2, nil, nil, nil, nil)
	_ = s.SetResult(m1, 1, 0)
	m2, _ := s.CreateMatch(comp2, tmB2, tmA2, nil, nil, nil, nil)
	_ = s.SetResult(m2, 2, 1)
	// Both: 3 pts, GF 2, GA 2, GD 0 → alphabetical: «نمونه ا ۱» < «نمونه ج ۱»
	fin2, _ := s.FinishedMatches(comp2)
	regs2, _ := s.Registrations(comp2)
	in2 := make([]standing.TeamInput, 0, len(regs2))
	for _, r := range regs2 {
		in2 = append(in2, standing.TeamInput{ID: r.TeamID, Name: r.TeamName})
	}
	mi2 := make([]standing.MatchInput, 0, len(fin2))
	for _, f := range fin2 {
		mi2 = append(mi2, standing.MatchInput{HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID, HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals})
	}
	rows2 := standing.Compute(in2, mi2)
	if rows2[0].TeamName != "نمونه ا ۱" || rows2[1].TeamName != "نمونه ج ۱" {
		t.Fatalf("alpha fallback order wrong: %+v", rows2)
	}
	if rows2[0].Points != 3 || rows2[1].Points != 3 || rows2[0].GoalsFor != 2 {
		t.Fatalf("GF-tie stats wrong: %+v", rows2)
	}
}

// 10 — audit rows exist for mutations (spot-check action/entity/entity_id).
func TestAuditLog(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, _ := seedLeague(t, s, "league1", strPtr("A"))
	matchID, err := s.CreateMatch(compID, teamA, teamB_of(t, s, compID), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create match: %v", err)
	}
	if err := s.SetResult(matchID, 1, 0); err != nil {
		t.Fatalf("set result: %v", err)
	}

	entries, err := s.AuditLog(50)
	if err != nil {
		t.Fatalf("audit log: %v", err)
	}
	if len(entries) < 5 {
		t.Fatalf("audit rows = %d, want ≥5 (season, activation, clubs, teams, registrations, match, result)", len(entries))
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		kinds[e.Entity+"/"+e.Action] = true
	}
	for _, want := range []string{"club/insert", "team/insert", "registration/insert", "match/insert", "match/update"} {
		if !kinds[want] {
			t.Fatalf("missing audit kind %s in %+v", want, entries)
		}
	}
	// entity_id spot-check: the result update must reference the match id.
	var resultUpdateFound bool
	for _, e := range entries {
		if e.Entity == "match" && e.Action == "update" && e.EntityID == matchID {
			resultUpdateFound = true
		}
	}
	if !resultUpdateFound {
		t.Fatalf("result update audit row for match %d not found", matchID)
	}
}

// 10b — AuditLogFiltered: exact-match filters, honest totals, page walking and
// the documented input clamps (TASK-017).
func TestAuditLogFiltered(t *testing.T) {
	s := newTestStore(t)
	const clubs = 60 // > auditFilteredDefaultLimit, so the clamps are observable
	for i := 0; i < clubs; i++ {
		if _, err := s.CreateClub(fmt.Sprintf("باشگاه %d", i)); err != nil {
			t.Fatalf("create club %d: %v", i, err)
		}
	}

	// The unfiltered total must agree with what AuditLog sees, row for row.
	all, err := s.AuditLog(1000)
	if err != nil {
		t.Fatalf("AuditLog: %v", err)
	}
	page, total, err := s.AuditLogFiltered("", "", 1000, 0)
	if err != nil {
		t.Fatalf("AuditLogFiltered (no filter): %v", err)
	}
	if total != len(all) || len(page) != len(all) {
		t.Fatalf("no-filter total/rows = %d/%d, want %d/%d", total, len(page), len(all), len(all))
	}

	// Exact match by action: every row matches, and the total equals the true count.
	inserts, insertTotal, err := s.AuditLogFiltered("insert", "", 1000, 0)
	if err != nil {
		t.Fatalf("filter action=insert: %v", err)
	}
	if insertTotal == 0 || insertTotal != len(inserts) {
		t.Fatalf("action=insert total/rows = %d/%d", insertTotal, len(inserts))
	}
	for _, e := range inserts {
		if e.Action != "insert" {
			t.Fatalf("action=insert returned %q (id %d)", e.Action, e.ID)
		}
	}
	if insertTotal >= total {
		t.Fatalf("action=insert matched %d of %d rows; the filter did nothing", insertTotal, total)
	}

	// Exact match by entity: the seeded clubs, and only the clubs we created.
	clubRows, clubTotal, err := s.AuditLogFiltered("", "club", 1000, 0)
	if err != nil {
		t.Fatalf("filter entity=club: %v", err)
	}
	if clubTotal != clubs || len(clubRows) != clubs {
		t.Fatalf("entity=club total/rows = %d/%d, want %d/%d", clubTotal, len(clubRows), clubs, clubs)
	}
	for _, e := range clubRows {
		if e.Entity != "club" {
			t.Fatalf("entity=club returned entity %q (id %d)", e.Entity, e.ID)
		}
	}

	// Combined filter: both conditions must hold.
	combined, combinedTotal, err := s.AuditLogFiltered("insert", "club", 1000, 0)
	if err != nil {
		t.Fatalf("combined filter: %v", err)
	}
	if combinedTotal != clubs || len(combined) != clubs {
		t.Fatalf("insert+club total/rows = %d/%d, want %d/%d", combinedTotal, len(combined), clubs, clubs)
	}

	// A filter that matches nothing: empty page, zero total, no error.
	none, noneTotal, err := s.AuditLogFiltered("delete", "club", 10, 0)
	if err != nil {
		t.Fatalf("no-match filter: %v", err)
	}
	if noneTotal != 0 || len(none) != 0 {
		t.Fatalf("no-match filter returned %d rows, total %d", len(none), noneTotal)
	}

	// Walk the whole log in pages of 7: no duplicates, no omissions, same order.
	seen := []int64{}
	for offset := 0; offset < total; offset += 7 {
		p, tot, err := s.AuditLogFiltered("", "", 7, offset)
		if err != nil {
			t.Fatalf("page walk at offset %d: %v", offset, err)
		}
		if tot != total {
			t.Fatalf("page walk total = %d at offset %d, want %d", tot, offset, total)
		}
		for _, e := range p {
			seen = append(seen, e.ID)
		}
	}
	if len(seen) != len(all) {
		t.Fatalf("page walk saw %d rows, want %d", len(seen), len(all))
	}
	for i := range all {
		if seen[i] != all[i].ID {
			t.Fatalf("page walk row %d = id %d, want %d (order/duplicates)", i, seen[i], all[i].ID)
		}
	}

	// Offset past the end: empty page, total still correct.
	beyond, beyondTotal, err := s.AuditLogFiltered("", "", 7, 100000)
	if err != nil {
		t.Fatalf("offset past the end: %v", err)
	}
	if len(beyond) != 0 || beyondTotal != total {
		t.Fatalf("offset past the end = %d rows, total %d; want 0 rows, total %d", len(beyond), beyondTotal, total)
	}

	// Clamps: limit<=0 → 50, limit>500 → 500, offset<0 → 0.
	defaulted, defTotal, err := s.AuditLogFiltered("", "", 0, 0)
	if err != nil {
		t.Fatalf("limit=0: %v", err)
	}
	if len(defaulted) != auditFilteredDefaultLimit || defTotal != total {
		t.Fatalf("limit=0 returned %d rows (total %d), want %d", len(defaulted), defTotal, auditFilteredDefaultLimit)
	}
	huge, hugeTotal, err := s.AuditLogFiltered("", "", 10000, 0)
	if err != nil {
		t.Fatalf("limit=10000: %v", err)
	}
	if len(huge) > auditFilteredMaxLimit || hugeTotal != total {
		t.Fatalf("limit=10000 returned %d rows (total %d), want ≤%d", len(huge), hugeTotal, auditFilteredMaxLimit)
	}
	negative, negTotal, err := s.AuditLogFiltered("", "", 5, -5)
	if err != nil {
		t.Fatalf("offset=-5: %v", err)
	}
	firstPage, firstTotal, err := s.AuditLogFiltered("", "", 5, 0)
	if err != nil {
		t.Fatalf("offset=0: %v", err)
	}
	if negTotal != firstTotal || len(negative) != len(firstPage) {
		t.Fatalf("offset=-5 gave %d rows (total %d), want the first page: %d rows (total %d)",
			len(negative), negTotal, len(firstPage), firstTotal)
	}
	for i := range firstPage {
		if negative[i].ID != firstPage[i].ID {
			t.Fatalf("offset=-5 row %d = id %d, want %d", i, negative[i].ID, firstPage[i].ID)
		}
	}

	// Determinism: repeated identical calls return the identical sequence.
	a1, t1, err := s.AuditLogFiltered("insert", "club", 10, 0)
	if err != nil {
		t.Fatalf("repeat a: %v", err)
	}
	a2, t2, err := s.AuditLogFiltered("insert", "club", 10, 0)
	if err != nil {
		t.Fatalf("repeat b: %v", err)
	}
	if t1 != t2 || len(a1) != len(a2) {
		t.Fatalf("repeated call totals = %d/%d, rows %d/%d", t1, t2, len(a1), len(a2))
	}
	for i := range a1 {
		if a1[i].ID != a2[i].ID {
			t.Fatalf("repeated call row %d = id %d then %d", i, a1[i].ID, a2[i].ID)
		}
	}
}

// teamB_of fetches the second registered team of the seeded competition.
func teamB_of(t *testing.T, s *Store, compID int64) int64 {
	t.Helper()
	regs, err := s.Registrations(compID)
	if err != nil || len(regs) < 2 {
		t.Fatalf("registrations: %v", err)
	}
	return regs[1].TeamID
}

// 11 — unregister blocked with matches, OK without.
func TestUnregisterBlocked(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))
	matchID, err := s.CreateMatch(compID, teamA, teamB, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create match: %v", err)
	}
	regs, _ := s.Registrations(compID)
	var regA int64
	for _, r := range regs {
		if r.TeamID == teamA {
			regA = r.ID
		}
	}
	err = s.Unregister(regA)
	if err == nil || !strings.Contains(err.Error(), "حذف ثبت‌نام ممکن نیست") {
		t.Fatalf("unregister with matches → %v, want Persian blocked error", err)
	}
	if !errors.Is(err, ErrHasMatches) {
		t.Fatalf("want ErrHasMatches, got %v", err)
	}
	// Remove the match, then unregister succeeds.
	if err := s.DeleteMatch(matchID); err != nil {
		t.Fatalf("delete match: %v", err)
	}
	if err := s.Unregister(regA); err != nil {
		t.Fatalf("unregister after match delete: %v", err)
	}
}

// 12 — BackupTo produces a file that opens as a valid DB with the data.
func TestBackupTo(t *testing.T) {
	s := newTestStore(t)
	season, _ := s.ActiveSeason()

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := s.BackupTo(backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi, err := os.Stat(backupPath); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file missing/empty: %v", err)
	}

	b, err := Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer b.Close()
	seasons, err := b.Seasons()
	if err != nil || len(seasons) != 1 || seasons[0].Name != "۱۴۰۵–۱۴۰۶" {
		t.Fatalf("backup seasons = %+v (%v)", seasons, err)
	}
	if season == nil {
		t.Fatalf("active season vanished")
	}
	// Backups are recorded in the source DB's audit log.
	entries, _ := s.AuditLog(10)
	var backupAudited bool
	for _, e := range entries {
		if e.Action == "backup" && e.Entity == "database" {
			backupAudited = true
		}
	}
	if !backupAudited {
		t.Fatalf("backup not audited")
	}
}

// Extra — week filter (string per contract) and status filter.
func TestMatchesFilters(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))
	idW1, _ := s.CreateMatch(compID, teamA, teamB, intPtr(1), nil, nil, nil)
	idW2, _ := s.CreateMatch(compID, teamB, teamA, intPtr(2), nil, nil, nil)
	_ = idW1
	_ = idW2

	week1, err := s.Matches(compID, strPtr("1"), nil)
	if err != nil || len(week1) != 1 {
		t.Fatalf("week filter = %d (%v), want 1", len(week1), err)
	}
	if err := s.SetResult(week1[0].ID, 1, 0); err != nil {
		t.Fatalf("set result: %v", err)
	}
	finished, err := s.Matches(compID, nil, strPtr("finished"))
	if err != nil || len(finished) != 1 {
		t.Fatalf("status filter = %d (%v), want 1", len(finished), err)
	}
	all, err := s.Matches(compID, nil, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("no filter = %d (%v), want 2", len(all), err)
	}
}

// Extra — Club deactivation blocked by active registrations of its teams.
func TestDeactivateClubBlocked(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, _ := seedLeague(t, s, "league1", strPtr("A"))
	teams, _ := s.Teams(nil)
	var clubA int64
	for _, tm := range teams {
		if tm.ID == teamA {
			clubA = tm.ClubID
		}
	}
	err := s.DeactivateClub(clubA)
	if err == nil || !errors.Is(err, ErrActiveRefs) {
		t.Fatalf("deactivate club with registered team → %v, want ErrActiveRefs", err)
	}
	// Unregister that club's registrations, then deactivation succeeds.
	regs, err := s.Registrations(compID)
	if err != nil {
		t.Fatalf("registrations: %v", err)
	}
	for _, r := range regs {
		if r.ClubID == clubA {
			if err := s.Unregister(r.ID); err != nil {
				t.Fatalf("unregister: %v", err)
			}
		}
	}
	if err := s.DeactivateClub(clubA); err != nil {
		t.Fatalf("deactivate after unregister: %v", err)
	}
	clubs, _ := s.Clubs(true)
	for _, c := range clubs {
		if c.ID == clubA && c.IsActive {
			t.Fatalf("club %d still active", clubA)
		}
	}
}

// ---------------------------------------------------------------------------
// §7 rule traceability additions (TASK-024). Each of these exists because the
// rule matrix in TESTING.md §5 records that rule as uncovered or only
// indirectly covered; each fails if its guard is removed.
// ---------------------------------------------------------------------------

// auditRowCount is the raw audit_log size, used by the rule-10 sweep.
func auditRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

// Rule 7 (deleting is restricted) — a competition cannot be deleted while
// anything references it, with the sentinel matching the reference kind, and a
// successful delete is audited like every other mutation.
func TestDeleteCompetitionRefusedWhileItHasRefs(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))

	// Registrations present → the registrations guard fires first.
	err := s.DeleteCompetition(compID)
	if !errors.Is(err, ErrActiveRefs) {
		t.Fatalf("delete with registrations → %v, want ErrActiveRefs", err)
	}
	if err == nil || !strings.Contains(err.Error(), "امکان حذف نیست") {
		t.Fatalf("delete refusal must be Persian and explanatory, got %v", err)
	}

	// Matches present, registrations gone → the matches guard fires. The store's
	// own API cannot reach this state (Unregister is blocked while a match
	// exists), so the registrations are cleared at the SQL layer: that is the
	// state a legacy or out-of-band write would leave behind, which is exactly
	// what the second guard exists for.
	matchID, err := s.CreateMatch(compID, teamA, teamB, intPtr(1), nil, nil, nil)
	if err != nil {
		t.Fatalf("create match: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM registrations WHERE competition_id = ?`, compID); err != nil {
		t.Fatalf("clear registrations at the SQL layer: %v", err)
	}
	err = s.DeleteCompetition(compID)
	if !errors.Is(err, ErrHasMatches) {
		t.Fatalf("delete with matches → %v, want ErrHasMatches", err)
	}

	// No references left → the delete succeeds, is audited, and the row is gone.
	if err := s.DeleteMatch(matchID); err != nil {
		t.Fatalf("delete match: %v", err)
	}
	before := auditRowCount(t, s)
	if err := s.DeleteCompetition(compID); err != nil {
		t.Fatalf("delete with no references: %v", err)
	}
	if got := auditRowCount(t, s); got != before+1 {
		t.Fatalf("successful delete wrote %d audit rows, want exactly 1", got-before)
	}
	if _, err := s.Competition(compID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("competition %d still readable after delete: %v", compID, err)
	}
}

// mutationFixture carries the ids each mutation step needs from the ones before
// it, so the sweep below also proves every id it depends on is obtainable the
// supported way.
type mutationFixture struct {
	seasonID   int64
	clubA      int64
	teamA      int64
	clubB      int64
	teamB      int64
	compID     int64
	regA       int64
	regB       int64
	matchID    int64
	backupPath string
}

// mutationStep is one named mutating call plus the audit row it must produce.
type mutationStep struct {
	name           string
	action, entity string
	run            func(*mutationFixture, *Store) error
}

// mutationSteps is the ordered sweep of every mutating method on DataStore.
// Order matters only where the store's own rules impose it (register before a
// match, clear registrations before deleting a competition).
func mutationSteps() []mutationStep {
	return []mutationStep{
		{"CreateSeason", "insert", "season", func(fx *mutationFixture, s *Store) error {
			id, err := s.CreateSeason("۱۳۹۹–۱۴۰۱", nil, nil)
			fx.seasonID = id
			return err
		}},
		{"UpdateSeason", "update", "season", func(fx *mutationFixture, s *Store) error {
			return s.UpdateSeason(fx.seasonID, "۱۳۹۹–۱۴۰۲", nil, nil)
		}},
		{"ActivateSeason", "update", "season", func(fx *mutationFixture, s *Store) error {
			return s.ActivateSeason(fx.seasonID)
		}},
		{"EnsureAgeGroups", "insert", "age_group", func(fx *mutationFixture, s *Store) error {
			return s.EnsureAgeGroups(15)
		}},
		{"CreateClub", "insert", "club", func(fx *mutationFixture, s *Store) error {
			id, err := s.CreateClub("باشگاه سنجش")
			fx.clubA = id
			return err
		}},
		{"RenameClub", "update", "club", func(fx *mutationFixture, s *Store) error {
			return s.RenameClub(fx.clubA, "باشگاه سنجش نوین")
		}},
		{"CreateTeam", "insert", "team", func(fx *mutationFixture, s *Store) error {
			id, err := s.CreateTeam(fx.clubA, "۱", "باشگاه سنجش ۱")
			fx.teamA = id
			return err
		}},
		{"UpdateTeam", "update", "team", func(fx *mutationFixture, s *Store) error {
			return s.UpdateTeam(fx.teamA, "۲", "باشگاه سنجش ۲", nil)
		}},
		{"CreateSecondClub", "insert", "club", func(fx *mutationFixture, s *Store) error {
			id, err := s.CreateClub("باشگاه دوم سنجش")
			fx.clubB = id
			return err
		}},
		{"CreateSecondTeam", "insert", "team", func(fx *mutationFixture, s *Store) error {
			id, err := s.CreateTeam(fx.clubB, "۱", "باشگاه دوم سنجش ۱")
			fx.teamB = id
			return err
		}},
		{"CreateCompetition", "insert", "competition", func(fx *mutationFixture, s *Store) error {
			ages, err := s.AgeGroups()
			if err != nil {
				return err
			}
			if len(ages) == 0 {
				return errors.New("no age groups to attach a competition to")
			}
			id, err := s.CreateCompetition(fx.seasonID, ages[0].ID, "league1", strPtr("A"), "لیگ یک گروه A")
			fx.compID = id
			return err
		}},
		{"RenameCompetition", "update", "competition", func(fx *mutationFixture, s *Store) error {
			return s.RenameCompetition(fx.compID, "لیگ یک گروه آ", strPtr("آ"))
		}},
		{"RegisterFirstTeam", "insert", "registration", func(fx *mutationFixture, s *Store) error {
			id, err := s.Register(fx.compID, fx.teamA)
			fx.regA = id
			return err
		}},
		{"RegisterSecondTeam", "insert", "registration", func(fx *mutationFixture, s *Store) error {
			id, err := s.Register(fx.compID, fx.teamB)
			fx.regB = id
			return err
		}},
		{"UnregisterSecondTeam", "delete", "registration", func(fx *mutationFixture, s *Store) error {
			return s.Unregister(fx.regB)
		}},
		{"RegisterSecondTeamAgain", "insert", "registration", func(fx *mutationFixture, s *Store) error {
			id, err := s.Register(fx.compID, fx.teamB)
			fx.regB = id
			return err
		}},
		{"CreateMatch", "insert", "match", func(fx *mutationFixture, s *Store) error {
			id, err := s.CreateMatch(fx.compID, fx.teamA, fx.teamB, intPtr(1), nil, nil, nil)
			fx.matchID = id
			return err
		}},
		{"SetResult", "update", "match", func(fx *mutationFixture, s *Store) error {
			return s.SetResult(fx.matchID, 2, 1)
		}},
		{"ClearResult", "update", "match", func(fx *mutationFixture, s *Store) error {
			return s.ClearResult(fx.matchID)
		}},
		{"UpdateMatchSchedule", "update", "match", func(fx *mutationFixture, s *Store) error {
			return s.UpdateMatchSchedule(fx.matchID, intPtr(2), nil, nil, nil)
		}},
		{"DeleteMatch", "delete", "match", func(fx *mutationFixture, s *Store) error {
			return s.DeleteMatch(fx.matchID)
		}},
		{"UnregisterFirstTeam", "delete", "registration", func(fx *mutationFixture, s *Store) error {
			return s.Unregister(fx.regA)
		}},
		{"UnregisterSecondTeamFinal", "delete", "registration", func(fx *mutationFixture, s *Store) error {
			return s.Unregister(fx.regB)
		}},
		{"DeleteCompetition", "delete", "competition", func(fx *mutationFixture, s *Store) error {
			return s.DeleteCompetition(fx.compID)
		}},
		{"DeactivateTeam", "update", "team", func(fx *mutationFixture, s *Store) error {
			return s.DeactivateTeam(fx.teamA)
		}},
		{"DeactivateClub", "update", "club", func(fx *mutationFixture, s *Store) error {
			return s.DeactivateClub(fx.clubA)
		}},
		{"BackupTo", "backup", "database", func(fx *mutationFixture, s *Store) error {
			return s.BackupTo(fx.backupPath)
		}},
	}
}

// Rule 10 (every admin mutation writes an audit-log row) — the systematic
// version. TestAuditLog spot-checks a handful of mutations; this walks every
// mutating method on DataStore and requires exactly one new row per call, with
// the action/entity the audit page and the filtered query rely on.
//
// It also pins the one deliberate exception: EnsureAgeGroups is idempotent, so a
// call that inserts nothing must audit nothing (a no-op is not a mutation).
func TestEveryMutationWritesAnAuditRow(t *testing.T) {
	s := newTestStore(t)
	fx := &mutationFixture{backupPath: filepath.Join(t.TempDir(), "audit-sweep-backup.db")}

	for _, step := range mutationSteps() {
		t.Run(step.name, func(t *testing.T) {
			before := auditRowCount(t, s)
			if err := step.run(fx, s); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			if got := auditRowCount(t, s); got != before+1 {
				t.Fatalf("%s wrote %d audit rows, want exactly 1", step.name, got-before)
			}
			newest, err := s.AuditLog(1)
			if err != nil {
				t.Fatalf("read audit log: %v", err)
			}
			if len(newest) != 1 || newest[0].Action != step.action || newest[0].Entity != step.entity {
				t.Fatalf("%s audited %+v, want %s/%s", step.name, newest, step.action, step.entity)
			}
		})
	}

	before := auditRowCount(t, s)
	if err := s.EnsureAgeGroups(15); err != nil {
		t.Fatalf("idempotent EnsureAgeGroups: %v", err)
	}
	if got := auditRowCount(t, s); got != before {
		t.Fatalf("a no-op EnsureAgeGroups wrote %d audit rows, want 0", got-before)
	}
}

// Rule 5 (duplicate fixtures rejected) — and its documented boundary. The guard
// is a partial index (WHERE week IS NOT NULL), so two identical pairings with no
// week are both accepted: "the same week" is undefined when a fixture has no
// week. This test pins that limitation so removing or changing it has to be a
// deliberate act, and so TESTING.md §5's rule-5 row cannot silently drift from
// the code.
func TestDuplicateFixtureNeedsAWeek(t *testing.T) {
	s := newTestStore(t)
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))

	// With a week: the second identical fixture is refused.
	if _, err := s.CreateMatch(compID, teamA, teamB, intPtr(4), nil, nil, nil); err != nil {
		t.Fatalf("first week-4 fixture: %v", err)
	}
	if _, err := s.CreateMatch(compID, teamA, teamB, intPtr(4), nil, nil, nil); err == nil {
		t.Fatal("a duplicate fixture with the same week was accepted")
	}

	// Without a week: both are accepted, because there is no week to compare.
	if _, err := s.CreateMatch(compID, teamA, teamB, nil, strPtr("2026-10-01"), nil, nil); err != nil {
		t.Fatalf("first week-less fixture: %v", err)
	}
	if _, err := s.CreateMatch(compID, teamA, teamB, nil, strPtr("2026-10-08"), nil, nil); err != nil {
		t.Fatalf("the second week-less fixture was refused (%v) — if this now fails, the "+
			"duplicate guard changed and TESTING.md §5 rule 5 must be updated with it", err)
	}
}

// newestAudit returns the most recent audit row for one action/entity and fails
// if the newest such row belongs to a different entity id (so a test cannot
// accidentally read a sibling operation's row).
func newestAudit(t *testing.T, s *Store, action, entity string, entityID int64) AuditEntry {
	t.Helper()
	rows, _, err := s.AuditLogFiltered(action, entity, 1, 0)
	if err != nil {
		t.Fatalf("read audit log (%s/%s): %v", action, entity, err)
	}
	if len(rows) == 0 {
		t.Fatalf("no audit row for %s/%s", action, entity)
	}
	if rows[0].EntityID != entityID {
		t.Fatalf("newest %s/%s row is entity %d, want %d", action, entity, rows[0].EntityID, entityID)
	}
	return rows[0]
}

// Rule 10 requires an audit row to carry (action, entity, entity_id, before,
// after, time) — but the mutation sweep above pins only action/entity/count. A
// change that kept writing rows while dropping the before/after payload would
// pass it, so the payload shape is pinned here at all three mutation kinds.
func TestAuditRowsCarryTheBeforeAfterPayload(t *testing.T) {
	s := newTestStore(t)

	// insert — nothing existed before, so before is NULL and after is the new state.
	clubID, err := s.CreateClub("باشگاه سنجش")
	if err != nil {
		t.Fatalf("create club: %v", err)
	}
	inserted := newestAudit(t, s, "insert", "club", clubID)
	if inserted.Before != nil {
		t.Fatalf("insert before = %q, want NULL", *inserted.Before)
	}
	if inserted.After == nil || !strings.Contains(*inserted.After, "باشگاه سنجش") {
		t.Fatalf("insert after = %v, want the created club's name", inserted.After)
	}

	// update — both sides present, and they must differ, or the payload is not a delta.
	if err := s.RenameClub(clubID, "باشگاه سنجش نوین"); err != nil {
		t.Fatalf("rename club: %v", err)
	}
	updated := newestAudit(t, s, "update", "club", clubID)
	if updated.Before == nil || updated.After == nil {
		t.Fatalf("update before/after = %v/%v, want both set", updated.Before, updated.After)
	}
	if *updated.Before == *updated.After {
		t.Fatalf("update before and after are identical (%q) — not a delta", *updated.Before)
	}

	// delete — the row is gone, so after is NULL and before is its last state.
	compID, teamA, teamB := seedLeague(t, s, "league1", strPtr("A"))
	matchID, err := s.CreateMatch(compID, teamA, teamB, intPtr(3), nil, nil, nil)
	if err != nil {
		t.Fatalf("create match: %v", err)
	}
	if err := s.DeleteMatch(matchID); err != nil {
		t.Fatalf("delete match: %v", err)
	}
	deleted := newestAudit(t, s, "delete", "match", matchID)
	if deleted.After != nil {
		t.Fatalf("delete after = %q, want NULL", *deleted.After)
	}
	if deleted.Before == nil {
		t.Fatal("delete before = NULL, want the removed row's last state")
	}

	// actor and time are named in rule 10 too.
	if deleted.Admin == "" {
		t.Fatal("audit row carries no admin")
	}
	if deleted.CreatedAt.IsZero() {
		t.Fatal("audit row carries a zero created_at")
	}
}
