// Acceptance tests for the TASK-012 import pipeline (preview → confirm).
//
// The pipeline is the integrity gate for bulk fixture entry: nothing may reach
// the store before the administrator confirms, error rows must never import,
// and the confirm token is one-shot. These tests drive the real HTTP surface
// (`Server.Handler()`) with real multipart uploads and a real, migrated store,
// and assert the Persian text the operator actually reads.
//
// The store contract is asserted too, not just the HTML: after a confirm, the
// rows are looked up in the store, because "the preview looked right" is not
// evidence that the right fixture was written.
package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------- helpers ----------

// importUpload posts a file to /admin/import as the browser form does. A nil
// csrf omits both the field and the header, so the CSRF gate can be tested.
func importUpload(t *testing.T, h http.Handler, compID int64, filename, body string, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if csrf != nil {
		if err := mw.WriteField(csrfField, csrf.Value); err != nil {
			t.Fatalf("write csrf field: %v", err)
		}
	}
	if err := mw.WriteField("competition", strconv.FormatInt(compID, 10)); err != nil {
		t.Fatalf("write competition field: %v", err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte(body)); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if csrf != nil {
		req.Header.Set(csrfHeader, csrf.Value)
		req.AddCookie(csrf)
	}
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var (
	importTokenRe   = regexp.MustCompile(`name="token" value="([^"]+)"`)
	importResolveRe = regexp.MustCompile(`name="resolve_(\d+)_away"`)
)

// importToken reads the one-shot confirm token out of a preview response.
func importToken(t *testing.T, body string) string {
	t.Helper()
	m := importTokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("preview carries no confirm token: %s", body)
	}
	return m[1]
}

// resolveLine reads the file line number of the row whose away team needs a pick.
func resolveLine(t *testing.T, body string) string {
	t.Helper()
	m := importResolveRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("preview carries no away-team resolution field: %s", body)
	}
	return m[1]
}

// sampleCSV is the file the competition office would actually hand over: one
// clean row, one row whose away team is not a registered name, and one row that
// duplicates a fixture already in the programme.
func sampleCSV() string {
	return strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان,زمین",
		"۵,۱۴۰۵/۰۹/۰۱,نمونه ب ۱,نمونه ث ۱,زمین نمونه",
		"۵,۱۴۰۵/۰۹/۰۲,نمونه ب ۱,تیم ناشناخته,زمین نمونه",
		"۱,۱۴۰۵/۰۹/۰۳,نمونه ب ۱,نمونه پ ۱,زمین نمونه",
		"",
	}, "\n")
}

// ---------- page ----------

func TestImportPageRendersUploadForm(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/import", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/import = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="file"`, `action="/admin/import"`, "پیش‌نمایش"} {
		if !strings.Contains(body, want) {
			t.Errorf("import page missing %q", want)
		}
	}
}

// ---------- preview ----------

func TestImportPreviewSeparatesValidAndErrorRows(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	rec := importUpload(t, s.Handler(), comp.ID, "fixtures.csv", sampleCSV(), session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"۱ مسابقهٔ معتبر", // the one clean row
		"۲ خطا",           // the unknown name + the duplicate
		"شاید منظور شما",  // advisory suggestion (D7: never applied silently)
		`name="resolve_`,  // the administrator resolves by hand
		"از قبل در برنامهٔ همین مسابقات", // the duplicate is caught BEFORE confirm
		"نمونه پ ۱", // official names preserved byte-exact
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q", want)
		}
	}
	if got := importToken(t, body); len(got) < 16 {
		t.Errorf("confirm token looks too short to be random: %q", got)
	}
	// Nothing is written at preview time: seedTestData leaves exactly 3 matches.
	matches, err := st.Matches(comp.ID, nil, nil)
	if err != nil {
		t.Fatalf("matches: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("preview must not write anything: %d matches, want the 3 seeded", len(matches))
	}
}

// ---------- confirm ----------

func TestImportConfirmImportsOnlyValidRows(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	preview := importUpload(t, s.Handler(), comp.ID, "fixtures.csv", sampleCSV(), session, csrf)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200", preview.Code)
	}
	token := importToken(t, preview.Body.String())

	rec := adminMutate(s.Handler(), "/admin/import/confirm", url.Values{"token": {token}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "۱ مسابقه ثبت شد") {
		t.Errorf("result must report the imported count, got: %s", body)
	}
	if !strings.Contains(body, "۲ سطر وارد نشد") {
		t.Errorf("result must report the skipped rows, got: %s", body)
	}
	if !strings.Contains(body, "خط") {
		t.Errorf("skipped rows must be named by file line, got: %s", body)
	}

	// The clean row landed …
	if _, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[2].ID, 5); !ok {
		t.Error("the valid row was not imported")
	}
	// … the unknown-name row did not (errors never import) …
	teamsAll, err := st.Teams(nil)
	if err != nil {
		t.Fatalf("teams: %v", err)
	}
	for _, team := range teamsAll {
		if strings.Contains(team.DisplayName, "ناشناخته") {
			t.Errorf("the import created/renamed a team: %q (D7 forbids this)", team.DisplayName)
		}
	}
	// … and the duplicate did not add a second copy of the seeded week-1 fixture.
	if count := countInWeek(t, st, comp.ID, teams[0].ID, teams[1].ID, 1); count != 1 {
		t.Errorf("week 1 fixture stored %d times, want 1", count)
	}
}

func TestImportConfirmTokenIsOneShot(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	preview := importUpload(t, s.Handler(), comp.ID, "fixtures.csv", sampleCSV(), session, csrf)
	token := importToken(t, preview.Body.String())
	if rec := adminMutate(s.Handler(), "/admin/import/confirm", url.Values{"token": {token}}, session, csrf); rec.Code != http.StatusOK {
		t.Fatalf("first confirm = %d, want 200", rec.Code)
	}

	rec := adminMutate(s.Handler(), "/admin/import/confirm", url.Values{"token": {token}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200 with a Persian refusal", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "این پیش‌نمایش دیگر معتبر نیست") {
		t.Errorf("a replayed token must be refused in Persian, got: %s", rec.Body.String())
	}
	if count := countInWeek(t, st, comp.ID, teams[0].ID, teams[2].ID, 5); count != 1 {
		t.Errorf("the replay imported the row again (%d copies)", count)
	}
}

func TestImportConfirmAppliesAdminNameResolution(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان,زمین",
		"۶,۱۴۰۵/۰۹/۰۵,نمونه ب ۱,تیم ناشناخته,زمین نمونه",
		"",
	}, "\n")
	preview := importUpload(t, s.Handler(), comp.ID, "one.csv", csv, session, csrf)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200", preview.Code)
	}
	body := preview.Body.String()
	token := importToken(t, body)
	line := resolveLine(t, body)

	// The administrator picks the official team from the row's dropdown.
	rec := adminMutate(s.Handler(), "/admin/import/confirm", url.Values{
		"token":                     {token},
		"resolve_" + line + "_away": {strconv.FormatInt(teams[2].ID, 10)},
	}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "۱ مسابقه ثبت شد") {
		t.Errorf("resolved row must import, got: %s", rec.Body.String())
	}
	if _, ok := matchInWeek(t, st, comp.ID, teams[0].ID, teams[2].ID, 6); !ok {
		t.Error("the resolved row was not stored against the picked official team")
	}
	if teamsAll, err := st.Teams(nil); err == nil {
		for _, team := range teamsAll {
			if strings.Contains(team.DisplayName, "ناشناخته") {
				t.Errorf("resolution must not create or rename a team, found %q", team.DisplayName)
			}
		}
	}
}

// ---------- CSRF / auth ----------

func TestImportMutationsRequireCSRF(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, _ := seedTestData(t, st)
	session, _ := loginTestAdmin(t, s)

	up := importUpload(t, s.Handler(), comp.ID, "fixtures.csv", sampleCSV(), session, nil)
	if up.Code != http.StatusForbidden {
		t.Errorf("upload without a CSRF token = %d, want 403", up.Code)
	}
	confirm := adminMutate(s.Handler(), "/admin/import/confirm", url.Values{"token": {"whatever"}}, session, nil)
	if confirm.Code != http.StatusForbidden {
		t.Errorf("confirm without a CSRF token = %d, want 403", confirm.Code)
	}
}

func TestImportAnonymousBlocked(t *testing.T) {
	s, st := newAdminTestServer(t)
	seedTestData(t, st)

	rec := adminBrowse(s.Handler(), "/admin/import", nil)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous GET /admin/import = %d, want 303 to the login page", rec.Code)
	}
}

// A store failure mid-batch must be reported per row, never as a silent partial
// import. The duplicate row that races with the confirm step is the reachable
// case: the preview said "valid", the store says "already in the programme".
func TestImportConfirmReportsRowsTheStoreRejects(t *testing.T) {
	s, st := newAdminTestServer(t)
	_, _, comp, teams := seedTestData(t, st)
	session, csrf := loginTestAdmin(t, s)

	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان,زمین",
		"۷,۱۴۰۵/۰۹/۰۷,نمونه ب ۱,نمونه ث ۱,زمین نمونه",
		"",
	}, "\n")
	preview := importUpload(t, s.Handler(), comp.ID, "race.csv", csv, session, csrf)
	token := importToken(t, preview.Body.String())

	// The fixture appears between preview and confirm (another admin, another tab).
	if _, err := st.CreateMatch(comp.ID, teams[0].ID, teams[2].ID, intPtr(7), stringPtr("2026-11-28"), nil, nil); err != nil {
		t.Fatalf("seed racing fixture: %v", err)
	}

	rec := adminMutate(s.Handler(), "/admin/import/confirm", url.Values{"token": {token}}, session, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "این مسابقه از قبل در برنامه ثبت شده است") {
		t.Errorf("the row the store rejected must be reported by line, got: %s", body)
	}
	if count := countInWeek(t, st, comp.ID, teams[0].ID, teams[2].ID, 7); count != 1 {
		t.Errorf("fixture stored %d times, want 1", count)
	}
}
