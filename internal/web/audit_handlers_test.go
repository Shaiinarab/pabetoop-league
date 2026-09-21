// Audit log UI tests (TASK-014): real store, real templates, admin session.
package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// badge is how the audit table renders one row's action label; counting it is a
// reliable row count (the filter <option> list uses different markup).
const auditRowBadge = `<span class="badge">`

func TestAuditListsKnownMutation(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	if _, err := st.CreateClub("نمونه ب نوین"); err != nil {
		t.Fatalf("create club: %v", err)
	}

	rec := adminGet(t, s, "/admin/audit")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/audit = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Persian action label, entity label, the changed value, and the admin.
	for _, want := range []string{"ایجاد", "باشگاه", "نمونه ب نوین", "admin", "گزارش تغییرات"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit page missing %q", want)
		}
	}
	// The filter form is a GET; nothing on this page mutates. (The one POST form
	// in the chrome is admin_base's logout control.)
	if strings.Contains(body, `method="post" action="/admin/audit"`) {
		t.Error("audit page must not post to the audit route")
	}
	if strings.Contains(body, "hx-post") {
		t.Error("audit page must not contain hx-post mutation wiring")
	}
	if !strings.Contains(body, `method="get" action="/admin/audit"`) {
		t.Error("the filter form should be a GET back to /admin/audit")
	}
}

func TestAuditFilters(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	clubID, err := st.CreateClub("نمونه پ")
	if err != nil {
		t.Fatalf("create club: %v", err)
	}
	if err := st.RenameClub(clubID, "نمونه پ نوین"); err != nil {
		t.Fatalf("rename club: %v", err)
	}

	// entity=club keeps club rows.
	body := adminGet(t, s, "/admin/audit?entity=club").Body.String()
	if !strings.Contains(body, "نمونه پ نوین") {
		t.Error("entity=club should include the club rename")
	}

	// action=insert excludes the update row.
	body = adminGet(t, s, "/admin/audit?action=insert").Body.String()
	if strings.Contains(body, auditRowBadge+"ویرایش</span>") {
		t.Error("action=insert must not render update rows")
	}
	if !strings.Contains(body, auditRowBadge+"ایجاد</span>") {
		t.Error("action=insert should render the insert row")
	}

	// action=update excludes the insert row.
	body = adminGet(t, s, "/admin/audit?action=update").Body.String()
	if strings.Contains(body, auditRowBadge+"ایجاد</span>") {
		t.Error("action=update must not render insert rows")
	}
	if !strings.Contains(body, "نمونه پ نوین") {
		t.Error("action=update should render the renamed value")
	}

	// The active filter is reported as selected.
	if !strings.Contains(body, `value="update" selected`) {
		t.Error("the active action filter should render as selected")
	}
}

func TestAuditPagination(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	const total = 60
	for i := 0; i < total; i++ {
		if _, err := st.CreateClub(fmt.Sprintf("باشگاه %d", i)); err != nil {
			t.Fatalf("create club %d: %v", i, err)
		}
	}

	body := adminGet(t, s, "/admin/audit").Body.String()
	if got := strings.Count(body, auditRowBadge); got != auditPageSize {
		t.Errorf("page 1 row count = %d, want %d", got, auditPageSize)
	}
	if !strings.Contains(body, "صفحهٔ بعد") || !strings.Contains(body, "page=2") {
		t.Error("page 1 should link to page 2")
	}
	if strings.Contains(body, "صفحهٔ قبل") {
		t.Error("page 1 must not offer a previous page")
	}

	body = adminGet(t, s, "/admin/audit?page=2").Body.String()
	if got := strings.Count(body, auditRowBadge); got != total-auditPageSize {
		t.Errorf("page 2 row count = %d, want %d", got, total-auditPageSize)
	}
	if !strings.Contains(body, "صفحهٔ قبل") {
		t.Error("page 2 should link back to page 1")
	}
	if strings.Contains(body, "صفحهٔ بعد") {
		t.Error("page 2 is the last page and must not offer a next page")
	}
}

// A page past the end still renders (first page) instead of an empty dead end.
func TestAuditPageBeyondEndFallsBack(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)
	if _, err := st.CreateClub("دیار حافظ"); err != nil {
		t.Fatalf("create club: %v", err)
	}

	rec := adminGet(t, s, "/admin/audit?page=99")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/audit?page=99 = %d, want 200", rec.Code)
	}
	if got := strings.Count(rec.Body.String(), auditRowBadge); got != 1 {
		t.Errorf("row count = %d, want 1 (clamped to the first page)", got)
	}
}

func TestAuditNeverLeaksHostPaths(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	secretDir := t.TempDir()
	dest := filepath.Join(secretDir, "snapshot-secret.db")
	if err := st.BackupTo(dest); err != nil {
		t.Fatalf("backup: %v", err)
	}

	body := adminGet(t, s, "/admin/audit?entity=database").Body.String()
	if strings.Contains(body, secretDir) {
		t.Error("audit page leaked an absolute host path")
	}
	if !strings.Contains(body, "snapshot-secret.db") {
		t.Error("audit page should show the snapshot base name")
	}
	if !strings.Contains(body, "پشتیبان‌گیری") {
		t.Error("audit page should label the backup action in Persian")
	}
}

// TASK-017: a filter that matches nothing renders the Persian empty state, not a
// zero-row table with dead pagination or an error page.
func TestAuditNoMatchFilterEmptyState(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)
	if _, err := st.CreateClub("کوروش"); err != nil {
		t.Fatalf("create club: %v", err)
	}

	rec := adminGet(t, s, "/admin/audit?entity=competition")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/audit?entity=competition = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "رویدادی با این فیلتر پیدا نشد.") {
		t.Error("a no-match filter should render the Persian empty state")
	}
	if got := strings.Count(body, auditRowBadge); got != 0 {
		t.Errorf("no-match filter rendered %d rows, want 0", got)
	}
	if strings.Contains(body, "صفحهٔ") {
		t.Error("a no-match filter must not render pagination controls")
	}
}

// TASK-017: ?page= must never 500 and never produce a negative offset.
func TestAuditInvalidPageIsFirstPage(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)
	if _, err := st.CreateClub("سعدی"); err != nil {
		t.Fatalf("create club: %v", err)
	}

	for _, target := range []string{"/admin/audit?page=abc", "/admin/audit?page=0", "/admin/audit?page=-3", "/admin/audit?page=", "/admin/audit?page=1.5"} {
		rec := adminGet(t, s, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
			continue
		}
		body := rec.Body.String()
		if strings.Contains(body, auditDataErrorMessage) {
			t.Errorf("GET %s rendered the Persian error page", target)
		}
		if got := strings.Count(body, auditRowBadge); got != 1 {
			t.Errorf("GET %s row count = %d, want 1 (falls back to page 1)", target, got)
		}
	}
}

// TASK-017: the paged view walks the log exactly once — no row repeated on two
// pages, none skipped. Club names are unique, so their appearance in the change
// column is a reliable per-row identity check.
func TestAuditPagesCoverEveryRowExactlyOnce(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	const total = 60
	for i := 0; i < total; i++ {
		if _, err := st.CreateClub(fmt.Sprintf("باشگاه %d", i)); err != nil {
			t.Fatalf("create club %d: %v", i, err)
		}
	}

	seen := map[int]int{}
	for _, page := range []int{1, 2} {
		body := adminGet(t, s, fmt.Sprintf("/admin/audit?page=%d", page)).Body.String()
		for i := 0; i < total; i++ {
			if strings.Contains(body, fmt.Sprintf("باشگاه %d</td>", i)) {
				seen[i]++
			}
		}
	}
	for i := 0; i < total; i++ {
		switch seen[i] {
		case 1: // exactly once: correct
		case 0:
			t.Errorf("club %d appears on no page — a row was dropped", i)
		default:
			t.Errorf("club %d appears on %d pages — a row was duplicated", i, seen[i])
		}
	}
}

// TASK-017 regression: the audit page used to filter and page in memory over a
// bounded 500-row fetch, so history past that window was unreachable. The oldest
// row must now be reachable by paging and the "only the last 500" note must not
// appear.
func TestAuditPagingReachesBeyondTheOldFetchWindow(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	const total = 520 // one full fetch window plus a tail
	for i := 0; i < total; i++ {
		if _, err := st.CreateClub(fmt.Sprintf("باشگاه %d", i)); err != nil {
			t.Fatalf("create club %d: %v", i, err)
		}
	}

	lastPage := (total + auditPageSize - 1) / auditPageSize
	body := adminGet(t, s, fmt.Sprintf("/admin/audit?page=%d", lastPage)).Body.String()

	wantRows := total - (lastPage-1)*auditPageSize
	if got := strings.Count(body, auditRowBadge); got != wantRows {
		t.Errorf("last page row count = %d, want %d", got, wantRows)
	}
	// The very first mutation (lowest id) is the oldest row and belongs here.
	if !strings.Contains(body, "باشگاه 0</td>") {
		t.Error("the oldest audit row is unreachable by paging — the fetch window is still capping the view")
	}
	if strings.Contains(body, "تنها ۵۰۰ رویداد آخر بارگذاری شده است") {
		t.Error("the page still claims only the last 500 events were loaded")
	}
}

func TestAuditHasNoMutationRoute(t *testing.T) {
	s, _ := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := adminRequest(t, s, method, "/admin/audit")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /admin/audit = %d, want 405 (audit log is read-only)", method, rec.Code)
		}
	}
}
