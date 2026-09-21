// TASK-037 — the U in teams CRUD.
//
// Reviewing the create path would not have caught this: the form was already
// edit-capable (its contract carries .Title/.ActionURL/.Team) and the store
// method already existed. What was missing was the route and the handlers, and
// the only way to see that is to try to edit a team. These tests do.
package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// seedEditableTeam creates a club and a categorised team and returns
// (clubID, teamID, ageID, ageName).
func seedEditableTeam(t *testing.T, st *store.Store) (clubID, teamID, ageID int64, ageName string) {
	t.Helper()
	clubID, err := st.CreateClub("نمونه ب")
	if err != nil {
		t.Fatalf("seed club: %v", err)
	}
	ages, err := st.AgeGroups()
	if err != nil || len(ages) == 0 {
		t.Fatalf("seed age groups: %d (%v)", len(ages), err)
	}
	ageID, ageName = ages[0].ID, ages[0].DisplayName
	teamID, err = st.CreateTeamWithAgeGroup(clubID, &ageID, "۱", "نمونه ب ۱")
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}
	return clubID, teamID, ageID, ageName
}

func TestAdminTeamEditFormPrefillsAndLocksTheClub(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)
	_, teamID, _, ageName := seedEditableTeam(t, st)

	rec := adminBrowse(s.Handler(), "/admin/teams/"+strconv.FormatInt(teamID, 10)+"/edit", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET edit form = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// It is the edit screen, not the create screen…
	if !strings.Contains(body, "ویرایش تیم") {
		t.Errorf("edit form does not say ویرایش تیم — the same form is used, so only the view data can say it")
	}
	// …posting to the resource, not to the collection. The quoted form is what
	// makes this exact: action="/admin/teams/7" must not satisfy it.
	if !strings.Contains(body, `action="/admin/teams/`+strconv.FormatInt(teamID, 10)+`"`) {
		t.Errorf("edit form does not post to /admin/teams/%d", teamID)
	}
	if strings.Contains(body, `action="/admin/teams"`) {
		t.Errorf("edit form still posts to the create target /admin/teams")
	}
	// Prefilled from the stored row: the operator must not have to retype what
	// they are not changing.
	if !strings.Contains(body, "نمونه ب ۱") {
		t.Errorf("edit form is not prefilled with the team's display name")
	}
	if !strings.Contains(body, ageName) {
		t.Errorf("edit form is not prefilled with the team's category %q", ageName)
	}
	// The club is not writable (store.UpdateTeam does not write it), so the
	// control is locked rather than silently ignored.
	if !strings.Contains(body, `name="club" required disabled`) {
		t.Errorf("club select is not disabled in edit mode — an enabled control that does nothing is the worse lie")
	}
}

func TestAdminTeamsListLinksToTheEditForm(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)
	_, teamID, _, _ := seedEditableTeam(t, st)

	list := adminBrowse(s.Handler(), "/admin/teams", session)
	if list.Code != http.StatusOK {
		t.Fatalf("GET /admin/teams = %d, want 200", list.Code)
	}
	// A route nobody can click is not shipped.
	if !strings.Contains(list.Body.String(), "/admin/teams/"+strconv.FormatInt(teamID, 10)+"/edit") {
		t.Errorf("teams list has no link to the edit form — the route is unreachable from the UI")
	}
}

func TestAdminTeamUpdatePersistsAllThreeFields(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	_, teamID, _, _ := seedEditableTeam(t, st)

	ages, err := st.AgeGroups()
	if err != nil || len(ages) < 2 {
		t.Fatalf("age groups: %d (%v), want at least 2", len(ages), err)
	}
	newAge := ages[len(ages)-1]

	path := "/admin/teams/" + strconv.FormatInt(teamID, 10)
	rec := adminMutate(s.Handler(), path, url.Values{
		"age_group":    {strconv.FormatInt(newAge.ID, 10)},
		"display_name": {"نمونه ب ۲"},
		"label":        {"۲"},
	}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update team = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}

	got, err := st.TeamByID(teamID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.DisplayName != "نمونه ب ۲" || got.Label != "۲" {
		t.Errorf("row = %q/%q, want the edited values", got.DisplayName, got.Label)
	}
	if got.AgeGroupID == nil || *got.AgeGroupID != newAge.ID {
		t.Errorf("age_group_id = %v, want the edited category %d", got.AgeGroupID, newAge.ID)
	}
}

func TestAdminTeamUpdateRerenders422OnValidationError(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	_, teamID, ageID, _ := seedEditableTeam(t, st)

	path := "/admin/teams/" + strconv.FormatInt(teamID, 10)
	rec := adminMutate(s.Handler(), path, url.Values{
		"age_group":    {strconv.FormatInt(ageID, 10)},
		"display_name": {""}, // required
		"label":        {"۲"},
	}, session, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty display name = %d, want 422 (D2: htmx swaps 422 bodies)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "نام نمایشی تیم") {
		t.Errorf("422 body does not carry the Persian field error")
	}
	// And the re-rendered form is still the edit form.
	if !strings.Contains(body, "ویرایش تیم") {
		t.Errorf("422 body re-rendered the create form instead of the edit form")
	}

	got, err := st.TeamByID(teamID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.DisplayName != "نمونه ب ۱" || got.Label != "۱" {
		t.Errorf("row = %q/%q after a refused update, want it untouched", got.DisplayName, got.Label)
	}
}

func TestAdminTeamUpdateRefusesUnknownCategoryInPersian(t *testing.T) {
	s, st := newAdminTestServer(t)
	session, csrf := loginTestAdmin(t, s)
	_, teamID, _, _ := seedEditableTeam(t, st)

	path := "/admin/teams/" + strconv.FormatInt(teamID, 10)
	rec := adminMutate(s.Handler(), path, url.Values{
		"age_group":    {"999999"},
		"display_name": {"نمونه ب ۲"},
		"label":        {"۲"},
	}, session, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown category = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	// The store's Persian refusal must reach the operator, not a raw FK error.
	if !strings.Contains(rec.Body.String(), "رده سنی با شناسه 999999 یافت نشد") {
		t.Errorf("422 body does not carry the Persian unknown-category message")
	}
	got, err := st.TeamByID(teamID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.DisplayName != "نمونه ب ۱" {
		t.Errorf("display name = %q after a refused update, want it untouched", got.DisplayName)
	}
}

func TestAdminTeamEditUnknownIdIs404(t *testing.T) {
	s, _ := newAdminTestServer(t)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/teams/999999/edit", session)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("edit of an unknown team = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "تیم با شناسه 999999 یافت نشد") {
		t.Errorf("404 body does not name the missing team in Persian")
	}
}
