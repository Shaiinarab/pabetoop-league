// TASK-029 — the team form's age category must reach the database, and
// migration 0004 must apply to a database that already contains teams.
package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// teamWithID returns the team with the given id from a read result. The
// category now travels on Team itself, so the tests read it back through the
// same public path the admin list uses — there is no separate accessor to
// exercise (the old TeamAgeGroupID existed only because Team had no field).
func teamWithID(t *testing.T, teams []Team, id int64) Team {
	t.Helper()
	for _, tm := range teams {
		if tm.ID == id {
			return tm
		}
	}
	t.Fatalf("team %d not among the %d returned", id, len(teams))
	return Team{}
}

// TestCreateTeamWithAgeGroupPersistsAndFrozenPathStaysNull covers the store
// method the admin form calls: the submitted category is stored and comes back
// on Team, while CreateTeam (cmd/seed and older callers) keeps age_group_id
// NULL, and a category that does not exist is refused in Persian.
func TestCreateTeamWithAgeGroupPersistsAndFrozenPathStaysNull(t *testing.T) {
	s := newTestStore(t)
	ages, err := s.AgeGroups()
	if err != nil || len(ages) < 2 {
		t.Fatalf("age groups: %v (%d)", err, len(ages))
	}
	clubID, err := s.CreateClub("باشگاه سنجش")
	if err != nil {
		t.Fatalf("club: %v", err)
	}

	ageID := ages[1].ID
	withAge, err := s.CreateTeamWithAgeGroup(clubID, &ageID, "۱", "سنجش ۱")
	if err != nil {
		t.Fatalf("create with age group: %v", err)
	}
	teams, err := s.Teams(&clubID)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	got := teamWithID(t, teams, withAge)
	if got.AgeGroupID == nil || *got.AgeGroupID != ageID {
		t.Errorf("stored age_group_id = %v, want %d", got.AgeGroupID, ageID)
	}
	// The joined name is what the teams list renders; asserting it here keeps a
	// broken join from reaching the screen as a blank column.
	if got.AgeGroupName != ages[1].DisplayName {
		t.Errorf("AgeGroupName = %q, want %q", got.AgeGroupName, ages[1].DisplayName)
	}

	noAge, err := s.CreateTeam(clubID, "۲", "سنجش ۲") // no category: age_group_id NULL
	if err != nil {
		t.Fatalf("create without age group: %v", err)
	}
	teams, err = s.Teams(&clubID)
	if err != nil {
		t.Fatalf("teams after second create: %v", err)
	}
	got = teamWithID(t, teams, noAge)
	if got.AgeGroupID != nil {
		t.Errorf("age_group_id via CreateTeam = %d, want NULL", *got.AgeGroupID)
	}
	// An uncategorised team must still be returned (the join is a LEFT JOIN) and
	// must render as «—», not as a stray name.
	if got.AgeGroupName != "" {
		t.Errorf("AgeGroupName for an uncategorised team = %q, want empty", got.AgeGroupName)
	}

	bad := int64(99999)
	if _, err := s.CreateTeamWithAgeGroup(clubID, &bad, "۳", "سنجش ۳"); err == nil {
		t.Error("a non-existent age group must be refused")
	}
}

// TestMigration0004AppliesToExistingTeams is the regression for the nullable
// decision. The seeded database already contains teams; a NOT NULL column with
// no default would abort Migrate() and stop the server booting. Build exactly
// that pre-0004 schema with one team in it, then prove Migrate() succeeds and
// leaves the historic row intact with a NULL category.
func TestMigration0004AppliesToExistingTeams(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "seeded.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.ensureMigrationsTable(); err != nil {
		t.Fatalf("migrations table: %v", err)
	}
	for _, name := range []string{
		"0001_init.sql",
		"0002_competitions_premier_unique.sql",
		"0003_age_groups_seed.sql",
	} {
		raw, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := s.db.Exec(stripPragmas(string(raw))); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	res, err := s.db.Exec(`INSERT INTO clubs (name) VALUES ('باشگاه قدیمی')`)
	if err != nil {
		t.Fatalf("historic club: %v", err)
	}
	clubID, _ := res.LastInsertId()
	res, err = s.db.Exec(`INSERT INTO teams (club_id, label, display_name) VALUES (?, '۱', 'قدیمی ۱')`, clubID)
	if err != nil {
		t.Fatalf("historic team: %v", err)
	}
	teamID, _ := res.LastInsertId()

	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate() on a database with existing teams must not abort: %v", err)
	}

	var age sql.NullInt64
	if err := s.db.QueryRow(`SELECT age_group_id FROM teams WHERE id = ?`, teamID).Scan(&age); err != nil {
		t.Fatalf("historic team must survive 0004: %v", err)
	}
	if age.Valid {
		t.Errorf("historic team age_group_id = %d, want NULL (not derivable)", age.Int64)
	}
}
