#!/bin/sh
# Container entrypoint: optionally create a demo database, then exec the server.
#
# SEED=1 generates demo data when the database does not exist yet. It is
# idempotent: an existing database is never touched, so restarting the container
# cannot wipe real data. To regenerate deliberately, remove the volume.
set -eu

DB="${DB_PATH:-/app/data/league.db}"
CLUBS="${CLUBS_FILE:-/app/seed/clubs.json}"

if [ "${SEED:-0}" = "1" ] && [ ! -f "$DB" ]; then
  echo "seeding $DB with placeholder demo data (clubs: $CLUBS)"
  mkdir -p "$(dirname "$DB")"
  /app/league-seed --db "$DB" --clubs-file "$CLUBS" --force
fi

exec /app/league "$@"
