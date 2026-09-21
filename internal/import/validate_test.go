package importer

import (
	"strings"
	"testing"
)

// testTeams is a small registered-team set (ids are the store's team ids).
func testTeams() []Team {
	return []Team{
		{ID: 1, DisplayName: "نمونه ب ۱"},
		{ID: 2, DisplayName: "نمونه پ ۱"},
		{ID: 3, DisplayName: "نمونه ث ۱"},
		{ID: 4, DisplayName: "نمونه ب نوین"},
	}
}

// weekRow builds a parsed row without going through Parse.
func weekRow(line, week int, home, away string) Row {
	w := week
	return Row{Line: line, Week: &w, Date: isoAnchor, Home: home, Away: away}
}

func TestValidateExactMatchResolves(t *testing.T) {
	v := Validate([]Row{weekRow(2, 1, "نمونه ب ۱", "نمونه پ ۱")}, testTeams(), nil)
	if v.Total() != 1 {
		t.Fatalf("total = %d, want 1", v.Total())
	}
	row := v.Rows[0]
	if !row.Valid() {
		t.Fatalf("row should be valid, errors: %+v", row.Errors)
	}
	if row.HomeTeamID != 1 || row.AwayTeamID != 2 {
		t.Errorf("resolved ids = %d/%d, want 1/2", row.HomeTeamID, row.AwayTeamID)
	}
	if row.HomeTeamName != "نمونه ب ۱" {
		t.Errorf("resolved name = %q", row.HomeTeamName)
	}
	if v.ValidCount() != 1 || v.ErrorCount() != 0 {
		t.Errorf("counts = %d valid / %d error, want 1/0", v.ValidCount(), v.ErrorCount())
	}
}

func TestValidateTrimsWhitespaceForMatching(t *testing.T) {
	v := Validate([]Row{weekRow(2, 1, "  نمونه ب ۱ ", "نمونه پ ۱")}, testTeams(), nil)
	if !v.Rows[0].Valid() {
		t.Fatalf("surrounding whitespace should still match exactly, errors: %+v", v.Rows[0].Errors)
	}
}

func TestValidateUnknownTeamGivesNearestSuggestions(t *testing.T) {
	v := Validate([]Row{weekRow(2, 1, "نمونه ب", "نمونه پ ۱")}, testTeams(), nil)
	row := v.Rows[0]
	if row.Valid() {
		t.Fatal("an unregistered name must not be silently accepted (D7)")
	}
	if row.NeedsHomePick() != true {
		t.Error("an unresolved side must be flagged as needing an explicit pick")
	}
	if len(row.Errors) != 1 {
		t.Fatalf("errors = %+v, want exactly one", row.Errors)
	}
	msg := row.Errors[0].Message
	if !strings.Contains(msg, "نمونه ب") || !strings.Contains(msg, "ثبت‌نام نشده") {
		t.Errorf("message = %q, want a Persian not-registered message naming the team", msg)
	}
	if row.Errors[0].Line != 2 {
		t.Errorf("error line = %d, want 2 (the row's physical line)", row.Errors[0].Line)
	}
	if len(row.HomeSuggestions) != maxSuggestions {
		t.Fatalf("suggestions = %v, want %d", row.HomeSuggestions, maxSuggestions)
	}
	if row.HomeSuggestions[0] != "نمونه ب ۱" {
		t.Errorf("closest suggestion = %q, want «نمونه ب ۱»", row.HomeSuggestions[0])
	}
	// The supplied text is preserved, never corrected.
	if row.Home != "نمونه ب" || row.HomeTeamName != "نمونه ب" {
		t.Errorf("supplied name was rewritten: %q / %q", row.Home, row.HomeTeamName)
	}
}

func TestValidateMatchesAcrossArabicLetterForms(t *testing.T) {
	// The file says «نوين» with an Arabic yeh; the registered name uses a Persian
	// yeh. That is NOT an exact match, so it must be offered as a suggestion
	// rather than silently resolved (D7 — never auto-correct).
	teams := []Team{{ID: 7, DisplayName: "نمونه ب نوین"}}
	v := Validate([]Row{weekRow(3, 1, "نمونه ب نوين", "نمونه ب نوين")}, teams, nil)
	row := v.Rows[0]
	if row.Valid() {
		t.Fatal("Arabic yeh must not auto-resolve; D7 forbids silent correction")
	}
	if len(row.HomeSuggestions) == 0 || row.HomeSuggestions[0] != "نمونه ب نوین" {
		t.Errorf("suggestions = %v, want the official «نمونه ب نوین» first", row.HomeSuggestions)
	}
}

func TestValidateSelfMatchIsRejected(t *testing.T) {
	v := Validate([]Row{weekRow(2, 1, "نمونه پ ۱", "نمونه پ ۱")}, testTeams(), nil)
	row := v.Rows[0]
	if row.Valid() {
		t.Fatal("a team cannot play itself")
	}
	if !hasMessage(row.Errors, "خودش") {
		t.Errorf("errors = %+v, want a Persian self-match message", row.Errors)
	}
}

func TestValidateDuplicateFixtureInFile(t *testing.T) {
	rows := []Row{
		weekRow(2, 1, "نمونه ب ۱", "نمونه پ ۱"),
		weekRow(3, 1, "نمونه ب ۱", "نمونه پ ۱"),
	}
	v := Validate(rows, testTeams(), nil)
	if !v.Rows[0].Valid() {
		t.Fatalf("first occurrence should be valid, errors: %+v", v.Rows[0].Errors)
	}
	second := v.Rows[1]
	if second.Valid() {
		t.Fatal("the second identical fixture must be flagged")
	}
	msg := second.Errors[0].Message
	if !strings.Contains(msg, "تکرار") {
		t.Errorf("message = %q, want a Persian duplicate message", msg)
	}
	if !strings.Contains(msg, "خط ۲") { // first occurrence line, Persian digits
		t.Errorf("message = %q, want it to name the first occurrence's line «خط ۲»", msg)
	}
}

func TestValidateDifferentWeekIsNotADuplicate(t *testing.T) {
	rows := []Row{
		weekRow(2, 1, "نمونه ب ۱", "نمونه پ ۱"),
		weekRow(3, 2, "نمونه ب ۱", "نمونه پ ۱"),
	}
	v := Validate(rows, testTeams(), nil)
	if v.ValidCount() != 2 {
		t.Fatalf("valid = %d, want 2 (same pairing in another week is legitimate)", v.ValidCount())
	}
}

func TestValidateAmbiguousSharedNameNeedsAPick(t *testing.T) {
	teams := []Team{
		{ID: 1, DisplayName: "نمونه ب ۱"},
		{ID: 2, DisplayName: "نمونه ب ۱"}, // same official display name, different team
	}
	v := Validate([]Row{weekRow(2, 1, "نمونه ب ۱", "نمونه ب ۱")}, teams, nil)
	row := v.Rows[0]
	if row.Valid() {
		t.Fatal("a name shared by two teams must not be resolved automatically")
	}
	if !hasMessage(row.Errors, "مشترک") {
		t.Errorf("errors = %+v, want a Persian ambiguous-name message", row.Errors)
	}
}

func TestValidateCountsMixOfValidAndInvalid(t *testing.T) {
	rows := []Row{
		weekRow(2, 1, "نمونه ب ۱", "نمونه پ ۱"),    // valid
		weekRow(3, 2, "نام ناشناخته", "نمونه پ ۱"), // error
		weekRow(4, 3, "نمونه ب ۱", "نمونه ث ۱"),
	}
	v := Validate(rows, testTeams(), nil)
	if v.Total() != 3 {
		t.Errorf("total = %d, want 3", v.Total())
	}
	if v.ValidCount() != 2 || v.ErrorCount() != 1 {
		t.Errorf("counts = %d valid / %d error, want 2/1", v.ValidCount(), v.ErrorCount())
	}
}

func TestValidateCarriesParseErrorsAndSortsByLine(t *testing.T) {
	parseErrs := []RowError{{Line: 9, Message: "خط نهم مشکل دارد."}, {Line: 1, Message: "خط اول مشکل دارد."}}
	v := Validate([]Row{weekRow(5, 1, "نمونه ب ۱", "نمونه پ ۱")}, testTeams(), parseErrs)
	if len(v.Errors) != 2 {
		t.Fatalf("errors = %+v, want the two parse errors carried through", v.Errors)
	}
	if v.Errors[0].Line != 1 || v.Errors[1].Line != 9 {
		t.Errorf("errors not sorted by line: %+v", v.Errors)
	}
}

func TestValidateEmptyRowNamesAreNotResolved(t *testing.T) {
	v := Validate([]Row{weekRow(2, 1, "", "")}, testTeams(), nil)
	row := v.Rows[0]
	if row.Valid() {
		t.Fatal("empty names cannot be valid")
	}
	if !row.NeedsHomePick() || !row.NeedsAwayPick() {
		t.Error("empty sides must be flagged as needing a pick")
	}
}

// hasMessage reports whether any error message contains needle.
func hasMessage(errs []RowError, needle string) bool {
	for _, e := range errs {
		if strings.Contains(e.Message, needle) {
			return true
		}
	}
	return false
}
