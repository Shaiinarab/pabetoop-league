# DATABASE — Schema, Integrity Rules, and Operations

Source of truth: `internal/store/migrations/` (versioned, embedded in the binary via `embed.FS`;
the migration runner applies files in filename order and tracks them in `schema_migrations`).
Currently `0001_init.sql` + `0002_competitions_premier_unique.sql` +
`0003_age_groups_seed.sql` + `0004_teams_age_group.sql`.

> **Migrations are append-only.** Never edit an applied file — add `NNNN_name.sql`. `0002` is the
> worked example: it exists because a nullable-column UNIQUE constraint silently did not enforce
> what the schema comment claimed (see the integrity table below and D15).

## Engine

**SQLite** via `modernc.org/sqlite` (pure Go — no cgo). Connection settings: `journal_mode=WAL`,
`foreign_keys=ON`, `busy_timeout=5000`. Growth path to PostgreSQL is documented in DECISIONS.md D1;
the store layer (`internal/store`, contract in `api.go`) is the only place SQL lives.

## Entity map

```
seasons ─┬─< competitions ─┬─< registrations >─ teams >─ clubs
         │                 └─< matches
         └─(one active)    age_groups (10..14, extensible table)
audit_log (every admin mutation)
```

- **Club ≠ Team** (PROJECT_SPEC §6/§7). `teams.display_name` is the official display form
  («نمونه ب ۲»); `clubs.name` is the official club name. Both authority-controlled (D7).
- `teams.age_group_id` (0004) is the age category the team was created for — nullable by design:
  rows that predate 0004 (and `cmd/seed`) stay NULL, and a NOT NULL column would abort
  `Migrate()` on the already-populated seeded database. Separate from a competition's own
  `age_group_id`; `UNIQUE (club_id, label)` still means one label per club, not per category.
- `registrations.club_id` is **denormalized** at registration time — historical integrity: the club
  a team represented never mutates retroactively (PROJECT_SPEC §47/§48).
- `competitions.level` ∈ {`premier`, `league1`}; `group_name` NULL iff premier (CHECK-enforced);
  `group_name` is free text — the number of League 1 groups is data, not schema (§5).

## Integrity rules — WHERE they live

| Rule (PROJECT_SPEC §7 — `§16` was a pre-consolidation brief number; see `PROJECT_SPEC` §11) | Mechanism | Verified |
|---|---|---|
| Premier: max 1 team per club per age category | `trg_premier_club_unique_insert/update` → RAISE(ABORT) «یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد» | ✅ trigger test |
| Both teams registered in the match's competition | `trg_match_teams_registered_insert/update` (+ friendlier pre-check in store layer) | ✅ trigger test |
| A team cannot play itself | `matches` CHECK `home_team_id != away_team_id` | ✅ |
| FINISHED ⇔ both scores present; SCHEDULED ⇔ none | `matches` CHECK | ✅ |
| Scores non-negative | `matches` CHECK (score ≥ 0) | ✅ |
| Duplicate fixture (competition, week, pairing) | partial UNIQUE index (`WHERE week IS NOT NULL`) | ✅ |
| Duplicate registration | UNIQUE (competition_id, team_id) | ✅ |
| Duplicate club name | UNIQUE (clubs.name) — different spellings stay different clubs (D7) | ✅ |
| Premier/league1 group_name shape | competitions CHECK | ✅ |
| **One Premier League per (season, age)** | partial UNIQUE index `idx_competitions_premier_unique ON competitions(season_id, age_group_id) WHERE level='premier'` (0002) | ✅ `TestCompetitionCreateDuplicatePremierRefused` + live |
| Score/state transitions, unregister-with-matches blocks | store layer validation (Persian messages) → covered by TASK-004 tests | via store |
| Audit on every admin mutation | store layer writes `audit_log` in the same transaction (D10) | via store |

**Why triggers, not CHECK subqueries:** SQLite CHECK constraints cannot contain subqueries.

**Age categories are seeded by 0003, not by the seed command.** `0001` creates the
`age_groups` table without rows and `EnsureAgeGroups` was only ever called by
`cmd/seed`, so a fresh install (`Migrate()` + `cmd/server`, no `SEED=1`) had an empty
category list — the operator could create a season and then find the competitions
form's «ردهٔ سنی» select empty. `0003` inserts the five fixed MVP categories
(10–14) with `INSERT OR IGNORE`, which is **required rather than defensive**: the
same migration runs on an existing seeded database, where a plain `INSERT` would hit
`UNIQUE(age)` and abort `Migrate()`, taking the server's boot with it. Verified live
the same day (`tmp/verify-fresh.sh`): a brand-new file gets 5 categories, and a
seeded file keeps exactly 5. `EnsureAgeGroups` remains the idempotent path used by
`cmd/seed` and stays safe on top of the migration.
The five labels must match `persianAgeLabel()` exactly («۱۲ سال») — asserted by
`TestFreshInstallSeedsAgeCategoriesFromMigrateAlone`.

**Why the Premier-League row needs a partial index (0002):** SQLite treats NULL values as *distinct
from each other* in a UNIQUE index, and premier rows carry `group_name = NULL`. So the table-level
`UNIQUE (season_id, age_group_id, level, group_name)` never fired for a second premier competition —
the rule looked enforced and was not. A partial unique index is the only correct mechanism.
**Lesson for any future nullable-column uniqueness rule: verify it with a test, do not read it off
the DDL.** (D15; found by a test, 2026-09-12.)
The Premier rule needs cross-row/cross-table logic, so it is a BEFORE INSERT/UPDATE trigger.

## Dates

Canonical storage: ISO Gregorian `TEXT` (`scheduled_date`, `seasons.starts_on/ends_on`).
All Jalali rendering/conversion is centralized in `internal/jalali` (D9). Times are plain
«HH:MM» text (spec A4 — Iran is single-offset +03:30; DST abolished 2022).

## Standings

Never stored. Computed on demand by `internal/standing` from FINISHED matches
(`store.FinishedMatches`), deterministic, tie-break isolated (D6).

## Verified 2026-09-11 (sqlite3, 8/8 scenarios)

> **Re-runnable** (closed by `TASK-036`; run it with `tools/db-scenarios.sh`):
>
> ```
> tools/db-scenarios.sh
> ```
>
> It builds a fresh DB from the committed migrations, replays the eight scenarios below and
> fails naming the one that drifted. The list below is the description of *what* is asserted;
> the script is the assertion.

1. premier duplicate-club → ABORT with Persian message ✅
2. different club → OK ✅
3. same club, multiple league1 teams → OK ✅
4. match with both teams registered → OK ✅
5. unregistered team in match → ABORT «هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند» ✅
6. finished with scores → OK; scheduled with a score → CHECK fail ✅
7. duplicate fixture same week → UNIQUE fail ✅
8. premier-with-group / league1-without-group → CHECK fail ✅

## Operations

- **Location:** `data/pabetoop-league.db` (git-ignored).
- **Backup:** store `BackupTo` (SQLite online backup / `VACUUM INTO`) → timestamped file;
  admin UI one-click download (§41/§73). CLI fallback: `sqlite3 data/pabetoop-league.db ".backup data/backup-$(date +%Y%m%d-%H%M%S).db"`.
- **Migrations:** apply automatically at startup; never edit an applied migration — add a new
  numbered file. `schema_migrations` is the ledger.
- **Inspect:** `sqlite3 data/pabetoop-league.db ".tables"`, `.schema matches`.
