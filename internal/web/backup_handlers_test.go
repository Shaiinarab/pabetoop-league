// Backup handler tests (TASK-014): real store, real templates, admin session.
package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// newFileBackedTestServer mirrors newPublicTestServer but keeps the database on
// disk: the backup path (VACUUM INTO) and the snapshot re-open are only faithful
// against a real file rather than a shared in-memory database.
func newFileBackedTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewWithConfig(st, testConfig(t)), st
}

// adminRequest performs an authenticated request against the routes this task
// registers. Only these routes plus the session/CSRF layer matter here, so the
// mux is built directly instead of going through Server.Handler().
func adminRequest(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.RegisterBackupAuditRoutes(mux)

	req := httptest.NewRequest(method, target, nil)
	issue := httptest.NewRecorder()
	s.sessions.issue(issue, req, true)
	cookies := issue.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("session issue produced no cookie")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// adminGet is the authenticated GET shorthand used by the TASK-014 tests.
func adminGet(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	return adminRequest(t, s, http.MethodGet, target)
}

// isolatedBackupEnv points both backup paths at temp dirs for this test.
func isolatedBackupEnv(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	marker = filepath.Join(t.TempDir(), "last-backup.txt")
	t.Setenv("BACKUP_DIR", dir)
	t.Setenv("BACKUP_MARKER_FILE", marker)
	return dir, marker
}

// ---------- access control ----------

func TestBackupAuditRoutesRequireAdmin(t *testing.T) {
	s, _ := newPublicTestServer(t)

	mux := http.NewServeMux()
	s.RegisterBackupAuditRoutes(mux)

	for _, path := range []string{"/admin/backup", "/admin/backup/download", "/admin/backup/status", "/admin/audit"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusSeeOther {
			t.Errorf("anonymous GET %s = %d, want 303", path, rec.Code)
			continue
		}
		if loc := rec.Header().Get("Location"); loc != adminLoginPath {
			t.Errorf("anonymous GET %s redirects to %q, want %q", path, loc, adminLoginPath)
		}
	}
}

// ---------- page ----------

func TestBackupPageEmptyStateAndGuidance(t *testing.T) {
	s, _ := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	rec := adminGet(t, s, "/admin/backup")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/backup = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		"هنوز پشتیبانی گرفته نشده است.",    // empty state (spec §73)
		"این فایل را در جای امن نگه دارید", // guidance text required by the brief
		`href="/admin/backup/download"`,    // the one big action
		`id="backup-status"`,               // htmx refresh target
		`href="/admin/audit"`,              // route to the audit page
		`hx-get="/admin/backup/status"`,    // refresh wiring
	} {
		if !strings.Contains(body, want) {
			t.Errorf("backup page missing %q", want)
		}
	}
	// The page must not leak a host path (only file names).
	if strings.Contains(body, t.TempDir()) {
		t.Error("backup page leaked an absolute path")
	}
}

// ---------- download ----------

func TestBackupDownloadStreamsValidSQLite(t *testing.T) {
	s, st := newFileBackedTestServer(t)
	seedTestData(t, st) // 3 clubs, 3 teams, 3 matches
	dir, marker := isolatedBackupEnv(t)

	rec := adminGet(t, s, "/admin/backup/download")
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	// Attachment contract.
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, ".db") {
		t.Errorf("Content-Disposition = %q, want an attachment with a .db name", cd)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("SQLite format 3\x00")) {
		t.Fatalf("downloaded bytes are not a SQLite database (%d bytes)", len(body))
	}

	// The snapshot must open as a real DB holding the same data.
	snap := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(snap, body, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	opened, err := store.Open(snap)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer opened.Close()
	clubs, err := opened.Clubs(true)
	if err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if len(clubs) != 3 {
		t.Errorf("snapshot has %d clubs, want 3", len(clubs))
	}

	// Kept on disk so the status page can report it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup dir has %d files, want 1", len(entries))
	}
	if !strings.HasPrefix(entries[0].Name(), backupPrefix) {
		t.Errorf("snapshot name %q lacks the %q prefix", entries[0].Name(), backupPrefix)
	}

	// Marker for TASK-010's dashboard line.
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\s*$`).Match(raw) {
		t.Errorf("marker content = %q, want an ISO date (jalaliLong input)", strings.TrimSpace(string(raw)))
	}

	// Store contract: the snapshot is recorded in the audit log.
	audit, err := st.AuditLog(5)
	if err != nil {
		t.Fatalf("audit log: %v", err)
	}
	if len(audit) == 0 || audit[0].Action != "backup" {
		t.Errorf("newest audit row = %+v, want action %q", audit, "backup")
	}
}

func TestBackupDownloadSecondCallDoesNotCollide(t *testing.T) {
	s, _ := newFileBackedTestServer(t)
	dir, _ := isolatedBackupEnv(t)

	for i := 0; i < 2; i++ {
		if rec := adminGet(t, s, "/admin/backup/download"); rec.Code != http.StatusOK {
			t.Fatalf("download #%d = %d, want 200 (VACUUM INTO refuses to overwrite)", i+1, rec.Code)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("backup dir has %d files, want 2 (unique names)", len(entries))
	}
}

// ---------- status fragment ----------

func TestBackupStatusPartialEmptyThenFilled(t *testing.T) {
	s, _ := newFileBackedTestServer(t)
	dir, _ := isolatedBackupEnv(t)

	// Empty state first.
	rec := adminGet(t, s, "/admin/backup/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "هنوز پشتیبانی گرفته نشده است.") {
		t.Error("empty status should show the Persian empty state")
	}

	// After a download, the fragment shows the Jalali timestamp + size.
	if rec := adminGet(t, s, "/admin/backup/download"); rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	rec = adminGet(t, s, "/admin/backup/status")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(body, "آخرین پشتیبان") {
		t.Error("filled status should name the last backup")
	}
	if strings.Contains(body, "هنوز پشتیبانی گرفته نشده است.") {
		t.Error("filled status must not show the empty state")
	}
	// The timestamp must be the snapshot's real Jalali date, not just any digits.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("read backup dir: %v (%d files)", err, len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	want := jalaliDateTime(info.ModTime())
	if !strings.Contains(body, want) {
		t.Errorf("status should show the snapshot's Jalali timestamp %q, got: %s", want, body)
	}
	// Persian digits from the Jalali formatter, and the swap target survives.
	if !strings.Contains(body, "۱۴") {
		t.Errorf("status should render Persian Jalali digits, got: %s", body)
	}
	if !strings.Contains(body, `id="backup-status"`) {
		t.Error("fragment must carry #backup-status so outerHTML swaps keep the target")
	}
	// The fragment is a fragment: no page chrome.
	if strings.Contains(body, "<html") || strings.Contains(body, "class=\"topbar\"") {
		t.Error("status partial must not render the full admin page")
	}
}

func TestBackupPageListsSnapshots(t *testing.T) {
	s, _ := newFileBackedTestServer(t)
	isolatedBackupEnv(t)

	if rec := adminGet(t, s, "/admin/backup/download"); rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	rec := adminGet(t, s, "/admin/backup")
	body := rec.Body.String()
	if !strings.Contains(body, backupPrefix) {
		t.Error("page should list the snapshot file name")
	}
	if strings.Contains(body, "هنوز پشتیبانی گرفته نشده است.") {
		t.Error("page should not show the empty state once a snapshot exists")
	}
	if !strings.Contains(body, "کیلوبایت") && !strings.Contains(body, "بایت") && !strings.Contains(body, "مگابایت") {
		t.Error("page should show the snapshot size")
	}
}
