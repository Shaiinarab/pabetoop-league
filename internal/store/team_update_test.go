// TASK-037 — the U in teams CRUD. UpdateTeam used to be dead code: no handler
// and no route called it, and it could not carry the age category the create
// path requires (so an edit would have silently dropped it). These tests pin the
// three things that made it a gap: the category is persisted, the before/after
// audit payload describes **every** field the call can change, and an unknown
// category is refused in Persian rather than as a raw FK error.
//
// The club is deliberately not writable (see UpdateTeam's comment): a team's club
// anchors its display name and history, so moving one is a deactivate-and-recreate.
package store

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// seedCategorisedTeam creates a club and a team in it with a real category and
// returns (clubID, teamID, ageID). The category is what TASK-029 added and what
// this task had to make editable.
func seedCategorisedTeam(t *testing.T, s *Store) (clubID, teamID, ageID int64) {
	t.Helper()
	clubID, err := s.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("create club: %v", err)
	}
	ages, err := s.AgeGroups()
	if err != nil || len(ages) == 0 {
		t.Fatalf("age groups: %d (%v)", len(ages), err)
	}
	ageID = ages[0].ID
	teamID, err = s.CreateTeamWithAgeGroup(clubID, &ageID, "۱", "نمونه ب ۱")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	return clubID, teamID, ageID
}

func TestUpdateTeamPersistsLabelDisplayAndCategory(t *testing.T) {
	s := newTestStore(t)
	clubID, teamID, seededAge := seedCategorisedTeam(t, s)

	ages, err := s.AgeGroups()
	if err != nil {
		t.Fatalf("age groups: %v", err)
	}
	newAge := ages[len(ages)-1]
	if newAge.ID == seededAge {
		t.Fatalf("test needs two distinct categories, got %d twice", seededAge)
	}

	if err := s.UpdateTeam(teamID, "۲", "نمونه ب ۲", &newAge.ID); err != nil {
		t.Fatalf("update team: %v", err)
	}

	got, err := s.TeamByID(teamID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Label != "۲" {
		t.Errorf("label = %q, want the edited value", got.Label)
	}
	if got.DisplayName != "نمونه ب ۲" {
		t.Errorf("display_name = %q, want the edited value", got.DisplayName)
	}
	if got.AgeGroupID == nil || *got.AgeGroupID != newAge.ID {
		t.Errorf("age_group_id = %v, want the edited category %d — the edit silently dropped it", got.AgeGroupID, newAge.ID)
	}
	// The joined name must follow the id, not go stale in the same read.
	if got.AgeGroupName != newAge.DisplayName {
		t.Errorf("age_group_name = %q, want the joined %q", got.AgeGroupName, newAge.DisplayName)
	}
	// The club is immutable by design and must still be joined.
	if got.ClubID != clubID || got.ClubName != "نمونه ب" {
		t.Errorf("club = %d/%q, want %d/نمونه ب — the edit moved the team", got.ClubID, got.ClubName, clubID)
	}
}

// A nil category means "leave it unchanged", not "clear it" (DataStore.UpdateTeam).
// The default has to be the safe one: the edit form always submits a category, so
// nil can only arrive from a programmatic caller — and a bug that dropped the
// field must not be able to wipe stored data.
func TestUpdateTeamNilCategoryLeavesItAlone(t *testing.T) {
	s := newTestStore(t)
	_, teamID, ageID := seedCategorisedTeam(t, s)

	if err := s.UpdateTeam(teamID, "۱", "نمونه ب ۱ (اصلاح)", nil); err != nil {
		t.Fatalf("update team: %v", err)
	}

	got, err := s.TeamByID(teamID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.AgeGroupID == nil || *got.AgeGroupID != ageID {
		t.Errorf("age_group_id = %v, want the untouched category %d", got.AgeGroupID, ageID)
	}
	if got.DisplayName != "نمونه ب ۱ (اصلاح)" {
		t.Errorf("display_name = %q, want the edited value", got.DisplayName)
	}
}

// Rule 10 (TESTING.md §7) wants an audit row per mutation with a real
// before/after delta. The category is the field this task added to the payload —
// and a nil→value transition is the one case the old payload could not express.
func TestUpdateTeamAuditsEveryChangedField(t *testing.T) {
	s := newTestStore(t)
	ages, err := s.AgeGroups()
	if err != nil || len(ages) == 0 {
		t.Fatalf("age groups: %d (%v)", len(ages), err)
	}
	clubID, err := s.CreateClub("باشگاه نمونه")
	if err != nil {
		t.Fatalf("create club: %v", err)
	}
	// CreateTeam is the no-category path: age_group_id starts NULL.
	teamID, err := s.CreateTeam(clubID, "۱", "نمونه ۱")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}

	newAge := ages[0].ID
	if err := s.UpdateTeam(teamID, "۲", "نمونه ۲", &newAge); err != nil {
		t.Fatalf("update team: %v", err)
	}

	row := newestAudit(t, s, "update", "team", teamID)
	if row.Before == nil || row.After == nil {
		t.Fatalf("audit before/after = %v/%v, want both set", row.Before, row.After)
	}
	for _, field := range []string{"label", "display_name", "age_group_id"} {
		if !strings.Contains(*row.Before, field) {
			t.Errorf("audit before has no %q: %s", field, *row.Before)
		}
		if !strings.Contains(*row.After, field) {
			t.Errorf("audit after has no %q: %s", field, *row.After)
		}
	}
	// The nil → value transition is the whole point: it must be visible as a
	// change, not as two identical payloads.
	if *row.Before == *row.After {
		t.Errorf("audit before and after are identical (%s) — not a delta", *row.Before)
	}
	if !strings.Contains(*row.Before, `"age_group_id":null`) {
		t.Errorf("audit before does not record the uncategorised state: %s", *row.Before)
	}
	// …and the after side must carry the value it became, or the row records a
	// change without saying what changed to.
	if want := `"age_group_id":` + strconv.FormatInt(newAge, 10); !strings.Contains(*row.After, want) {
		t.Errorf("audit after does not record the new category (want %s): %s", want, *row.After)
	}
}

func TestUpdateTeamRefusesUnknownCategoryInPersian(t *testing.T) {
	s := newTestStore(t)
	_, teamID, ageID := seedCategorisedTeam(t, s)

	bogus := int64(999999)
	err := s.UpdateTeam(teamID, "۲", "نمونه ب ۲", &bogus)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("update with an unknown category = %v, want ErrNotFound", err)
	}
	if err == nil || !strings.Contains(err.Error(), "رده سنی با شناسه 999999 یافت نشد") {
		t.Errorf("error = %v, want the Persian message naming the id", err)
	}
	// A refusal must not have half-applied: the row is exactly as it was.
	got, err := s.TeamByID(teamID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Label != "۱" || got.DisplayName != "نمونه ب ۱" {
		t.Errorf("row = %q/%q after a refused update, want it untouched", got.Label, got.DisplayName)
	}
	if got.AgeGroupID == nil || *got.AgeGroupID != ageID {
		t.Errorf("age_group_id = %v after a refused update, want the original %d", got.AgeGroupID, ageID)
	}
}

func TestUpdateTeamRefusesDuplicateLabelInPersian(t *testing.T) {
	s := newTestStore(t)
	clubID, teamA, ageID := seedCategorisedTeam(t, s)
	teamB, err := s.CreateTeamWithAgeGroup(clubID, &ageID, "۲", "نمونه ب ۲")
	if err != nil {
		t.Fatalf("create second team: %v", err)
	}

	// teams.UNIQUE (club_id, label) — the second team cannot take the first's label.
	err = s.UpdateTeam(teamB, "۱", "نمونه ب ۳", &ageID)
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("duplicate label = %v, want ErrInUse", err)
	}
	// The message names the label that collided, not the club's name.
	if err == nil || !strings.Contains(err.Error(), "قبلاً تیمی با برچسب") || !strings.Contains(err.Error(), "۱") {
		t.Errorf("error = %v, want the Persian duplicate-label message naming the label", err)
	}
	got, err := s.TeamByID(teamB)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Label != "۲" || got.DisplayName != "نمونه ب ۲" {
		t.Errorf("row = %q/%q after a refused update, want it untouched", got.Label, got.DisplayName)
	}
	_ = teamA
}

func TestTeamByIDUnknownIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.TeamByID(999999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("TeamByID(unknown) = %v, want ErrNotFound", err)
	}
	if err == nil || !strings.Contains(err.Error(), "تیم با شناسه 999999 یافت نشد") {
		t.Errorf("error = %v, want the Persian message naming the id", err)
	}
}
