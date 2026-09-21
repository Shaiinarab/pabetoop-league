#!/usr/bin/env bash
# tools/serve.sh — run the Pabetoop League server (single Go binary + one SQLite file).
#
# This is the deployment entrypoint. It builds `bin/pabetoop-league` once, ensures the
# data directory exists, and runs the server in the foreground (so systemd/supervisor
# can own it). Nothing here needs a database server, Node, or the network.
#
#   tools/serve.sh                       # :8080, data/pabetoop-league.db
#   ADDR=:9000 tools/serve.sh            # custom listen address
#   ADMIN_PASSWORD=... tools/serve.sh    # bootstrap the admin account on first run
#   SEED=1 tools/serve.sh                # generate a fresh demo DB if none exists
#   tools/serve.sh --background          # detached, logs to data/server.log
#
# Production note: set ADMIN_PASSWORD (or ADMIN_PASSWORD_HASH) and either
# SESSION_SECRET or leave SESSION_SECRET_FILE to auto-create data/secret.key (0600).
# Put a TLS-terminating reverse proxy in front; the app sets HSTS when r.TLS != nil.

set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# Never let a global proxy capture localhost traffic (AGENTS.md pitfall).
export NO_PROXY=127.0.0.1,localhost no_proxy=127.0.0.1,localhost

# Load .env (brand + settings) if it exists, so a re-branded deployment serves the
# name the operator set. Explicit environment variables win, so `ADDR=:9000 tools/serve.sh`
# still beats a stale .env entry. See tools/load-env.sh for why this is not a plain `source`.
ENV_FILE="$ROOT/.env" . "$ROOT/tools/load-env.sh"

ADDR="${ADDR:-:8080}"
DB_PATH="${DB_PATH:-data/pabetoop-league.db}"
BIN="${BIN:-bin/pabetoop-league}"
LOG="${LOG:-data/server.log}"
BACKGROUND=0
[ "${1:-}" = "--background" ] && BACKGROUND=1

mkdir -p data bin

if [ "${SEED:-0}" = "1" ] && [ ! -f "$DB_PATH" ]; then
    echo "seeding $DB_PATH (demo data)…"
    go run ./cmd/seed --db "$DB_PATH" --force || exit 1
fi

echo "building $BIN …"
go build -o "$BIN" ./cmd/server || exit 1

run() {
    exec env ADDR="$ADDR" DB_PATH="$DB_PATH" "$BIN"
}

if [ "$BACKGROUND" = 1 ]; then
    echo "starting in background on $ADDR (log: $LOG)"
    # setsid: a plain nohup from a tool shell dies with the shell (SIGHUP).
    setsid env ADDR="$ADDR" DB_PATH="$DB_PATH" "$BIN" >>"$LOG" 2>&1 </dev/null &
    sleep 1
    pid=$(pgrep -f "$BIN" | head -1)
    echo "pid: ${pid:-?}"
    curl -s -o /dev/null -w 'health: %{http_code}\n' "http://${ADDR#:}/healthz" 2>/dev/null \
        || curl -s -o /dev/null -w 'health: %{http_code}\n' "http://127.0.0.1:${ADDR##*:}/healthz"
else
    echo "listening on $ADDR (db: $DB_PATH)"
    run
fi
