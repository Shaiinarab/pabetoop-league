// Package store implements the SQLite data layer for the platform (TASK-004).
//
// It satisfies the DataStore contract in api.go exactly:
//
//   - Driver: modernc.org/sqlite (pure Go, no cgo — DECISIONS.md D1), WAL mode,
//     foreign_keys=ON, busy_timeout=5000 (applied per-connection via DSN
//     pragmas; the migration files' PRAGMA lines are handled the same way —
//     journal_mode cannot change inside a transaction, so Migrate() strips
//     PRAGMA lines and applies connection-level settings itself).
//   - Migrations: embedded via embed.FS from migrations/*.sql, applied in
//     filename order, tracked in schema_migrations, idempotent on reopen.
//   - Every mutating method runs in one transaction and writes an audit_log
//     row (D10). DB triggers (premier-club uniqueness, match-registration
//     checks) act as backstop behind friendlier service-level validation.
//   - All user-triggerable validation errors are Persian (PROJECT_SPEC §70);
//     sentinels below carry the exact required texts, wrapped with context.
//   - Dates are opaque ISO Gregorian strings here; internal/jalali owns all
//     calendar conversion (D9).
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (D1)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Persian validation sentinels (exact required texts). Callers match with
// errors.Is; the store wraps them with entity context via wrap()/%w.
var (
	ErrDuplicateName   = errors.New("باشگاهی با این نام قبلاً ثبت شده است")
	ErrPremierClubOnce = errors.New("یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد")
	ErrNegativeScore   = errors.New("نتیجه نمی‌تواند منفی باشد")
	ErrNotScheduled    = errors.New("نتیجه فقط برای مسابقه‌های برنامه‌ریزی‌شده قابل ثبت است")
	ErrNotFinished     = errors.New("پاک کردن نتیجه فقط برای مسابقه‌های تمام‌شده ممکن است")
	ErrHasMatches      = errors.New("این تیم در این مسابقات بازی ثبت‌شده دارد؛ حذف ثبت‌نام ممکن نیست")
	ErrBothTeamsNeeded = errors.New("هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند")
	ErrSelfMatch       = errors.New("یک تیم نمی‌تواند با خودش بازی کند")
	ErrNotFound        = errors.New("مورد یافت نشد")
	ErrActiveRefs      = errors.New("این مورد در داده‌های فعال استفاده شده است؛ غیرفعال‌سازی ممکن نیست")
	ErrInUse           = errors.New("این مورد در حال استفاده است")
	ErrBadLevel        = errors.New("سطح مسابقات نامعتبر است")
	ErrBadGroup        = errors.New("گروه برای این سطح نامعتبر است")
	ErrBadWeek         = errors.New("هفته باید عددی بزرگ‌تر از صفر باشد")
)

// wrappedError adds entity context to a sentinel while keeping errors.Is.
type wrappedError struct {
	msg string
	err error
}

func (e *wrappedError) Error() string { return e.msg }
func (e *wrappedError) Unwrap() error { return e.err }

func wrap(err error, format string, args ...any) error {
	return &wrappedError{msg: fmt.Sprintf(format, args...), err: err}
}

// Store is the SQLite-backed implementation of DataStore.
type Store struct {
	db *sql.DB
}

// compile-time proof that *Store satisfies the contract.
var _ DataStore = (*Store)(nil)

const dsnPragmas = "_pragma=foreign_keys(1)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=synchronous(NORMAL)"

// Open opens (creating if needed) the SQLite database at path with WAL mode,
// foreign keys, and a 5s busy timeout. Call Migrate to apply the schema.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&" + dsnPragmas
	return openDSN(dsn)
}

// OpenMemory opens a named shared in-memory database (tools/tests).
// (WAL does not apply to in-memory databases, so it is not requested here.)
func OpenMemory(name string) (*Store, error) {
	dsn := "file:" + name + "?mode=memory&cache=shared&" + dsnPragmas
	return openDSN(dsn)
}

func openDSN(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("باز کردن پایگاه داده ممکن نشد: %w", err)
	}
	// WAL handles concurrent readers; the engine enforces single-writer.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s := &Store{db: db}
	if err := s.db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("اتصال به پایگاه داده ممکن نشد: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---------- migrations ----------

// Migrate applies embedded migrations in filename order. Idempotent: applied
// versions are recorded in schema_migrations and skipped on reopen.
//
// PRAGMA lines inside migration files are stripped — connection-level pragmas
// are already applied via DSN in Open (journal_mode cannot change inside a
// transaction anyway).
func (s *Store) Migrate() error {
	if err := s.ensureMigrationsTable(); err != nil {
		return err
	}
	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("خواندن فایل‌های مهاجرت ممکن نشد: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("خواندن مهاجرت %s ممکن نشد: %w", name, err)
		}
		if err := s.applyMigration(name, stripPragmas(string(raw))); err != nil {
			return fmt.Errorf("اعمال مهاجرت %s ناموفق بود: %w", name, err)
		}
	}
	return nil
}

func (s *Store) ensureMigrationsTable() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	return err
}

func (s *Store) appliedVersions() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// stripPragmas removes single-line PRAGMA statements from a migration body;
// DSN-level pragmas cover them.
func stripPragmas(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(strings.ToUpper(trimmed), "PRAGMA") && strings.HasSuffix(trimmed, ";") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func (s *Store) applyMigration(version, body string) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// No bound args: the driver executes the whole multi-statement script.
	if _, err := tx.Exec(body); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- small helpers ----------

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func toJson(v any) *string {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// audit writes an audit_log row inside the current transaction (D10).
// created_at uses the DB default (uniform SQLite datetime text), which
// AuditLog parses — the driver does not auto-convert text to time.Time.
func audit(tx *sql.Tx, action, entity string, entityID int64, before, after *string) error {
	_, err := tx.Exec(
		`INSERT INTO audit_log (action, entity, entity_id, before, after, admin)
		 VALUES (?, ?, ?, ?, ?, 'admin')`,
		action, entity, entityID, before, after,
	)
	return err
}

func optStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func optIntPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func optInt64Ptr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func scanStrPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func intFromPtr(ns sql.NullInt64) *int {
	if !ns.Valid {
		return nil
	}
	v := int(ns.Int64)
	return &v
}

func int64FromPtr(ns sql.NullInt64) *int64 {
	if !ns.Valid {
		return nil
	}
	v := ns.Int64
	return &v
}

// isUniqueErr reports whether err is a UNIQUE-constraint failure (message
// shapes vary by driver version; match defensively). Must be checked BEFORE
// constraintText, which matches any "constraint failed" text.
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// constraintText reports a SQLite RAISE(ABORT, …) constraint failure and
// passes the trigger's Persian message through to the caller.
func constraintText(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if strings.Contains(err.Error(), "constraint failed") {
		return err.Error(), true
	}
	return "", false
}

// toPersianDigits renders Latin digits as Persian digits (labels only —
// stored data stays canonical; D7).
func toPersianDigits(s string) string {
	return strings.NewReplacer(
		"0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴",
		"5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹",
	).Replace(s)
}

func persianAgeLabel(age int) string {
	return toPersianDigits(strconv.Itoa(age)) + " سال"
}

// ---------- seasons ----------

func (s *Store) CreateSeason(name string, startsOn, endsOn *string) (int64, error) {
	var id int64
	err := s.withTx(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO seasons (name, starts_on, ends_on, is_active) VALUES (?, ?, ?, 0)`,
			name, optStr(startsOn), optStr(endsOn),
		)
		if err != nil {
			if isUniqueErr(err) {
				return wrap(ErrDuplicateName, "فصلی با نام «%s» قبلاً ثبت شده است", name)
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		after := toJson(map[string]any{"name": name, "starts_on": startsOn, "ends_on": endsOn})
		return audit(tx, "insert", "season", id, nil, after)
	})
	return id, err
}

func (s *Store) UpdateSeason(id int64, name string, startsOn, endsOn *string) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var oldName string
		var oldStart, oldEnd sql.NullString
		err := tx.QueryRow(`SELECT name, starts_on, ends_on FROM seasons WHERE id = ?`, id).
			Scan(&oldName, &oldStart, &oldEnd)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "فصل با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE seasons SET name = ?, starts_on = ?, ends_on = ? WHERE id = ?`,
			name, optStr(startsOn), optStr(endsOn), id); err != nil {
			if isUniqueErr(err) {
				return wrap(ErrDuplicateName, "فصلی با نام «%s» قبلاً ثبت شده است", name)
			}
			return err
		}
		before := toJson(map[string]any{"name": oldName, "starts_on": scanStrPtr(oldStart), "ends_on": scanStrPtr(oldEnd)})
		after := toJson(map[string]any{"name": name, "starts_on": startsOn, "ends_on": endsOn})
		return audit(tx, "update", "season", id, before, after)
	})
}

func (s *Store) ActivateSeason(id int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM seasons WHERE id = ?)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return wrap(ErrNotFound, "فصل با شناسه %d یافت نشد", id)
		}
		if _, err := tx.Exec(`UPDATE seasons SET is_active = 0 WHERE is_active = 1`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE seasons SET is_active = 1 WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(tx, "update", "season", id, nil, toJson(map[string]any{"is_active": true}))
	})
}

const seasonCols = `id, name, starts_on, ends_on, is_active`

func seasonFromRow(scan func(dest ...any) error) (*Season, error) {
	var se Season
	var active int64
	var start, end sql.NullString
	if err := scan(&se.ID, &se.Name, &start, &end, &active); err != nil {
		return nil, err
	}
	se.StartsOn = scanStrPtr(start)
	se.EndsOn = scanStrPtr(end)
	se.IsActive = active == 1
	return &se, nil
}

func (s *Store) ActiveSeason() (*Season, error) {
	row := s.db.QueryRow(`SELECT ` + seasonCols + ` FROM seasons WHERE is_active = 1 LIMIT 1`)
	se, err := seasonFromRow(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // no active season is a valid state
	}
	return se, err
}

func (s *Store) Seasons() ([]Season, error) {
	rows, err := s.db.Query(`SELECT ` + seasonCols + ` FROM seasons ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Season{}
	for rows.Next() {
		se, err := seasonFromRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *se)
	}
	return out, rows.Err()
}

// ---------- age groups ----------

func (s *Store) EnsureAgeGroups(ages ...int) error {
	sorted := append([]int(nil), ages...)
	sort.Ints(sorted)
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		for _, age := range sorted {
			var exists bool
			if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM age_groups WHERE age = ?)`, age).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			res, err := tx.Exec(
				`INSERT INTO age_groups (age, display_name, sort_order) VALUES (?, ?, ?)`,
				age, persianAgeLabel(age), age,
			)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if err := audit(tx, "insert", "age_group", id, nil,
				toJson(map[string]any{"age": age, "display_name": persianAgeLabel(age)})); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) AgeGroups() ([]AgeGroup, error) {
	rows, err := s.db.Query(`SELECT id, age, display_name FROM age_groups ORDER BY sort_order, age`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgeGroup{}
	for rows.Next() {
		var ag AgeGroup
		if err := rows.Scan(&ag.ID, &ag.Age, &ag.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, ag)
	}
	return out, rows.Err()
}

// ---------- clubs ----------

func (s *Store) CreateClub(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, wrap(ErrInUse, "نام باشگاه نمی‌تواند خالی باشد")
	}
	var id int64
	err := s.withTx(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO clubs (name) VALUES (?)`, name)
		if err != nil {
			if isUniqueErr(err) {
				return wrap(ErrDuplicateName, "باشگاهی با این نام قبلاً ثبت شده است: %s", name)
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit(tx, "insert", "club", id, nil, toJson(map[string]any{"name": name}))
	})
	return id, err
}

const clubCols = `id, name, is_active`

func clubFromRow(scan func(dest ...any) error) (Club, error) {
	var c Club
	var active int64
	if err := scan(&c.ID, &c.Name, &active); err != nil {
		return c, err
	}
	c.IsActive = active == 1
	return c, nil
}

func (s *Store) Clubs(includeInactive bool) ([]Club, error) {
	q := `SELECT ` + clubCols + ` FROM clubs`
	if !includeInactive {
		q += ` WHERE is_active = 1`
	}
	q += ` ORDER BY name COLLATE NOCASE`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Club{}
	for rows.Next() {
		c, err := clubFromRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RenameClub(id int64, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return wrap(ErrInUse, "نام باشگاه نمی‌تواند خالی باشد")
	}
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var oldName string
		err := tx.QueryRow(`SELECT name FROM clubs WHERE id = ?`, id).Scan(&oldName)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "باشگاه با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clubs SET name = ? WHERE id = ?`, newName, id); err != nil {
			if isUniqueErr(err) {
				return wrap(ErrDuplicateName, "باشگاهی با این نام قبلاً ثبت شده است: %s", newName)
			}
			return err
		}
		return audit(tx, "update", "club", id,
			toJson(map[string]any{"name": oldName}),
			toJson(map[string]any{"name": newName}))
	})
}

func (s *Store) DeactivateClub(id int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var name string
		var active bool
		if err := tx.QueryRow(`SELECT name, is_active FROM clubs WHERE id = ?`, id).Scan(&name, &active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wrap(ErrNotFound, "باشگاه با شناسه %d یافت نشد", id)
			}
			return err
		}
		// Blocked while any team of this club still holds a registration
		// (contract: active team + active registrations).
		var n int64
		if err := tx.QueryRow(`
			SELECT COUNT(*)
			FROM registrations r
			JOIN teams t ON t.id = r.team_id
			WHERE t.club_id = ? AND t.is_active = 1
		`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return wrap(ErrActiveRefs,
				"باشگاه «%s» تیم ثبت‌نام‌شده فعال دارد؛ ابتدا ثبت‌نام‌ها را حذف کنید", name)
		}
		if _, err := tx.Exec(`UPDATE clubs SET is_active = 0 WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(tx, "update", "club", id, nil, toJson(map[string]any{"is_active": false}))
	})
}

// ---------- teams ----------

// CreateTeam creates a team without an age category (age_group_id NULL). It is
// the frozen DataStore contract, kept for callers that have no category in hand
// (cmd/seed). The admin form requires one — use CreateTeamWithAgeGroup.
func (s *Store) CreateTeam(clubID int64, label, displayName string) (int64, error) {
	return s.CreateTeamWithAgeGroup(clubID, nil, label, displayName)
}

// CreateTeamWithAgeGroup is CreateTeam plus the age category the admin form
// requires (PROJECT_SPEC §27 identity: club + age category + group). A nil
// ageGroupID keeps age_group_id NULL (historic rows, cmd/seed).
func (s *Store) CreateTeamWithAgeGroup(clubID int64, ageGroupID *int64, label, displayName string) (int64, error) {
	label = strings.TrimSpace(label)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return 0, wrap(ErrInUse, "نام نمایشی تیم نمی‌تواند خالی باشد")
	}
	var id int64
	err := s.withTx(context.Background(), func(tx *sql.Tx) error {
		var clubActive bool
		err := tx.QueryRow(`SELECT is_active FROM clubs WHERE id = ?`, clubID).Scan(&clubActive)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "باشگاه با شناسه %d یافت نشد", clubID)
		}
		if err != nil {
			return err
		}
		if ageGroupID != nil {
			// Friendly Persian failure instead of the raw FK error.
			var ageOK bool
			if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM age_groups WHERE id = ?)`, *ageGroupID).Scan(&ageOK); err != nil {
				return err
			}
			if !ageOK {
				return wrap(ErrNotFound, "رده سنی با شناسه %d یافت نشد", *ageGroupID)
			}
		}
		res, err := tx.Exec(
			`INSERT INTO teams (club_id, label, display_name, age_group_id) VALUES (?, ?, ?, ?)`,
			clubID, label, displayName, optInt64Ptr(ageGroupID),
		)
		if err != nil {
			if isUniqueErr(err) {
				return wrap(ErrInUse, "باشگاه قبلاً تیمی با برچسب «%s» دارد", label)
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit(tx, "insert", "team", id, nil,
			toJson(map[string]any{"club_id": clubID, "label": label, "display_name": displayName, "age_group_id": ageGroupID}))
	})
	return id, err
}

const teamCols = `t.id, t.club_id, c.name, t.label, t.display_name, t.is_active, t.age_group_id, g.display_name`

// teamFrom is the join shared by every team read, so the age-category join can
// never drift between Teams and TeamsPage. It is a LEFT JOIN because a team with
// no category (cmd/seed, pre-0004 rows) must still be returned, and because
// age_groups.id is the primary key the join can never multiply a row.
const teamFrom = ` FROM teams t JOIN clubs c ON c.id = t.club_id LEFT JOIN age_groups g ON g.id = t.age_group_id`

func teamFromRow(scan func(dest ...any) error) (Team, error) {
	var t Team
	var active int64
	var ageID sql.NullInt64
	var ageName sql.NullString
	if err := scan(&t.ID, &t.ClubID, &t.ClubName, &t.Label, &t.DisplayName, &active, &ageID, &ageName); err != nil {
		return t, err
	}
	t.IsActive = active == 1
	t.AgeGroupID = int64FromPtr(ageID)
	t.AgeGroupName = ageName.String
	return t, nil
}

// TeamByID returns one team by primary key, including its joined club name and
// age category. Unknown id → ErrNotFound in Persian, never a bare sql.ErrNoRows.
func (s *Store) TeamByID(id int64) (Team, error) {
	row := s.db.QueryRow(`SELECT `+teamCols+teamFrom+` WHERE t.id = ?`, id)
	t, err := teamFromRow(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, wrap(ErrNotFound, "تیم با شناسه %d یافت نشد", id)
	}
	if err != nil {
		return Team{}, err
	}
	return t, nil
}

func (s *Store) Teams(clubID *int64) ([]Team, error) {
	q := `SELECT ` + teamCols + teamFrom
	args := []any{}
	if clubID != nil {
		q += ` WHERE t.club_id = ?`
		args = append(args, *clubID)
	}
	q += ` ORDER BY c.name COLLATE NOCASE, t.label COLLATE NOCASE`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		t, err := teamFromRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TeamsPage returns one page of teams plus the total number of teams matching
// the filter. Clamps mirror AuditLogFiltered. The page query and the count
// share one WHERE construction, so the total can never disagree with the rows,
// and the ordering is exactly Teams()'s — paging can neither drop nor duplicate
// a row.
func (s *Store) TeamsPage(clubID *int64, limit, offset int) ([]Team, int, error) {
	if limit <= 0 {
		limit = teamsPageDefaultLimit
	}
	if limit > teamsPageMaxLimit {
		limit = teamsPageMaxLimit
	}
	if offset < 0 {
		offset = 0
	}

	where := ""
	args := []any{}
	if clubID != nil {
		where = " WHERE t.club_id = ?"
		args = append(args, *clubID)
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams t`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pageArgs := make([]any, 0, len(args)+2)
	pageArgs = append(pageArgs, args...)
	pageArgs = append(pageArgs, limit, offset)
	rows, err := s.db.Query(`SELECT `+teamCols+teamFrom+where+
		` ORDER BY c.name COLLATE NOCASE, t.label COLLATE NOCASE LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		t, err := teamFromRow(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

const (
	teamsPageDefaultLimit = 50
	teamsPageMaxLimit     = 500
)

// UpdateTeam is the admin edit path (TASK-037). It is the same shape as
// CreateTeamWithAgeGroup — trim, refuse an empty display name, refuse an unknown
// category in Persian — plus the audit requirement: the before/after payload
// carries **every** field the call can change, including the category, so a row
// that lost its category is visible in the audit log rather than silent.
//
// The club is deliberately not writable here: a team's club is the anchor of
// its display name and history, so moving one is a deactivate-and-recreate, not
// an edit. `teams.UNIQUE (club_id, label)` therefore cannot be crossed by this
// call in a way the create path could not already produce.
func (s *Store) UpdateTeam(id int64, label, displayName string, ageGroupID *int64) error {
	label = strings.TrimSpace(label)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return wrap(ErrInUse, "نام نمایشی تیم نمی‌تواند خالی باشد")
	}
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var oldLabel, oldDisplay string
		var oldAge sql.NullInt64
		err := tx.QueryRow(`SELECT label, display_name, age_group_id FROM teams WHERE id = ?`, id).
			Scan(&oldLabel, &oldDisplay, &oldAge)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "تیم با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}

		// nil means "leave the category unchanged" (see DataStore.UpdateTeam):
		// resolve it to a concrete value up front so the UPDATE and the audit
		// row always describe the same state.
		newAge := oldAge
		if ageGroupID != nil {
			var ageOK bool
			if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM age_groups WHERE id = ?)`, *ageGroupID).Scan(&ageOK); err != nil {
				return err
			}
			if !ageOK {
				return wrap(ErrNotFound, "رده سنی با شناسه %d یافت نشد", *ageGroupID)
			}
			newAge = sql.NullInt64{Int64: *ageGroupID, Valid: true}
		}

		if _, err := tx.Exec(`UPDATE teams SET label = ?, display_name = ?, age_group_id = ? WHERE id = ?`,
			label, displayName, optInt64Ptr(int64FromPtr(newAge)), id); err != nil {
			if isUniqueErr(err) {
				return wrap(ErrInUse, "باشگاه قبلاً تیمی با برچسب «%s» دارد", label)
			}
			return err
		}
		return audit(tx, "update", "team", id,
			toJson(map[string]any{"label": oldLabel, "display_name": oldDisplay, "age_group_id": int64FromPtr(oldAge)}),
			toJson(map[string]any{"label": label, "display_name": displayName, "age_group_id": int64FromPtr(newAge)}))
	})
}

func (s *Store) DeactivateTeam(id int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var display string
		if err := tx.QueryRow(`SELECT display_name FROM teams WHERE id = ?`, id).Scan(&display); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wrap(ErrNotFound, "تیم با شناسه %d یافت نشد", id)
			}
			return err
		}
		var n int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM registrations WHERE team_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return wrap(ErrActiveRefs, "تیم «%s» ثبت‌نام فعال دارد؛ ابتدا ثبت‌نام را حذف کنید", display)
		}
		if _, err := tx.Exec(`UPDATE teams SET is_active = 0 WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(tx, "update", "team", id, nil, toJson(map[string]any{"is_active": false}))
	})
}

// ---------- competitions ----------

func (s *Store) CreateCompetition(seasonID, ageGroupID int64, level string, groupName *string, displayName string) (int64, error) {
	switch level {
	case "premier":
		if groupName != nil {
			return 0, wrap(ErrBadGroup, "لیگ برتر گروه ندارد؛ گروه «%s» برای لیگ برتر مجاز نیست", *groupName)
		}
	case "league1":
		if groupName == nil || strings.TrimSpace(*groupName) == "" {
			return 0, wrap(ErrBadGroup, "برای لیگ یک، نام گروه الزامی است (مثلاً «A»)")
		}
	default:
		return 0, wrap(ErrBadLevel, "سطح «%s» شناخته نشد؛ سطح باید premier یا league1 باشد", level)
	}
	var id int64
	err := s.withTx(context.Background(), func(tx *sql.Tx) error {
		var seasonOK, groupOK bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM seasons WHERE id = ?)`, seasonID).Scan(&seasonOK); err != nil {
			return err
		}
		if !seasonOK {
			return wrap(ErrNotFound, "فصل با شناسه %d یافت نشد", seasonID)
		}
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM age_groups WHERE id = ?)`, ageGroupID).Scan(&groupOK); err != nil {
			return err
		}
		if !groupOK {
			return wrap(ErrNotFound, "رده سنی با شناسه %d یافت نشد", ageGroupID)
		}
		res, err := tx.Exec(
			`INSERT INTO competitions (season_id, age_group_id, level, group_name, display_name) VALUES (?, ?, ?, ?, ?)`,
			seasonID, ageGroupID, level, optStr(groupName), displayName,
		)
		if err != nil {
			if isUniqueErr(err) {
				// Premier collapses to one row per season+age via the partial index
				// in 0002 (the table UNIQUE cannot see NULL group_name), so it
				// deserves its own wording rather than the generic duplicate text.
				if level == "premier" {
					return wrap(ErrInUse, "برای این رده سنی در این فصل، لیگ برتر قبلاً ساخته شده است")
				}
				return wrap(ErrInUse, "این مسابقات قبلاً ثبت شده است (فصل/رده/سطح/گروه تکراری است)")
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit(tx, "insert", "competition", id, nil,
			toJson(map[string]any{"season_id": seasonID, "age_group_id": ageGroupID, "level": level, "group_name": groupName, "display_name": displayName}))
	})
	return id, err
}

const competitionCols = `
	c.id, c.season_id, s.name, c.age_group_id, ag.age,
	c.level, c.group_name, c.display_name,
	(SELECT COUNT(*) FROM registrations r WHERE r.competition_id = c.id)`

const competitionFrom = `
	FROM competitions c
	JOIN seasons s ON s.id = c.season_id
	JOIN age_groups ag ON ag.id = c.age_group_id`

func competitionFromRow(scan func(dest ...any) error) (*Competition, error) {
	var c Competition
	var group sql.NullString
	if err := scan(&c.ID, &c.SeasonID, &c.SeasonName, &c.AgeGroupID, &c.AgeGroupAge,
		&c.Level, &group, &c.DisplayName, &c.Registrations); err != nil {
		return nil, err
	}
	c.GroupName = scanStrPtr(group)
	return &c, nil
}

func (s *Store) Competition(id int64) (*Competition, error) {
	row := s.db.QueryRow(`SELECT `+competitionCols+` `+competitionFrom+` WHERE c.id = ?`, id)
	c, err := competitionFromRow(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wrap(ErrNotFound, "مسابقات با شناسه %d یافت نشد", id)
	}
	return c, err
}

func (s *Store) Competitions(seasonID, ageGroupID *int64) ([]Competition, error) {
	q := `SELECT ` + competitionCols + ` ` + competitionFrom + ` WHERE 1=1`
	args := []any{}
	if seasonID != nil {
		q += ` AND c.season_id = ?`
		args = append(args, *seasonID)
	}
	if ageGroupID != nil {
		q += ` AND c.age_group_id = ?`
		args = append(args, *ageGroupID)
	}
	q += ` ORDER BY c.age_group_id, c.level, c.group_name`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Competition{}
	for rows.Next() {
		c, err := competitionFromRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// RenameCompetition updates the authority-controlled display name and, for
// league1, the group label. Official names are stored exactly as typed (D7):
// whitespace is trimmed, never normalised. Premier has no group by schema, so a
// group passed for a premier competition is refused rather than silently
// dropped, and a league1 competition is never left without one.
func (s *Store) RenameCompetition(id int64, displayName string, groupName *string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return wrap(ErrInUse, "نام مسابقات نمی‌تواند خالی باشد")
	}
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var level, oldDisplay string
		var oldGroup sql.NullString
		err := tx.QueryRow(`SELECT level, display_name, group_name FROM competitions WHERE id = ?`, id).
			Scan(&level, &oldDisplay, &oldGroup)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقات با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}
		newGroup := groupName
		if newGroup != nil {
			trimmed := strings.TrimSpace(*newGroup)
			newGroup = &trimmed
		}
		switch level {
		case "premier":
			if newGroup != nil && *newGroup != "" {
				return wrap(ErrBadGroup, "لیگ برتر گروه ندارد؛ گروه «%s» برای لیگ برتر مجاز نیست", *newGroup)
			}
			newGroup = nil
		default: // league1 (level is CHECK-constrained to premier|league1)
			if newGroup == nil || *newGroup == "" {
				return wrap(ErrBadGroup, "برای لیگ یک، نام گروه الزامی است (مثلاً «A»)")
			}
		}
		if _, err := tx.Exec(`UPDATE competitions SET display_name = ?, group_name = ? WHERE id = ?`,
			displayName, optStr(newGroup), id); err != nil {
			if isUniqueErr(err) {
				return wrap(ErrInUse, "مسابقات دیگری با همین فصل، رده، سطح و گروه وجود دارد")
			}
			return err
		}
		return audit(tx, "update", "competition", id,
			toJson(map[string]any{"display_name": oldDisplay, "group_name": scanStrPtr(oldGroup)}),
			toJson(map[string]any{"display_name": displayName, "group_name": newGroup}))
	})
}

// DeleteCompetition removes a competition, but only while it is empty. A
// registration or a match blocks the delete (DATABASE.md: deletions that would
// break history are refused — the operator unregisters/clears first).
// Competitions carry no is_active column, so a blocked delete is the whole
// story rather than a deactivate fallback.
func (s *Store) DeleteCompetition(id int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var display string
		err := tx.QueryRow(`SELECT display_name FROM competitions WHERE id = ?`, id).Scan(&display)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقات با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}
		var regs, matches int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM registrations WHERE competition_id = ?`, id).Scan(&regs); err != nil {
			return err
		}
		if regs > 0 {
			return wrap(ErrActiveRefs,
				"مسابقات «%s» %s ثبت‌نام دارد؛ تا حذف ثبت‌نام‌ها امکان حذف نیست",
				display, toPersianDigits(strconv.FormatInt(regs, 10)))
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM matches WHERE competition_id = ?`, id).Scan(&matches); err != nil {
			return err
		}
		if matches > 0 {
			return wrap(ErrHasMatches,
				"مسابقات «%s» %s مسابقه دارد؛ حذف ممکن نیست",
				display, toPersianDigits(strconv.FormatInt(matches, 10)))
		}
		if _, err := tx.Exec(`DELETE FROM competitions WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(tx, "delete", "competition", id,
			toJson(map[string]any{"display_name": display}), nil)
	})
}

// ---------- registrations ----------

func (s *Store) Register(competitionID, teamID int64) (int64, error) {
	var id int64
	err := s.withTx(context.Background(), func(tx *sql.Tx) error {
		// Friendly pre-checks before the DB trigger backstop fires.
		var clubID int64
		var teamDisplay string
		var teamActive bool
		err := tx.QueryRow(`SELECT club_id, display_name, is_active FROM teams WHERE id = ?`, teamID).
			Scan(&clubID, &teamDisplay, &teamActive)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "تیم با شناسه %d یافت نشد", teamID)
		}
		if err != nil {
			return err
		}
		if !teamActive {
			return wrap(ErrActiveRefs, "تیم «%s» غیرفعال است؛ ثبت‌نام ممکن نیست", teamDisplay)
		}
		var level, compDisplay string
		err = tx.QueryRow(`SELECT level, display_name FROM competitions WHERE id = ?`, competitionID).
			Scan(&level, &compDisplay)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقات با شناسه %d یافت نشد", competitionID)
		}
		if err != nil {
			return err
		}
		var dup bool
		if err := tx.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM registrations WHERE competition_id = ? AND team_id = ?)`,
			competitionID, teamID,
		).Scan(&dup); err != nil {
			return err
		}
		if dup {
			return wrap(ErrInUse, "تیم «%s» قبلاً در «%s» ثبت‌نام کرده است", teamDisplay, compDisplay)
		}
		if level == "premier" {
			var clubName string
			_ = tx.QueryRow(`SELECT name FROM clubs WHERE id = ?`, clubID).Scan(&clubName)
			var clubHasTeam bool
			if err := tx.QueryRow(`
				SELECT EXISTS (
					SELECT 1
					FROM registrations r
					JOIN competitions c ON c.id = r.competition_id
					WHERE r.competition_id = ? AND c.level = 'premier' AND r.club_id = ?
				)`, competitionID, clubID).Scan(&clubHasTeam); err != nil {
				return err
			}
			if clubHasTeam {
				return wrap(ErrPremierClubOnce,
					"باشگاه «%s» قبلاً در «%s» تیم دارد؛ %s",
					clubName, compDisplay, ErrPremierClubOnce.Error())
			}
		}
		res, err := tx.Exec(
			`INSERT INTO registrations (competition_id, team_id, club_id) VALUES (?, ?, ?)`,
			competitionID, teamID, clubID,
		)
		if err != nil {
			if isUniqueErr(err) {
				return wrap(ErrInUse, "تیم «%s» قبلاً در «%s» ثبت‌نام کرده است", teamDisplay, compDisplay)
			}
			// Trigger backstop — pass the DB's own Persian text through.
			if txt, ok := constraintText(err); ok {
				return wrap(ErrPremierClubOnce, "%s (زمینه: «%s»)", txt, compDisplay)
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit(tx, "insert", "registration", id, nil,
			toJson(map[string]any{"competition_id": competitionID, "team_id": teamID, "club_id": clubID}))
	})
	return id, err
}

func (s *Store) Unregister(registrationID int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var compID, teamID int64
		err := tx.QueryRow(`SELECT competition_id, team_id FROM registrations WHERE id = ?`, registrationID).
			Scan(&compID, &teamID)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "ثبت‌نام با شناسه %d یافت نشد", registrationID)
		}
		if err != nil {
			return err
		}
		var n int64
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM matches WHERE competition_id = ? AND (home_team_id = ? OR away_team_id = ?)`,
			compID, teamID, teamID,
		).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			var teamDisplay string
			_ = tx.QueryRow(`SELECT display_name FROM teams WHERE id = ?`, teamID).Scan(&teamDisplay)
			return wrap(ErrHasMatches,
				"تیم «%s» در این مسابقات %s بازی ثبت‌شده دارد؛ حذف ثبت‌نام ممکن نیست",
				teamDisplay, toPersianDigits(strconv.FormatInt(n, 10)))
		}
		if _, err := tx.Exec(`DELETE FROM registrations WHERE id = ?`, registrationID); err != nil {
			return err
		}
		return audit(tx, "delete", "registration", registrationID,
			toJson(map[string]any{"competition_id": compID, "team_id": teamID}), nil)
	})
}

func (s *Store) Registrations(competitionID int64) ([]Registration, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.competition_id, r.team_id, r.club_id,
		       t.display_name, c.name
		FROM registrations r
		JOIN teams t ON t.id = r.team_id
		JOIN clubs c ON c.id = r.club_id
		WHERE r.competition_id = ?
		ORDER BY c.name COLLATE NOCASE, t.display_name
	`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registration{}
	for rows.Next() {
		var r Registration
		if err := rows.Scan(&r.ID, &r.CompetitionID, &r.TeamID, &r.ClubID, &r.TeamName, &r.ClubName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RegisteredTeamIDs(competitionID int64) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT team_id FROM registrations WHERE competition_id = ?`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ---------- matches ----------

func validateWeek(week *int) error {
	if week != nil && *week < 1 {
		return wrap(ErrBadWeek, "هفته باید عددی بزرگ‌تر از صفر باشد (دریافت‌شده: %s)", toPersianDigits(strconv.Itoa(*week)))
	}
	return nil
}

func (s *Store) CreateMatch(competitionID, homeTeamID, awayTeamID int64, week *int, scheduledDate, scheduledTime, venue *string) (int64, error) {
	if homeTeamID == awayTeamID {
		return 0, wrap(ErrSelfMatch, "یک تیم نمی‌تواند با خودش بازی کند")
	}
	if err := validateWeek(week); err != nil {
		return 0, err
	}
	var id int64
	err := s.withTx(context.Background(), func(tx *sql.Tx) error {
		// Friendly pre-check with team names before the DB trigger backstop.
		var compDisplay string
		err := tx.QueryRow(`SELECT display_name FROM competitions WHERE id = ?`, competitionID).Scan(&compDisplay)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقات با شناسه %d یافت نشد", competitionID)
		}
		if err != nil {
			return err
		}
		var registered int64
		if err := tx.QueryRow(`
			SELECT COUNT(DISTINCT team_id) FROM registrations
			WHERE competition_id = ? AND team_id IN (?, ?)
		`, competitionID, homeTeamID, awayTeamID).Scan(&registered); err != nil {
			return err
		}
		if registered < 2 {
			missing := ""
			for _, tid := range []int64{homeTeamID, awayTeamID} {
				var display string
				var inComp bool
				if err := tx.QueryRow(`
					SELECT (SELECT display_name FROM teams WHERE id = ?),
					       EXISTS (SELECT 1 FROM registrations WHERE competition_id = ? AND team_id = ?)
				`, tid, competitionID, tid).Scan(&display, &inComp); err != nil {
					return err
				}
				if !inComp {
					if missing != "" {
						missing += " و "
					}
					if display == "" {
						missing += fmt.Sprintf("«شناسه %d»", tid)
					} else {
						missing += "«" + display + "»"
					}
				}
			}
			return wrap(ErrBothTeamsNeeded, "تیم(های) %s در «%s» ثبت‌نام ندارند", missing, compDisplay)
		}
		res, err := tx.Exec(`
			INSERT INTO matches (competition_id, home_team_id, away_team_id, week, scheduled_date, scheduled_time, venue, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'scheduled')
		`, competitionID, homeTeamID, awayTeamID, optIntPtr(week), optStr(scheduledDate), optStr(scheduledTime), optStr(venue))
		if err != nil {
			if isUniqueErr(err) {
				return wrap(ErrInUse, "این بازی قبلاً در برنامه ثبت شده است (تکراری)")
			}
			// Trigger backstop — pass the DB's own Persian text through.
			if txt, ok := constraintText(err); ok {
				return wrap(ErrBothTeamsNeeded, "%s", txt)
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit(tx, "insert", "match", id, nil,
			toJson(map[string]any{"competition_id": competitionID, "home": homeTeamID, "away": awayTeamID, "week": week}))
	})
	return id, err
}

const matchCols = `
	m.id, m.competition_id, m.home_team_id, m.away_team_id,
	h.display_name, a.display_name,
	m.week, m.scheduled_date, m.scheduled_time, m.venue,
	m.status, m.home_score, m.away_score, m.notes`

const matchFrom = `
	FROM matches m
	JOIN teams h ON h.id = m.home_team_id
	JOIN teams a ON a.id = m.away_team_id`

func matchFromRow(scan func(dest ...any) error) (Match, error) {
	var m Match
	var week sql.NullInt64
	var date, tm, venue, notes sql.NullString
	var homeScore, awayScore sql.NullInt64
	if err := scan(&m.ID, &m.CompetitionID, &m.HomeTeamID, &m.AwayTeamID,
		&m.HomeTeamName, &m.AwayTeamName,
		&week, &date, &tm, &venue,
		&m.Status, &homeScore, &awayScore, &notes); err != nil {
		return m, err
	}
	m.Week = intFromPtr(week)
	m.ScheduledDate = scanStrPtr(date)
	m.ScheduledTime = scanStrPtr(tm)
	m.Venue = scanStrPtr(venue)
	m.HomeScore = intFromPtr(homeScore)
	m.AwayScore = intFromPtr(awayScore)
	m.Notes = scanStrPtr(notes)
	return m, nil
}

func (s *Store) Match(id int64) (*Match, error) {
	row := s.db.QueryRow(`SELECT `+matchCols+` `+matchFrom+` WHERE m.id = ?`, id)
	m, err := matchFromRow(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wrap(ErrNotFound, "مسابقه با شناسه %d یافت نشد", id)
	}
	return &m, err
}

func (s *Store) Matches(competitionID int64, week, status *string) ([]Match, error) {
	q := `SELECT ` + matchCols + ` ` + matchFrom + ` WHERE m.competition_id = ?`
	args := []any{competitionID}
	if week != nil {
		// week arrives as string per the contract; SQLite's column affinity
		// converts the text to an integer for comparison with m.week.
		q += ` AND m.week = ?`
		args = append(args, *week)
	}
	if status != nil {
		q += ` AND m.status = ?`
		args = append(args, *status)
	}
	q += ` ORDER BY COALESCE(m.week, 0), m.scheduled_date, m.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Match{}
	for rows.Next() {
		m, err := matchFromRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SetResult(matchID int64, homeScore, awayScore int) error {
	if homeScore < 0 || awayScore < 0 {
		return wrap(ErrNegativeScore, "%s (%s-%s)",
			ErrNegativeScore.Error(),
			toPersianDigits(strconv.Itoa(homeScore)),
			toPersianDigits(strconv.Itoa(awayScore)))
	}
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRow(`SELECT status FROM matches WHERE id = ?`, matchID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقه با شناسه %d یافت نشد", matchID)
		}
		if err != nil {
			return err
		}
		if status != "scheduled" {
			return wrap(ErrNotScheduled, "%s (وضعیت فعلی: %s)", ErrNotScheduled.Error(), status)
		}
		var before string
		_ = tx.QueryRow(`SELECT json_object('status', status) FROM matches WHERE id = ?`, matchID).Scan(&before)
		if _, err := tx.Exec(
			`UPDATE matches SET status = 'finished', home_score = ?, away_score = ?, updated_at = ? WHERE id = ?`,
			homeScore, awayScore, nowUTC(), matchID,
		); err != nil {
			return err
		}
		after := toJson(map[string]any{"status": "finished", "home_score": homeScore, "away_score": awayScore})
		return audit(tx, "update", "match", matchID, &before, after)
	})
}

func (s *Store) ClearResult(matchID int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var status string
		var hs, as sql.NullInt64
		err := tx.QueryRow(`SELECT status, home_score, away_score FROM matches WHERE id = ?`, matchID).
			Scan(&status, &hs, &as)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقه با شناسه %d یافت نشد", matchID)
		}
		if err != nil {
			return err
		}
		if status != "finished" {
			return wrap(ErrNotFinished, "%s (وضعیت فعلی: %s)", ErrNotFinished.Error(), status)
		}
		if _, err := tx.Exec(
			`UPDATE matches SET status = 'scheduled', home_score = NULL, away_score = NULL, updated_at = ? WHERE id = ?`,
			nowUTC(), matchID,
		); err != nil {
			return err
		}
		before := toJson(map[string]any{"status": "finished", "home_score": hs.Int64, "away_score": as.Int64})
		return audit(tx, "update", "match", matchID, before, toJson(map[string]any{"status": "scheduled"}))
	})
}

func (s *Store) UpdateMatchSchedule(id int64, week *int, scheduledDate, scheduledTime, venue *string) error {
	if err := validateWeek(week); err != nil {
		return err
	}
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		var oldW sql.NullInt64
		var oldD, oldT, oldV sql.NullString
		err := tx.QueryRow(`SELECT week, scheduled_date, scheduled_time, venue FROM matches WHERE id = ?`, id).
			Scan(&oldW, &oldD, &oldT, &oldV)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقه با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE matches SET week = ?, scheduled_date = ?, scheduled_time = ?, venue = ?, updated_at = ? WHERE id = ?`,
			optIntPtr(week), optStr(scheduledDate), optStr(scheduledTime), optStr(venue), nowUTC(), id,
		); err != nil {
			if isUniqueErr(err) {
				return wrap(ErrInUse, "این بازی قبلاً در برنامه ثبت شده است (تکراری)")
			}
			return err
		}
		before := toJson(map[string]any{"week": intFromPtr(oldW), "date": scanStrPtr(oldD), "time": scanStrPtr(oldT), "venue": scanStrPtr(oldV)})
		after := toJson(map[string]any{"week": week, "date": scheduledDate, "time": scheduledTime, "venue": venue})
		return audit(tx, "update", "match", id, before, after)
	})
}

func (s *Store) DeleteMatch(id int64) error {
	return s.withTx(context.Background(), func(tx *sql.Tx) error {
		row := tx.QueryRow(`SELECT `+matchCols+` `+matchFrom+` WHERE m.id = ?`, id)
		m, err := matchFromRow(row.Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(ErrNotFound, "مسابقه با شناسه %d یافت نشد", id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM matches WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(tx, "delete", "match", id,
			toJson(map[string]any{"home": m.HomeTeamName, "away": m.AwayTeamName, "status": m.Status}), nil)
	})
}

func (s *Store) MatchCount(competitionID int64) (total, finished int, err error) {
	err = s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'finished' THEN 1 ELSE 0 END), 0)
		FROM matches WHERE competition_id = ?
	`, competitionID).Scan(&total, &finished)
	return
}

func (s *Store) FinishedMatches(competitionID int64) ([]FinishedMatch, error) {
	rows, err := s.db.Query(`
		SELECT home_team_id, away_team_id, home_score, away_score
		FROM matches
		WHERE competition_id = ? AND status = 'finished'
		ORDER BY id
	`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FinishedMatch{}
	for rows.Next() {
		var f FinishedMatch
		if err := rows.Scan(&f.HomeTeamID, &f.AwayTeamID, &f.HomeGoals, &f.AwayGoals); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------- audit ----------

// parseSQLiteTime parses the two timestamp shapes that can appear in
// audit_log.created_at: the DB default ('2006-01-02 15:04:05', UTC) and
// RFC3339 (legacy rows).
func parseSQLiteTime(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func (s *Store) AuditLog(limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(auditSelect+`
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuditRows(rows)
}

// auditSelect is the one projection every audit query shares, so a new column
// cannot be added to one query and forgotten in the other.
const auditSelect = `
	SELECT id, action, entity, COALESCE(entity_id, 0), before, after, admin, created_at
	FROM audit_log`

// AuditLogFiltered clamps per the api.go contract and performs exactly two
// reads that share one WHERE construction, so the page and its total can never
// disagree about which rows match. Parameterised SQL only.
func (s *Store) AuditLogFiltered(action, entity string, limit, offset int) ([]AuditEntry, int, error) {
	if limit <= 0 {
		limit = auditFilteredDefaultLimit
	}
	if limit > auditFilteredMaxLimit {
		limit = auditFilteredMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	where, args := auditFilterClause(action, entity)

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pageArgs := make([]any, 0, len(args)+2)
	pageArgs = append(pageArgs, args...)
	pageArgs = append(pageArgs, limit, offset)
	// id is the primary key, so id DESC is total: newest first and deterministic
	// — pagination can neither drop nor duplicate a row.
	rows, err := s.db.Query(auditSelect+where+`
		ORDER BY id DESC
		LIMIT ? OFFSET ?
	`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := scanAuditRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

const (
	auditFilteredDefaultLimit = 50
	auditFilteredMaxLimit     = 500
)

// auditFilterClause builds the shared "WHERE …" fragment for the page query and
// the count query. Empty action/entity means "any"; values match exactly.
func auditFilterClause(action, entity string) (string, []any) {
	conds := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if action != "" {
		conds = append(conds, "action = ?")
		args = append(args, action)
	}
	if entity != "" {
		conds = append(conds, "entity = ?")
		args = append(args, entity)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// scanAuditRows drains an audit_log result set into entries, parsing both
// timestamp shapes the table can hold.
func scanAuditRows(rows *sql.Rows) ([]AuditEntry, error) {
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var before, after, createdAt sql.NullString
		if err := rows.Scan(&e.ID, &e.Action, &e.Entity, &e.EntityID, &before, &after, &e.Admin, &createdAt); err != nil {
			return nil, err
		}
		e.Before = scanStrPtr(before)
		e.After = scanStrPtr(after)
		if createdAt.Valid {
			t, err := parseSQLiteTime(createdAt.String)
			if err != nil {
				return nil, fmt.Errorf("زمان ثبت در گزارش تغییرات قابل خواندن نیست: %w", err)
			}
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- backup ----------

// BackupTo produces a consistent snapshot via VACUUM INTO (SQLite 3.27+;
// the driver ships newer). The destination file must not already exist.
func (s *Store) BackupTo(destPath string) error {
	if _, err := s.db.Exec(`VACUUM INTO ?`, destPath); err != nil {
		return fmt.Errorf("پشتیبان‌گیری ممکن نشد: %w", err)
	}
	_ = s.withTx(context.Background(), func(tx *sql.Tx) error {
		return audit(tx, "backup", "database", 0, nil, toJson(map[string]any{"dest": destPath}))
	})
	return nil
}
