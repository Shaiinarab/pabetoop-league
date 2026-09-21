// Fresh-install acceptance test — the client's headline Definition-of-Done item:
// «Admin can do a full season flow without documentation» (PROJECT_SPEC §10).
//
// This file deliberately performs ZERO seeding, beyond the canonical migration:
// no `seedTestData`, no `EnsureAgeGroups`, no direct INSERTs. Everything the test
// needs must come from `Migrate()` + the admin UI, because that is exactly what an
// operator on a brand-new install has. A test that seeds itself cannot prove the
// install is usable — it proves the test fixtures are.
//
// Origin: fb3's audit F2 ("fresh install is a dead end") was fixed by TASK-016
// (the seasons screen), but the dead end had only MOVED. `0001_init.sql` creates the
// `age_groups` table without rows, and the only caller of `EnsureAgeGroups` was
// `cmd/seed` — so a fresh install could create and activate a season and then find
// the competitions form's «ردهٔ سنی» select empty. Migration 0003 seeds the five
// fixed MVP categories; this file is what keeps that true.
package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// TestFreshInstallSeedsAgeCategoriesFromMigrateAlone pins the migration itself.
// Five categories, exact Persian labels, and a second Migrate() must not duplicate
// them (the same OR IGNORE that keeps an existing database bootable).
func TestFreshInstallSeedsAgeCategoriesFromMigrateAlone(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir() + "/fresh.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	want := []struct {
		age         int
		displayName string
	}{
		{10, "۱۰ سال"},
		{11, "۱۱ سال"},
		{12, "۱۲ سال"},
		{13, "۱۳ سال"},
		{14, "۱۴ سال"},
	}
	ages, err := st.AgeGroups()
	if err != nil {
		t.Fatalf("age groups: %v", err)
	}
	if len(ages) != len(want) {
		t.Fatalf("fresh install has %d age categories, want %d: %+v", len(ages), len(want), ages)
	}
	for i, w := range want {
		if ages[i].Age != w.age || ages[i].DisplayName != w.displayName {
			t.Errorf("age group %d = {%d, %q}, want {%d, %q}", i, ages[i].Age, ages[i].DisplayName, w.age, w.displayName)
		}
	}

	// Re-running the migrator (a server restart) must be a no-op.
	if err := st.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if again, err := st.AgeGroups(); err != nil || len(again) != len(want) {
		t.Errorf("second migrate changed the age categories: %d rows (err=%v)", len(again), err)
	}

	// The seed path must still be idempotent on top of the migration: an existing
	// database carries these rows already, and `EnsureAgeGroups` must not duplicate
	// them — nor may the migration have blocked it.
	if err := st.EnsureAgeGroups(10, 11, 12, 13, 14); err != nil {
		t.Fatalf("EnsureAgeGroups on a migrated database: %v", err)
	}
	if merged, err := st.AgeGroups(); err != nil || len(merged) != len(want) {
		t.Errorf("EnsureAgeGroups after the migration produced %d rows, want %d (err=%v)", len(merged), len(want), err)
	}
}

// TestFreshInstallCompletesTheSeasonFlow is the end-to-end proof: a brand-new
// database, the admin UI only, season → activate → competition.
func TestFreshInstallCompletesTheSeasonFlow(t *testing.T) {
	s, st := newAdminTestServer(t) // Migrate() only — the fresh-install state
	session, csrf := loginTestAdmin(t, s)

	// 1. The seasons screen must be reachable and explain an empty install.
	rec := adminBrowse(s.Handler(), "/admin/seasons", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/seasons = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "هنوز فصلی ثبت نشده است") {
		t.Errorf("fresh install's seasons screen must explain itself in Persian, got: %s", rec.Body.String())
	}

	// 2. Create the season the operator is told to create.
	const seasonName = "۱۴۰۶–۱۴۰۷"
	rec = adminMutate(s.Handler(), "/admin/seasons", url.Values{
		"name":      {seasonName},
		"starts_on": {"۱۴۰۶/۰۷/۰۱"},
	}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create season = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	season, ok := seasonByName(t, st, seasonName)
	if !ok {
		t.Fatal("the season the operator just created is not in the store")
	}

	// 3. Activate it (the single-active rule is the store's job).
	rec = adminMutate(s.Handler(), "/admin/seasons/"+strconv.FormatInt(season.ID, 10)+"/activate",
		url.Values{}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate season = %d, want 303", rec.Code)
	}

	// 4. THE REGRESSION: the competitions page must offer an age category. Before
	//    migration 0003 this select was empty on a fresh install — the operator
	//    could fill in the whole form and still have nothing to submit.
	rec = adminBrowse(s.Handler(), "/admin/competitions", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/competitions = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "۱۲ سال") {
		t.Fatalf("fresh install's competitions form offers no age category — the "+
			"season flow dead-ends here (body: %s)", body)
	}
	if !strings.Contains(body, `name="age_group"`) {
		t.Fatalf("competitions form lost its age_group field: %s", body)
	}
	ages, err := st.AgeGroups()
	if err != nil || len(ages) == 0 {
		t.Fatalf("no age categories to choose from: %v", err)
	}

	// 5. Create the first competition of the new season, using only what the UI has.
	age := ages[0]
	rec = adminMutate(s.Handler(), "/admin/competitions", url.Values{
		"season":       {strconv.FormatInt(season.ID, 10)},
		"age_group":    {strconv.FormatInt(age.ID, 10)},
		"level":        {"premier"},
		"display_name": {"لیگ برتر " + age.DisplayName},
	}, session, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create competition = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	comps, err := st.Competitions(&season.ID, nil)
	if err != nil {
		t.Fatalf("competitions: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("fresh install created %d competitions, want 1", len(comps))
	}

	// 6. And the list now shows it — the flow is closed, not merely accepted.
	rec = adminBrowse(s.Handler(), "/admin/competitions", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/competitions after create = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), comps[0].DisplayName) {
		t.Errorf("the new competition %q is not on the list page", comps[0].DisplayName)
	}
}
