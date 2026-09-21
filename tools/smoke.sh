#!/usr/bin/env bash
# tools/smoke.sh — live end-to-end smoke test for the Pabetoop-League server.
#
# Builds the real binary, boots it against a real SQLite database, and asserts
# that the running HTTP surface serves *data* (not placeholder/empty states).
# This is the Lead's integration gate and is safe for workers to run: it never
# touches data/pabetoop-league.db, never mutates the repo, and cleans up after
# itself.
#
# Usage:
#   tools/smoke.sh                     # uses data/verify-seed.db if present
#   tools/smoke.sh path/to.db          # explicit database
#   SEED=1 tools/smoke.sh              # generate a fresh seed DB with cmd/seed
#
# Exit 0 = all assertions passed. NO_PROXY is forced for 127.0.0.1 so an
# inherited proxy cannot hijack the localhost calls (AGENTS.md pitfall).

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

export NO_PROXY=127.0.0.1,localhost
export no_proxy=127.0.0.1,localhost

PORT="${SMOKE_PORT:-18099}"
ADDR="127.0.0.1:$PORT"
DB_ARG="${1:-}"
WORK="$(mktemp -d)"
DB="$WORK/smoke.db"
BIN="$WORK/server"
SRV_PID=""
FAILS=0
LAST_CODE=""
LAST_LOC=""

cleanup() {
    [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
    wait "$SRV_PID" 2>/dev/null
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILS=$((FAILS+1)); }

# req <path>: performs the request, records status/headers/body in $WORK.
# Deliberately NOT a command substitution — a subshell would swallow the globals.
req() {
    curl -s -o "$WORK/body" -D "$WORK/headers" -w '%{http_code}' \
        -H 'Accept: text/html' "http://$ADDR$1" > "$WORK/code" 2>/dev/null
    LAST_CODE="$(tr -d '\n' < "$WORK/code" 2>/dev/null)"
    LAST_LOC="$(awk 'tolower($1)=="location:"{print $2}' "$WORK/headers" 2>/dev/null | tr -d '\r' | tail -1)"
}
body() { cat "$WORK/body" 2>/dev/null; }

echo "=== smoke: pabetoop-league @ $ROOT ==="

if [ -n "$DB_ARG" ]; then
    [ -f "$DB_ARG" ] || { echo "smoke: no such db: $DB_ARG" >&2; exit 1; }
    cp "$DB_ARG" "$DB"
elif [ "${SEED:-0}" = "1" ]; then
    echo "  building seed generator…"
    go build -o "$WORK/seed" ./cmd/seed || { echo "seed build failed" >&2; exit 1; }
    "$WORK/seed" -db "$DB" >/dev/null 2>&1 || { echo "seed run failed" >&2; exit 1; }
    SEEDED=1
elif [ -f data/verify-seed.db ]; then
    cp data/verify-seed.db "$DB"
    SEEDED=1
else
    echo "  (no database given and data/verify-seed.db missing — running with an empty DB)"
    SEEDED=0
fi
[ -n "${SEEDED:-}" ] || SEEDED=1

echo "  building server…"
go build -o "$BIN" ./cmd/server || { echo "build failed" >&2; exit 1; }

echo "  starting on $ADDR…"
ADMIN_PASSWORD="smoke-pass" DB_PATH="$DB" ADDR="$ADDR" \
    SESSION_SECRET_FILE="$WORK/secret.key" "$BIN" >"$WORK/server.log" 2>&1 &
SRV_PID=$!

for _ in $(seq 1 50); do
    curl -s -o /dev/null "http://$ADDR/healthz" 2>/dev/null && break
    kill -0 "$SRV_PID" 2>/dev/null || { echo "server died at startup:"; cat "$WORK/server.log"; exit 1; }
    sleep 0.2
done

echo
echo "-- availability --"
req /healthz
[ "$LAST_CODE" = "200" ] && pass "GET /healthz 200" || fail "GET /healthz = $LAST_CODE"

echo "-- public pages must render real data, not placeholders --"
req /
[ "$LAST_CODE" = "200" ] && pass "GET / 200" || fail "GET / = $LAST_CODE"
if body | grep -q "در دست ساخت"; then
    fail "GET / still renders the «در دست ساخت» construction placeholder"
else
    pass "GET / has no construction placeholder"
fi
if [ "$SEEDED" = "1" ]; then
    if body | grep -qE "لیگ برتر|جدول|نتیجه"; then
        pass "GET / contains competition wording (data-backed render)"
    else
        fail "GET / shows no competition wording — looks like an empty DB"
    fi
fi

COMP_ID=""; AGE_ID=""
if command -v sqlite3 >/dev/null 2>&1; then
    COMP_ID="$(sqlite3 "$DB" 'SELECT id FROM competitions ORDER BY id LIMIT 1;' 2>/dev/null)"
    AGE_ID="$(sqlite3 "$DB" 'SELECT id FROM age_groups ORDER BY sort_order LIMIT 1;' 2>/dev/null)"
fi

if [ -n "$COMP_ID" ]; then
    req "/competition/$COMP_ID"
    [ "$LAST_CODE" = "200" ] && pass "GET /competition/$COMP_ID 200" || fail "GET /competition/$COMP_ID = $LAST_CODE"
    if body | grep -q "در دست ساخت"; then fail "/competition/$COMP_ID still placeholder"; else pass "/competition/$COMP_ID is data-backed"; fi
    if body | grep -qE "امتیاز|بازی"; then pass "/competition/$COMP_ID renders a standings table"; else fail "/competition/$COMP_ID has no standings content"; fi
    req "/competition/$COMP_ID/results"
    [ "$LAST_CODE" = "200" ] && pass "GET /competition/$COMP_ID/results 200" || fail "results tab = $LAST_CODE"
    req "/competition/999999"
    [ "$LAST_CODE" = "404" ] && pass "unknown competition -> 404" || fail "unknown competition = $LAST_CODE"
    if body | grep -q "پیدا نشد"; then pass "404 body is Persian"; else fail "404 body is not Persian"; fi
fi

if [ -n "$AGE_ID" ]; then
    req "/age/$AGE_ID"
    [ "$LAST_CODE" = "200" ] && pass "GET /age/$AGE_ID 200" || fail "GET /age/$AGE_ID = $LAST_CODE"
    if body | grep -q "در دست ساخت"; then fail "/age/$AGE_ID still placeholder"; else pass "/age/$AGE_ID is data-backed"; fi
fi

echo "-- admin surface must be mounted (not 404) --"
req /admin/clubs
case "$LAST_CODE" in
    303|302) pass "GET /admin/clubs -> $LAST_CODE $LAST_LOC (login gate)" ;;
    404)     fail "GET /admin/clubs = 404 — admin routes are NOT wired" ;;
    200)     pass "GET /admin/clubs 200" ;;
    *)       fail "GET /admin/clubs = $LAST_CODE" ;;
esac
req /admin
case "$LAST_CODE" in
    303|302) pass "GET /admin -> $LAST_CODE (login gate)" ;;
    200)     pass "GET /admin 200" ;;
    404)     fail "GET /admin = 404 — admin routes are NOT wired" ;;
    *)       fail "GET /admin = $LAST_CODE" ;;
esac

echo
if [ "$FAILS" = 0 ]; then
    printf '\033[32mSMOKE OK\033[0m — all checks passed\n'
    exit 0
else
    printf '\033[31mSMOKE FAILED\033[0m — %d check(s) failed\n' "$FAILS"
    echo "--- server log tail ---"
    tail -20 "$WORK/server.log" 2>/dev/null
    exit 1
fi
