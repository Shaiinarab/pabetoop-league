// TASK-029 — the admin team form requires an age category; a successful POST
// must persist the operator's choice, not merely redirect.
//
// P1 follow-up — a stored field no screen shows is only half a feature, so this
// file now also asserts the teams list *renders* the category. Before that, the
// operator could set a category and never see it again.
package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestAdminTeamCreatePersistsAgeGroup(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)

	clubID, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	ages, err := st.AgeGroups()
	if err != nil || len(ages) == 0 {
		t.Fatalf("age groups: %v", err)
	}
	ageID := ages[len(ages)-1].ID
	ageName := ages[len(ages)-1].DisplayName

	rec := adminMutate(s.Handler(), "/admin/teams", url.Values{
		"club":         {strconv.FormatInt(clubID, 10)},
		"age_group":    {strconv.FormatInt(ageID, 10)},
		"display_name": {"نمونه ب ۲"},
		"label":        {"۲"},
	}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create team = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}

	teams, err := st.Teams(nil)
	if err != nil || len(teams) != 1 {
		t.Fatalf("teams = %v (%v), want exactly the one just created", teams, err)
	}
	got := teams[0].AgeGroupID
	if got == nil || *got != ageID {
		t.Fatalf("team %d stored age_group_id = %v, want the submitted %d", teams[0].ID, got, ageID)
	}

	// The stored field must be visible: the operator set it on this screen and
	// has no other way to confirm it took. GET the list and look for the
	// category's own display name in the rendered row.
	list := adminBrowse(s.Handler(), "/admin/teams", session)
	if list.Code != http.StatusOK {
		t.Fatalf("GET /admin/teams = %d, want 200", list.Code)
	}
	body := list.Body.String()
	if !strings.Contains(body, ageName) {
		t.Errorf("teams list does not show the stored category %q — the operator can set it but not see it", ageName)
	}
	// The header must promise the column, not just the value in a cell.
	if !strings.Contains(body, "ردهٔ سنی") {
		t.Errorf("teams list has no age-category column header")
	}
	// And the join must not have dropped the row it belongs to.
	if !strings.Contains(body, "نمونه ب ۲") {
		t.Errorf("teams list does not contain the created team")
	}
}

// TestAdminTeamsListRendersUncategorisedAsDash pins the other half of the join:
// a team with no category (cmd/seed, pre-0004) must still appear, with the
// category cell empty rather than the row vanishing or a stray name leaking in.
func TestAdminTeamsListRendersUncategorisedAsDash(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)

	clubID, err := st.CreateClub("باشگاه نمونه")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	// CreateTeam is the no-category path: age_group_id stays NULL.
	if _, err := st.CreateTeam(clubID, "۱", "نمونه ۱"); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	list := adminBrowse(s.Handler(), "/admin/teams", session)
	if list.Code != http.StatusOK {
		t.Fatalf("GET /admin/teams = %d, want 200", list.Code)
	}
	body := list.Body.String()
	if !strings.Contains(body, "نمونه ۱") {
		t.Fatalf("an uncategorised team was dropped from the list by the LEFT JOIN")
	}
	ages, err := st.AgeGroups()
	if err != nil {
		t.Fatalf("age groups: %v", err)
	}
	for _, a := range ages {
		if strings.Contains(body, a.DisplayName) {
			t.Errorf("an uncategorised team rendered the category %q — the LEFT JOIN leaked a name", a.DisplayName)
		}
	}
}
