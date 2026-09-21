// Backup workflow (TASK-014, PROJECT_SPEC §41/§73): one-click snapshot download
// plus a last-backup status line. Deliberately minimal — the administrator is a
// competition-office clerk, not a system administrator.
//
// Wiring contract (integration step owned by the Lead): Handler() in server.go
// (TASK-008's file — outside this task's allowlist) must call
// RegisterBackupAuditRoutes(mux) exactly once. Registering a pattern twice on one
// http.ServeMux panics, so do not also hand-register these paths.
//
// Routes:
//
//	GET /admin/backup          → backup.html (the page)
//	GET /admin/backup/download → fresh VACUUM INTO snapshot, streamed as an attachment
//	GET /admin/backup/status   → htmx fragment «backup_status» (refreshable)
//
// Route deviation from the brief (flagged in the report + NAG): the brief assigned
// the streaming response to GET /admin/backup, but admin_base.html renders
// /admin/backup as a navigation TAB and dashboard.html links it as the backup entry
// point. A navigation destination that instantly downloads a file is a UX bug, so
// the page owns /admin/backup and the stream lives at /admin/backup/download.
package web

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
)

const (
	backupPrefix = "pabetoop-league-backup-"
	backupSuffix = ".db"

	defaultBackupDir    = "data/backups"
	defaultBackupMarker = "data/last-backup.txt"

	backupFailedMessage = "پشتیبان‌گیری ممکن نشد؛ لطفاً دوباره تلاش کنید."
	adminGenericMessage = "خطایی رخ داد؛ لطفاً دوباره تلاش کنید."
)

// init registers the two pages this task owns with render.go's adminPageFiles
// registry. render.go belongs to TASK-010 and is outside TASK-014's allowlist, so
// the registration happens here instead of editing that file. Package init runs
// before any handler (and before adminPageSet caches anything), so the map is
// fully populated by the time a page is parsed.
func init() {
	adminPageFiles["backup"] = "admin/backup.html"
	adminPageFiles["audit"] = "admin/audit.html"
}

// ---- paths (env-configured; Config is TASK-008's frozen shape) ----

// backupDir is where snapshots are kept. Relative by default so the audit row
// store.BackupTo writes (action "backup") never contains an absolute host path.
func backupDir() string {
	if v := strings.TrimSpace(os.Getenv("BACKUP_DIR")); v != "" {
		return v
	}
	return defaultBackupDir
}

// backupMarkerFile is the last-backup marker read by TASK-010's dashboard
// (admin_handlers.lastBackupDate). Same env name and default, so the dashboard's
// «آخرین پشتیبان‌گیری» line and this page always agree.
func backupMarkerFile() string {
	if v := strings.TrimSpace(os.Getenv("BACKUP_MARKER_FILE")); v != "" {
		return v
	}
	return defaultBackupMarker
}

// backupFileName names the snapshot. Latin digits on purpose: the name travels
// through Content-Disposition, the filesystem and e-mail, where Persian digits are
// legal but need RFC 5987 encoding — the Persian rendering lives on the page
// instead. Jalali (not Gregorian) because the administrator thinks in the
// competition calendar, and fixed-width fields so names sort chronologically.
func backupFileName(t time.Time) string {
	jy, jm, jd := jalali.FromGregorian(t)
	return fmt.Sprintf("%s%04d-%02d-%02d-%02d%02d%02d%s",
		backupPrefix, jy, jm, jd, t.Hour(), t.Minute(), t.Second(), backupSuffix)
}

// uniqueBackupPath guarantees a non-existent target: SQLite's VACUUM INTO refuses
// to overwrite (two clicks inside one second would otherwise fail).
func uniqueBackupPath(dir, name string) string {
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	stem := strings.TrimSuffix(name, backupSuffix)
	for i := 2; ; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, backupSuffix))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// ---- view shapes (must match admin/backup.html) ----

// backupStatusView is the «backup_status» fragment's data.
type backupStatusView struct {
	HasBackup bool
	When      string // Persian Jalali date + clock
	Name      string
	SizeHuman string
}

type backupFileView struct {
	Name      string
	When      string
	SizeHuman string
}

type backupView struct {
	adminPage
	Status backupStatusView
	Files  []backupFileView
}

// ---- routes ----

// RegisterBackupAuditRoutes mounts the backup and audit surfaces. Both are
// admin-only and read-only (the load-bearing exception is the download, which
// creates a snapshot and an audit row).
func (s *Server) RegisterBackupAuditRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/backup", s.requireAdmin(http.HandlerFunc(s.handleBackupPage)))
	mux.Handle("GET /admin/backup/download", s.requireAdmin(http.HandlerFunc(s.handleBackupDownload)))
	mux.Handle("GET /admin/backup/status", s.requireAdmin(http.HandlerFunc(s.handleBackupStatus)))
	mux.Handle("GET /admin/audit", s.requireAdmin(http.HandlerFunc(s.handleAuditPage)))
}

// ---- handlers ----

func (s *Server) handleBackupPage(w http.ResponseWriter, r *http.Request) {
	setDynamicCache(w)
	page := s.backupChrome(w, r)
	page.Breadcrumb = []breadcrumbItem{{Label: "خانه", Href: "/admin"}, {Label: "پشتیبان‌گیری"}}
	v := backupView{adminPage: page, Status: s.backupStatus()}
	for _, f := range listBackups(backupDir()) {
		v.Files = append(v.Files, backupFileView{
			Name:      f.Name,
			When:      jalaliDateTime(f.ModifiedAt),
			SizeHuman: humanSize(f.SizeBytes),
		})
	}
	s.renderAdmin(w, r, "backup", v, http.StatusOK)
}

// handleBackupStatus serves the htmx fragment only (no page chrome).
func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	setDynamicCache(w)
	s.renderBackupPartial(w, "backup_status", s.backupStatus())
}

// handleBackupDownload creates a fresh consistent snapshot and streams it as an
// attachment. The snapshot is kept on disk so the status page has something real
// to report; its ISO date is written to the marker file the dashboard reads.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writePersianError(w, http.StatusServiceUnavailable, backupFailedMessage)
		return
	}

	dir := backupDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		s.log.Error("create backup dir", "dir", dir, "error", err)
		writePersianError(w, http.StatusInternalServerError, backupFailedMessage)
		return
	}

	now := time.Now()
	path := uniqueBackupPath(dir, backupFileName(now))
	if err := s.store.BackupTo(path); err != nil {
		s.log.Error("backup snapshot failed", "path", path, "error", err)
		writePersianError(w, http.StatusInternalServerError, backupFailedMessage)
		return
	}

	// Non-fatal: the snapshot exists and downloads even if the marker write fails;
	// only the dashboard's last-backup line would be stale.
	if err := writeBackupMarker(now); err != nil {
		s.log.Error("write backup marker", "marker", backupMarkerFile(), "error", err)
	}

	f, err := os.Open(path)
	if err != nil {
		s.log.Error("open backup snapshot", "path", path, "error", err)
		writePersianError(w, http.StatusInternalServerError, backupFailedMessage)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.log.Error("stat backup snapshot", "path", path, "error", err)
		writePersianError(w, http.StatusInternalServerError, backupFailedMessage)
		return
	}

	name := filepath.Base(path)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent handles range requests and sets Content-Length; the explicit
	// Content-Type above is preserved (it only sniffs when the header is unset).
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// ---- helpers ----

// backupChrome is adminChrome with a nil-store guard: the TASK-008 foundation
// tests build a store-less Server, and adminChrome would dereference it.
func (s *Server) backupChrome(w http.ResponseWriter, r *http.Request) adminPage {
	if s.store == nil {
		return adminPage{SiteName: SiteName, CSRFToken: s.CSRFToken(r), NavActive: "backup", Flash: takeFlash(w, r)}
	}
	return s.adminChrome(w, r, "backup")
}

// backupStatus reports the newest snapshot on disk (or the empty state).
func (s *Server) backupStatus() backupStatusView {
	latest, ok := latestBackup(backupDir())
	if !ok {
		return backupStatusView{}
	}
	return backupStatusView{
		HasBackup: true,
		When:      jalaliDateTime(latest.ModifiedAt),
		Name:      latest.Name,
		SizeHuman: humanSize(latest.SizeBytes),
	}
}

// backupFile is one snapshot on disk.
type backupFile struct {
	Name       string
	SizeBytes  int64
	ModifiedAt time.Time
}

// listBackups returns this app's snapshots, newest first. Only files matching our
// own prefix/suffix are considered, so an unrelated file in the directory is
// never listed (or offered for download).
func listBackups(dir string) []backupFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]backupFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), backupPrefix) || !strings.HasSuffix(e.Name(), backupSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, backupFile{Name: e.Name(), SizeBytes: info.Size(), ModifiedAt: info.ModTime()})
	}
	// Names are fixed-width Jalali timestamps, so descending name == newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// latestBackup returns the newest snapshot, or ok=false when none exists.
func latestBackup(dir string) (backupFile, bool) {
	files := listBackups(dir)
	if len(files) == 0 {
		return backupFile{}, false
	}
	return files[0], true
}

// writeBackupMarker records the snapshot date in the file TASK-010's dashboard
// reads (admin_handlers.lastBackupDate renders it through jalaliLong).
func writeBackupMarker(at time.Time) error {
	marker := backupMarkerFile()
	if dir := filepath.Dir(marker); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return os.WriteFile(marker, []byte(jalali.FormatISO(at)+"\n"), 0o600)
}

// renderBackupPartial executes a named template from the backup page set for the
// htmx status refresh. render.go's renderAdminPartial is bound to the "clubs"
// page set, so a backup-local equivalent is needed here.
func (s *Server) renderBackupPartial(w http.ResponseWriter, name string, data any) {
	pages, err := adminPageSet(s.templatesDir)
	if err != nil {
		s.log.Error("admin templates unavailable", "error", err, "partial", name)
		writePersianError(w, http.StatusInternalServerError, adminGenericMessage)
		return
	}
	tmpl, ok := pages["backup"]
	if !ok {
		s.log.Error("backup page set missing", "partial", name)
		writePersianError(w, http.StatusInternalServerError, adminGenericMessage)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render backup partial failed", "partial", name, "error", err)
		writePersianError(w, http.StatusInternalServerError, adminGenericMessage)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// jalaliDateTime renders date + 24h clock in Persian digits: «۱۴۰۵/۰۷/۲۰ ۱۴:۳۲».
// Iran is a single-offset zone (+03:30, DST abolished 2022 — spec A4), so the
// process-local clock is the correct wall-clock reading.
func jalaliDateTime(t time.Time) string {
	return jalali.Format(t) + " " + jalali.ToPersianDigits(fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute()))
}

// humanSize renders a byte count in Persian for the admin UI.
func humanSize(n int64) string {
	const unit = 1024
	switch {
	case n < unit:
		return jalali.ToPersianDigits(fmt.Sprintf("%d", n)) + " بایت"
	case n < unit*unit:
		return jalali.ToPersianDigits(fmt.Sprintf("%.1f", float64(n)/unit)) + " کیلوبایت"
	default:
		return jalali.ToPersianDigits(fmt.Sprintf("%.1f", float64(n)/(unit*unit))) + " مگابایت"
	}
}
