#!/usr/bin/env bash
# instantiate.sh — re-brand this template for a specific league.
#
# Pabetoop League ships tagless: no league, city or club name is compiled in.
# This script rewrites the identifiers that are *structural* (Go module path,
# binary and volume names) and writes the *cosmetic* identity into .env, where
# internal/site picks it up at process start.
#
# Usage:
#   tools/instantiate.sh --name "Riverside Youth League" \
#                        --name-fa "لیگ نوجوانان رودخانه" \
#                        --discipline-fa "فوتسال" \
#                        --module-path github.com/yourorg/riverside-league
#
# Safe by construction: it refuses to run on a dirty tree, and it verifies the
# result with a build + the branding leak test before declaring success.
set -euo pipefail

NAME=""; NAME_FA=""; DISCIPLINE_FA=""; SUBTITLE_FA=""
MODULE_PATH=""; BINARY=""; DRY_RUN=0

die() { printf 'instantiate: %s\n' "$*" >&2; exit 1; }
info() { printf '  %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)          NAME="${2:?}"; shift 2 ;;
    --name-fa)       NAME_FA="${2:?}"; shift 2 ;;
    --discipline-fa) DISCIPLINE_FA="${2:?}"; shift 2 ;;
    --subtitle-fa)   SUBTITLE_FA="${2:?}"; shift 2 ;;
    --module-path)   MODULE_PATH="${2:?}"; shift 2 ;;
    --binary)        BINARY="${2:?}"; shift 2 ;;
    --dry-run)       DRY_RUN=1; shift ;;
    -h|--help)       sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)               die "unknown argument: $1 (try --help)" ;;
  esac
done

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

[[ -d .git ]] || die "not a git repository — run this from a clone of the template"
if [[ -n "$(git status --porcelain)" ]]; then
  die "working tree is dirty; commit or stash first so a bad run is revertible"
fi

OLD_MODULE="$(sed -n 's/^module[[:space:]]\+//p' go.mod)"
[[ -n "$OLD_MODULE" ]] || die "could not read the module path from go.mod"
MODULE_PATH="${MODULE_PATH:-$OLD_MODULE}"

DERIVED_BINARY="${MODULE_PATH##*/}"
BINARY="${BINARY:-$DERIVED_BINARY}"
[[ "$BINARY" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "binary name '$BINARY' is not shell/docker safe"

echo "Instantiation plan"
info "module   $OLD_MODULE  →  $MODULE_PATH"
info "binary   output         →  $BINARY"
info "brand    ${NAME:-<unchanged>} / ${NAME_FA:-<unchanged>}"
info "root     $ROOT"

if [[ "$DRY_RUN" == "1" ]]; then
  echo "dry run — nothing written"
  exit 0
fi

# ── 1. Structural rename: module path and the identifiers that name the build ──
if [[ "$MODULE_PATH" != "$OLD_MODULE" ]]; then
  echo "Rewriting module path"
  while IFS= read -r -d '' f; do
    sed -i "s|$OLD_MODULE|$MODULE_PATH|g" "$f"
  done < <(grep -rlZ "$OLD_MODULE" . \
             --exclude-dir=.git --exclude-dir=node_modules 2>/dev/null || true)
fi

if [[ "$BINARY" != "pabetoop-league" ]]; then
  echo "Renaming build outputs"
  for f in Dockerfile Makefile docker-compose.yml .github/workflows/ci.yml \
           .gitignore tools/serve.sh tools/smoke.sh; do
    [[ -f "$f" ]] || continue
    sed -i "s|pabetoop-league|$BINARY|g" "$f"
  done
  # Compose volume names cannot contain dashes.
  sed -i "s|pabetoop_league_|${BINARY//-/_}_|g" docker-compose.yml

  # The database filename is part of the fork's identity too: a fork should create
  # data/<its-own-name>.db, not data/pabetoop-league.db. Rewrite it wherever the
  # default is written down, so the docs, the Go default and .env all agree.
  # .env is created from .env.example further down, so rewriting the example here
  # is what makes the generated .env correct.
  echo "Renaming the default database file"
  while IFS= read -r -d '' f; do
    sed -i "s|data/pabetoop-league\.db|data/$BINARY.db|g" "$f"
  done < <(grep -rlZ "data/pabetoop-league\.db" . \
             --exclude-dir=.git --exclude-dir=node_modules 2>/dev/null || true)
fi

# ── 2. Cosmetic identity: written to .env, read by internal/site ──────────────
if [[ ! -f .env ]]; then
  cp .env.example .env
  info "created .env from .env.example"
fi

set_env() {
  local key="$1" value="$2"
  [[ -n "$value" ]] || return 0
  if grep -q "^${key}=" .env; then
    awk -v k="$key" -v v="$value" 'BEGIN{FS=OFS="="} $1==k {print k"="v; next} {print}' \
      .env > .env.tmp && mv .env.tmp .env
  else
    printf '%s=%s\n' "$key" "$value" >> .env
  fi
}

set_env LEAGUE_NAME          "$NAME"
set_env LEAGUE_NAME_FA       "$NAME_FA"
set_env LEAGUE_DISCIPLINE_FA "$DISCIPLINE_FA"
set_env LEAGUE_SUBTITLE_FA   "$SUBTITLE_FA"

# ── 3. Post-conditions: fail loudly rather than hand back a broken fork ───────
echo "Verifying"
if ! go build ./... ; then
  die "build failed after instantiation — inspect the diff, then 'git checkout .'"
fi
if ! go test ./internal/web -run Legacy >/dev/null; then
  die "branding leak test failed after instantiation"
fi

echo
echo "Done. Next steps:"
echo "  1. Replace seed/clubs.json with your own club list (or keep the placeholders)."
echo "  2. Review .env — ADMIN_PASSWORD and SESSION_SECRET still need your values."
echo "  3. docker compose up -d --build"
echo "  4. Commit: git add -A && git commit -m 'brand: instantiate for ${NAME:-this league}'"
