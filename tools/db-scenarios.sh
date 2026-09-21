#!/usr/bin/env bash
# db-scenarios.sh — replay DATABASE.md's 8 sqlite3 scenarios, and fail on drift.
#
# WHY (docs/OPEN-WORK.md V3)
#   DATABASE.md claims "Verified 2026-09-11 (sqlite3, 8/8 scenarios)", but the
#   scenarios were a manual procedure with no runner: nobody could repeat them,
#   and a migration change that quietly dropped a trigger, CHECK or UNIQUE would
#   not be caught. This makes the documented behaviour one command.
#
# WHAT IT DOES
#   Builds a fresh, throwaway database from the COMMITTED migrations
#   (internal/store/migrations/*.sql, in filename order), seeds a small fixed
#   domain, then replays each scenario on its own copy of that database. Every
#   scenario asserts on observable output — the refusal text, the constraint
#   named, the row count — never on "an error occurred": a scenario that stops
#   erroring goes red with the scenario named.
#
#   Nothing under the shared tree is written. The database and its WAL files live
#   in a scratch dir under the workspace .openclaw/tmp/, removed by a trap.
#   The shared tree is only read (migrations) — this script cannot mutate it.
#
# OFFLINE
#   sqlite3 + coreutils only (POSIX bash, no jq/python/node), no network.
#
# USAGE
#   tools/db-scenarios.sh          # run all eight; exit 0 only if all behave as documented
#
# Exit: 0 all 8 scenarios behaved as documented · 1 drift found (a scenario named) · 2 cannot run
#
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
WORKSPACE="$(cd "$ROOT/../.." && pwd -P)"
MIGRATIONS="$ROOT/internal/store/migrations"

SCRATCH_BASE="${DB_SCENARIOS_TMPDIR:-$WORKSPACE/.openclaw/tmp}"
SCRATCH=""
DB=""

cleanup() { [ -n "${SCRATCH:-}" ] && [ -d "$SCRATCH" ] && rm -rf "$SCRATCH"; return 0; }
trap cleanup EXIT INT TERM HUP

NO_PROXY="${NO_PROXY:-127.0.0.1,localhost}"
no_proxy="$NO_PROXY"
export NO_PROXY no_proxy

command -v sqlite3 >/dev/null 2>&1 || { printf 'db-scenarios: sqlite3 not on PATH\n' >&2; exit 2; }
[ -d "$MIGRATIONS" ] || { printf 'db-scenarios: no migrations at %s\n' "$MIGRATIONS" >&2; exit 2; }

# ---------------------------------------------------------------- assertion helpers

# sqlrun <sql> — sets OUT (combined output) and RC. -bail makes the first error a
# non-zero exit, so an expected refusal is observable as RC != 0.
sqlrun() {
    OUT="$(sqlite3 -bail -batch "$DB" "PRAGMA foreign_keys=ON; $1" 2>&1)"
    RC=$?
}

fail() { printf '  FAIL %s\n         %s\n' "$1" "$2" >&2; }

# check_ok <name> <sql> — the statement must succeed.
check_ok() {
    sqlrun "$2"
    if [ "$RC" -ne 0 ]; then
        fail "$1" "expected success, got a refusal: $(printf '%s' "$OUT" | head -1)"
        return 1
    fi
    return 0
}

# check_refused <name> <needle> <sql> — must fail AND name the documented reason.
check_refused() {
    sqlrun "$3"
    if [ "$RC" -eq 0 ]; then
        fail "$1" "expected a refusal, but the statement succeeded (drift)"
        return 1
    fi
    case "$OUT" in
        *"$2"*) return 0 ;;
        *) fail "$1" "refused, but not with the documented text; got: $(printf '%s' "$OUT" | head -1)"
           return 1 ;;
    esac
}

# check_eq <name> <expected> <sql> — single-value SELECT must equal <expected>.
check_eq() {
    sqlrun "$3"
    if [ "$RC" -ne 0 ]; then
        fail "$1" "query failed: $(printf '%s' "$OUT" | head -1)"
        return 1
    fi
    if [ "$(printf '%s' "$OUT" | tr -d '\r')" != "$2" ]; then
        fail "$1" "expected «$2», got «$OUT»"
        return 1
    fi
    return 0
}

pass() { printf '  PASS %s\n' "$1"; }

# ---------------------------------------------------------------- fresh DB

MIGRATIONS_APPLIED=0

build_base_db() {
    local m
    rm -f "$SCRATCH"/base.db "$SCRATCH"/base.db-wal "$SCRATCH"/base.db-shm
    for m in "$MIGRATIONS"/*.sql; do
        [ -f "$m" ] || { printf 'db-scenarios: no migration files\n' >&2; return 1; }
        # pragma results (`wal`, `5000`) are noise here; stderr still shows a real error.
        sqlite3 -bail -batch "$SCRATCH/base.db" <"$m" >/dev/null || {
            printf 'db-scenarios: migration failed: %s\n' "$(basename "$m")" >&2
            return 1
        }
        MIGRATIONS_APPLIED=$((MIGRATIONS_APPLIED+1))
    done
    # Fixed baseline domain. Explicit ids so the scenarios read plainly; two
    # seasons so the shape violations cannot collide with 0002's premier index.
    sqlite3 -bail -batch "$SCRATCH/base.db" "
        PRAGMA foreign_keys=ON;
        INSERT INTO seasons (id, name, is_active) VALUES
            (1, '۱۴۰۵–۱۴۰۶', 1), (2, '۱۳۹۹–۱۴۰۰', 0);
        INSERT INTO clubs (id, name) VALUES (1, 'نمونه آ'), (2, 'نمونه ب');
        INSERT INTO teams (id, club_id, label, display_name) VALUES
            (1, 1, '۱', 'نمونه آ ۱'), (2, 1, '۲', 'نمونه آ ۲'), (3, 2, '۱', 'نمونه ب ۱');
        INSERT INTO competitions (id, season_id, age_group_id, level, group_name, display_name) VALUES
            (1, 1, (SELECT id FROM age_groups WHERE age = 12), 'premier', NULL, 'لیگ برتر ۱۲ سال'),
            (2, 1, (SELECT id FROM age_groups WHERE age = 12), 'league1', 'A', 'لیگ یک A'),
            (3, 1, (SELECT id FROM age_groups WHERE age = 12), 'league1', 'B', 'لیگ یک B'),
            (4, 1, (SELECT id FROM age_groups WHERE age = 12), 'league1', 'C', 'لیگ یک C');
    " || return 1
}

use_db() { DB="$SCRATCH/$1.db"; cp "$SCRATCH/base.db" "$DB"; }

# ---------------------------------------------------------------- the 8 scenarios
# Each runs on its own copy of the baseline, so scenarios cannot contaminate
# each other. Comparisons here are against DATABASE.md's numbered list.

s1_premier_duplicate_club_aborts() {
    local n="1. premier duplicate-club → ABORT with the Persian trigger message"
    use_db s1 || return 1
    check_ok "$n (first team registers)" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (1, 1, 1);" || return 1
    check_refused "$n" "یک باشگاه در هر لیگ برتر فقط یک تیم" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (1, 2, 1);" || return 1
    check_eq "$n (the abort rolled back)" "1" \
        "SELECT COUNT(*) FROM registrations WHERE competition_id = 1;" || return 1
    pass "$n"
}

s2_premier_different_club_ok() {
    local n="2. different club in the same premier → OK"
    use_db s2 || return 1
    check_ok "$n" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (1, 1, 1), (1, 3, 2);" || return 1
    check_eq "$n (both persisted)" "2" \
        "SELECT COUNT(*) FROM registrations WHERE competition_id = 1;" || return 1
    pass "$n"
}

s3_league1_same_club_multiple_teams_ok() {
    local n="3. same club, multiple league1 teams → OK"
    use_db s3 || return 1
    check_ok "$n (club 1 fields a team in group A and team ۲ in group B)" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (2, 1, 1), (3, 2, 1);" || return 1
    check_eq "$n (both persisted)" "2" \
        "SELECT COUNT(*) FROM registrations WHERE club_id = 1 AND competition_id IN (2, 3);" || return 1
    pass "$n"
}

s4_match_both_teams_registered_ok() {
    local n="4. match with both teams registered → OK"
    use_db s4 || return 1
    check_ok "$n (register both teams in league1 C)" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (4, 1, 1), (4, 2, 1);" || return 1
    check_ok "$n (insert the fixture)" \
        "INSERT INTO matches (competition_id, home_team_id, away_team_id, week) VALUES (4, 1, 2, 1);" || return 1
    check_eq "$n (match persisted)" "1" "SELECT COUNT(*) FROM matches;" || return 1
    pass "$n"
}

s5_unregistered_team_aborts() {
    local n="5. unregistered team in a match → ABORT with the Persian trigger message"
    use_db s5 || return 1
    check_ok "$n (register both of club 1's teams)" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (4, 1, 1), (4, 2, 1);" || return 1
    # team 3 (نمونه ب ۱) is NOT registered in competition 4.
    check_refused "$n" "هر دو تیم مسابقه باید در همین مسابقات" \
        "INSERT INTO matches (competition_id, home_team_id, away_team_id, week) VALUES (4, 1, 3, 1);" || return 1
    check_eq "$n (nothing was inserted)" "0" "SELECT COUNT(*) FROM matches;" || return 1
    pass "$n"
}

s6_score_state_pairing() {
    local n="6. finished with scores → OK; scheduled with a score → CHECK fail"
    use_db s6 || return 1
    check_ok "$n (register and schedule a fixture)" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (4, 1, 1), (4, 2, 1);
         INSERT INTO matches (competition_id, home_team_id, away_team_id, week) VALUES (4, 1, 2, 1);" || return 1
    check_ok "$n (finish it with both scores)" \
        "UPDATE matches SET status = 'finished', home_score = 2, away_score = 1 WHERE id = 1;" || return 1
    check_eq "$n (the finished row kept its scores)" "2|1" \
        "SELECT home_score || '|' || away_score FROM matches WHERE id = 1 AND status = 'finished';" || return 1
    check_ok "$n (schedule a second fixture)" \
        "INSERT INTO matches (competition_id, home_team_id, away_team_id, week) VALUES (4, 2, 1, 2);" || return 1
    check_refused "$n" "CHECK constraint failed" \
        "UPDATE matches SET home_score = 1 WHERE id = 2;" || return 1
    check_eq "$n (the bad score was rolled back)" "1" \
        "SELECT COUNT(*) FROM matches WHERE id = 2 AND home_score IS NULL;" || return 1
    pass "$n"
}

s7_duplicate_fixture_refused() {
    local n="7. duplicate fixture in the same week → UNIQUE fail"
    use_db s7 || return 1
    check_ok "$n (register and insert the fixture)" \
        "INSERT INTO registrations (competition_id, team_id, club_id) VALUES (4, 1, 1), (4, 2, 1);
         INSERT INTO matches (competition_id, home_team_id, away_team_id, week) VALUES (4, 1, 2, 1);" || return 1
    check_refused "$n" "UNIQUE constraint failed" \
        "INSERT INTO matches (competition_id, home_team_id, away_team_id, week) VALUES (4, 1, 2, 1);" || return 1
    check_eq "$n (only one fixture persisted)" "1" "SELECT COUNT(*) FROM matches;" || return 1
    pass "$n"
}

s8_group_shape_check() {
    local n="8. premier-with-group / league1-without-group → CHECK fail"
    use_db s8 || return 1
    check_refused "$n (premier carrying a group)" "CHECK constraint failed" \
        "INSERT INTO competitions (id, season_id, age_group_id, level, group_name, display_name)
         VALUES (99, 2, (SELECT id FROM age_groups WHERE age = 12), 'premier', 'A', 'بدشکل');" || return 1
    check_refused "$n (league1 without a group)" "CHECK constraint failed" \
        "INSERT INTO competitions (id, season_id, age_group_id, level, group_name, display_name)
         VALUES (98, 2, (SELECT id FROM age_groups WHERE age = 12), 'league1', NULL, 'بدشکل');" || return 1
    check_eq "$n (no competition was created)" "4" "SELECT COUNT(*) FROM competitions;" || return 1
    pass "$n"
}

# ---------------------------------------------------------------- main

main() {
    mkdir -p "$SCRATCH_BASE" 2>/dev/null || {
        printf 'db-scenarios: cannot create scratch base %s\n' "$SCRATCH_BASE" >&2
        return 2
    }
    SCRATCH="$(mktemp -d "$SCRATCH_BASE/db-scenarios.XXXXXX")" || {
        printf 'db-scenarios: cannot create scratch under %s\n' "$SCRATCH_BASE" >&2
        return 2
    }
    printf '=== db-scenarios — DATABASE.md, 8 scenarios ===\n'
    printf 'building a fresh DB from %s\n' "$MIGRATIONS"
    build_base_db || { printf 'db-scenarios: could not build the baseline DB\n' >&2; return 2; }
    printf 'migrations applied: %s, age groups seeded: %s\n' \
        "$MIGRATIONS_APPLIED" \
        "$(sqlite3 -batch "$SCRATCH/base.db" 'SELECT COUNT(*) FROM age_groups;')"

    local failures=0
    s1_premier_duplicate_club_aborts              || failures=$((failures+1))
    s2_premier_different_club_ok                  || failures=$((failures+1))
    s3_league1_same_club_multiple_teams_ok        || failures=$((failures+1))
    s4_match_both_teams_registered_ok             || failures=$((failures+1))
    s5_unregistered_team_aborts                   || failures=$((failures+1))
    s6_score_state_pairing                        || failures=$((failures+1))
    s7_duplicate_fixture_refused                  || failures=$((failures+1))
    s8_group_shape_check                          || failures=$((failures+1))

    printf '\n=== %s/8 scenarios behaved as documented ===\n' "$((8 - failures))"
    [ "$failures" -eq 0 ] || { printf 'DRIFT: %s scenario(s) failed\n' "$failures" >&2; return 1; }
    return 0
}

case "${1:-}" in
    -h|--help) sed -n '3,/^set -uo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d' ;;
    "") main; exit $? ;;
    *) printf 'db-scenarios: takes no arguments (got %s)\n' "$1" >&2; exit 2 ;;
esac
