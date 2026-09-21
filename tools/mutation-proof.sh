#!/usr/bin/env bash
# mutation-proof.sh — re-run TESTING.md §5.2's mutation proofs as commands.
#
# WHY (docs/OPEN-WORK.md V4)
#   §5.2 records the proofs (see --list for the current set) with the exact line
#   each mutation made the suite print — the strongest evidence in the repo. The procedure was prose
#   and the scratch tree it ran in was gitignored, so a reader had only our word
#   that proof C ever bit. This script makes each proof one command.
#
#   The harness is also required to be able to FAIL: a mutation that does not
#   bite is a finding, and reporting it as success is worse than no harness.
#   `rule2-unique-no-bite` is a real entry for exactly that case (TESTING.md
#   §5.3): replacing registrations' UNIQUE (competition_id, team_id) with
#   UNIQUE (id) leaves the test green because the store's duplicate pre-check
#   bites first. The script must report an *expected* non-bite, not a pass.
#
# HOW IT STAYS HONEST
#   * Every run copies the module into a scratch dir under the workspace
#     .openclaw/tmp/ and mutates only the copy — never the shared tree. The
#     script hashes the three real files it can mutate before and after every
#     proof and aborts if any of them changed, so a bug here cannot ship a
#     mutation to main.
#   * An anchor that is not found, or a replacement that does not land, aborts
#     the proof — a mutation harness that silently runs an unmutated copy is a
#     rubber stamp. Likewise a build failure is reported as INCONCLUSIVE, never
#     as "the test failed, proof confirmed".
#   * Offline by construction: GOPROXY=off and -mod=mod against the module
#     cache, so a proof run never reaches the network. Warm the cache once with
#     `go mod download` before the first run.
#
# USAGE
#   tools/mutation-proof.sh --list          the proofs it knows (id, rule, file, test)
#   tools/mutation-proof.sh <id>            run one proof
#   tools/mutation-proof.sh --all           every proof (exit non-zero if any misbehaves)
#
# Exit: 0 all proofs behaved as expected · 1 a proof misbehaved or could not run
#
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
WORKSPACE="$(cd "$ROOT/../.." && pwd -P)"
PARENT="$(dirname "$ROOT")"
BASE_NAME="$(basename "$ROOT")"

# Scratch lives under the workspace .openclaw/tmp/ (workspace rule: no writes
# outside it). Overridable for a sandbox run.
SCRATCH_BASE="${MUTATION_PROOF_TMPDIR:-$WORKSPACE/.openclaw/tmp}"
RUN_DIR=""
CURRENT=""

cleanup() {
    if [ -n "${CURRENT:-}" ] && [ -d "$CURRENT" ]; then rm -rf "$CURRENT"; fi
    if [ -n "${RUN_DIR:-}" ] && [ -d "$RUN_DIR" ]; then rm -rf "$RUN_DIR"; fi
    return 0
}
trap cleanup EXIT INT TERM HUP

die() { printf 'mutation-proof: %s\n' "$1" >&2; exit 1; }

# ------------------------------------------------------------------ proof tables
#
# Both tables are pipe-separated because no field here contains a pipe. Keep it
# that way, or switch the delimiter deliberately.

# id | rule label | package hint | test name | expectation (bite|nobite)
proofs_table() {
    cat <<'EOF'
A|rule 5 — duplicate fixtures rejected|store|TestDuplicateFixtureNeedsAWeek|bite
B|rule 7 — deleting restricted while refs exist|store|TestDeleteCompetitionRefusedWhileItHasRefs|bite
C|rule 10 — every mutation writes an audit row|store|TestEveryMutationWritesAnAuditRow|bite
D|rule 10 — audit rows carry the before/after payload|store|TestAuditRowsCarryTheBeforeAfterPayload|bite
E|rule 1 — self-match refused (3 layers)|store|TestCreateMatchIntegrity|bite
F|rule 8 — import preview token is one-shot|web|TestImportConfirmTokenIsOneShot|bite
G|rule 9 — finished-only filter in FinishedMatches|store|TestFinishedMatchesExcludesScheduledMatches|bite
H|rule 4 — matches table CHECK refuses inconsistent score state|store|TestMatchTableCheckRefusesInconsistentScoreState|bite
I|P3 — the team edit persists the age category|store|TestUpdateTeamPersistsLabelDisplayAndCategory|bite
J|rule 10 — team update audit records the category that changed|store|TestUpdateTeamAuditsEveryChangedField|bite
K|route wiring — the team edit routes are registered|web|TestAdminTeamEditFormPrefillsAndLocksTheClub|bite
L|route table — every registrar is called from the routing tree|web|TestEveryRegistrarIsCalledFromTheRoutingTree|bite
M|rule 2 — needs BOTH layers removed (non-premier comp)|store|TestRegisterDuplicateRefusedByTheStorePreCheck|bite
N|rule 3 — the pre-check is the only layer that names the club|web|TestCompetitionRegisterPremierClubRuleRefused|bite
O|rule 6 — the handler refuses first, in its own words|web|TestFixtureCreateUnregisteredTeamRefused|bite
rule2-unique-no-bite|rule 2 — DB UNIQUE is a backstop, store pre-check bites|web|TestCompetitionRegisterDuplicateRefused|nobite
rule2-premier-shadow-no-bite|rule 2 — the premier web test is shadowed by rule 3|web|TestCompetitionRegisterDuplicateRefused|nobite
EOF
}

# id | file (repo-relative) | op | old literal | new literal | occurrences
#
# A literal pipe inside `old`/`new` is written `\p` (decoded after the record is
# split): `a \p\p b` is the Go condition `a || b`. Without this, the single most
# common Go operator could not be named as an anchor at all.
#
# Literal, reviewable edits — no clever regex zoo. Ops:
#   replace       old literal -> new literal (all occurrences)
#   insert_before new text (with its leading tab) on its own line before the anchor
#   insert_after  ... after the anchor
#   delete_line   drop lines containing the anchor (its `new` field is empty)
mutations_table() {
    cat <<'EOF'
A|internal/store/migrations/0001_init.sql|replace|CREATE UNIQUE INDEX idx_matches_unique_fixture|-- CREATE UNIQUE INDEX idx_matches_unique_fixture|1
A|internal/store/migrations/0001_init.sql|replace|ON matches(competition_id, week, home_team_id, away_team_id)|-- ON matches(competition_id, week, home_team_id, away_team_id)|1
A|internal/store/migrations/0001_init.sql|replace|WHERE week IS NOT NULL;|-- WHERE week IS NOT NULL;|1
B|internal/store/store.go|replace|if regs > 0 {|if false {|1
C|internal/store/store.go|insert_after|func audit(tx *sql.Tx, action, entity string, entityID int64, before, after *string) error {|	return nil|1
D|internal/store/store.go|replace|action, entity, entityID, before, after,|action, entity, entityID, nil, nil,|1
E|internal/store/store.go|replace|if homeTeamID == awayTeamID {|if false {|1
E|internal/store/migrations/0001_init.sql|replace|CHECK (home_team_id != away_team_id),|CHECK (home_team_id != away_team_id OR 1=1),|1
E|internal/store/migrations/0001_init.sql|replace|WHEN NEW.home_team_id = NEW.away_team_id|WHEN 0|2
F|internal/web/import_handlers.go|replace|delete(importPreviews.items, token)|// delete(importPreviews.items, token)|1
G|internal/store/store.go|replace|AND status = 'finished'|AND 1=1|1
H|internal/store/migrations/0001_init.sql|delete_line|    CHECK ( (status = 'finished' AND home_score IS NOT NULL AND away_score IS NOT NULL)||1
H|internal/store/migrations/0001_init.sql|delete_line|         OR (status = 'scheduled' AND home_score IS NULL AND away_score IS NULL) )||1
H|internal/store/migrations/0001_init.sql|replace|CHECK (home_team_id != away_team_id),|CHECK (home_team_id != away_team_id)|1
I|internal/store/store.go|replace|label, displayName, optInt64Ptr(int64FromPtr(newAge)), id)|label, displayName, optInt64Ptr(int64FromPtr(oldAge)), id)|1
J|internal/store/store.go|replace|"display_name": displayName, "age_group_id": int64FromPtr(newAge)}))|"display_name": displayName, "age_group_id": int64FromPtr(oldAge)}))|1
K|internal/web/admin_handlers.go|delete_line|mux.Handle("GET /admin/teams/{id}/edit"||1
K|internal/web/admin_handlers.go|delete_line|mux.Handle("POST /admin/teams/{id}",||1
L|internal/web/server.go|delete_line|s.RegisterSeasonRoutes(mux)||1
# M removes BOTH rule-2 layers at once. TESTING.md §5.3 records that neither
# layer alone is detectable: the store pre-check and the DB UNIQUE emit the
# same Persian sentence (the store maps isUniqueErr to the pre-check's
# wording), so a single mutation leaves the test green. Removing the pair is
# the only mutation that bites — that is the honest shape of this rule.
#
# It targets the STORE test, not the web one: in the web test's seeded premier
# competition a duplicate registration is simultaneously a rule-3 violation,
# and rule 3's message also contains "قبلاً در", so that test is blind to
# rule 2. rule2-premier-shadow-no-bite below pins that down mechanically.
M|internal/store/store.go|replace|if dup {|if false {|1
M|internal/store/migrations/0001_init.sql|replace|UNIQUE (competition_id, team_id)|UNIQUE (id)|1
rule2-premier-shadow-no-bite|internal/store/store.go|replace|if dup {|if false {|1
rule2-premier-shadow-no-bite|internal/store/migrations/0001_init.sql|replace|UNIQUE (competition_id, team_id)|UNIQUE (id)|1
# N keeps the rule sentence visible (the trigger's RAISE text carries it) but
# drops the club name, which only the pre-check can supply.
N|internal/store/store.go|replace|if clubHasTeam {|if false {|1
# O's anchor is deliberately pipe-free: this table is pipe-delimited, and the
# real line contains `||`. Replacing the condition with `false` disables the
# handler check, so the store's different wording is what reaches the flash.
O|internal/web/fixture_handlers.go|replace|!registered[int64(homeID)] \p\p !registered[int64(awayID)]|false|1
rule2-unique-no-bite|internal/store/migrations/0001_init.sql|replace|UNIQUE (competition_id, team_id)|UNIQUE (id)|1
EOF
}

# The only real files any mutation may touch. Hashed before/after each proof as
# the "never mutate the shared tree" guard.
TARGET_FILES="internal/store/store.go internal/store/migrations/0001_init.sql internal/web/import_handlers.go internal/web/admin_handlers.go internal/web/server.go internal/web/fixture_handlers.go"

file_hash() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    else
        cksum "$1" | cut -d' ' -f1
    fi
}

guard_hash() {
    local f s=""
    for f in $TARGET_FILES; do
        [ -f "$ROOT/$f" ] || continue
        s="$s$(file_hash "$ROOT/$f") "
    done
    printf '%s' "$s"
}

# ------------------------------------------------------------------ helpers

proof_field() { # id -> label|pkg|test|expect
    local id="$1" pid plabel ppkg ptest pexpect
    while IFS='|' read -r pid plabel ppkg ptest pexpect; do
        if [ "$pid" = "$id" ]; then
            printf '%s|%s|%s|%s\n' "$plabel" "$ppkg" "$ptest" "$pexpect"
            return 0
        fi
    done < <(proofs_table)
    return 1
}

proof_exists() { proof_field "$1" >/dev/null 2>&1; }

proof_id_list() {
    local pid rest
    while IFS='|' read -r pid rest; do
        # `rest` swallows the remaining fields; only the id is printed.
        printf '%s\n' "$pid"
    done < <(proofs_table)
}

copy_module() { # dest-dir -> $dest/$BASE_NAME is the module
    local dest="$1"
    mkdir -p "$dest" || return 1
    tar -C "$PARENT" -cf - \
        --exclude="$BASE_NAME/vendor" \
        --exclude="$BASE_NAME/tools/toolchain" \
        --exclude="$BASE_NAME/node_modules" \
        --exclude="$BASE_NAME/data" \
        --exclude="$BASE_NAME/tmp" \
        --exclude="$BASE_NAME/.openclaw" \
        --exclude="$BASE_NAME/bin" \
        --exclude="$BASE_NAME/prototype" \
        "$BASE_NAME" | tar -C "$dest" -xf - || return 1
    [ -f "$dest/$BASE_NAME/go.mod" ] || return 1
    return 0
}

# decode_pipes: the table is pipe-delimited, so a literal pipe in an anchor or
# replacement is stored as `\p` and restored here. Every consumer of `old`/`new`
# must go through this, or an anchor containing `||` silently matches nothing.
decode_pipes() { printf '%s' "$1" | sed 's/\\p/|/g'; }

apply_mutations() { # id module-dir
    local id="$1" mod="$2"
    local pid file op old new want f pre_old mut_before mut_after applied=0
    while IFS='|' read -r pid file op old new want; do
        [ "$pid" = "$id" ] || continue
        old="$(decode_pipes "$old")"
        new="$(decode_pipes "$new")"
        f="$mod/$file"
        if [ ! -f "$f" ]; then
            printf 'MUTATION TARGET MISSING: %s\n' "$file" >&2
            return 1
        fi
        pre_old="$(grep -cF -- "$old" "$f" 2>/dev/null)" || true
        [ -n "$pre_old" ] || pre_old=0
        if [ "$pre_old" -ne "$want" ]; then
            printf 'MUTATION ANCHOR NOT FOUND (want %s, found %s): %s <- %s\n' \
                "$want" "$pre_old" "$file" "$old" >&2
            return 1
        fi
        mut_before="$(file_hash "$f")"
        case "$op" in
            replace)
                awk -v old="$old" -v new="$new" '
                    { line=$0; p=index(line, old)
                      if (p>0) line=substr(line,1,p-1) new substr(line,p+length(old))
                      print line }
                ' "$f" > "$f.mut.$$" && mv "$f.mut.$$" "$f" || {
                    rm -f "$f.mut.$$"
                    printf 'MUTATION FAILED: %s\n' "$file" >&2
                    return 1
                } ;;
            insert_before|insert_after)
                awk -v anchor="$old" -v ins="$new" -v after="$([ "$op" = insert_after ] && echo 1 || echo 0)" '
                    { if (!done && index($0, anchor)>0) { if (after) { print; print ins } else { print ins; print }; done=1; next } print }
                ' "$f" > "$f.mut.$$" && mv "$f.mut.$$" "$f" || {
                    rm -f "$f.mut.$$"
                    printf 'MUTATION FAILED: %s\n' "$file" >&2
                    return 1
                } ;;
            delete_line)
                awk -v anchor="$old" -v want="$want" '
                    { if (dropped < want && index($0, anchor)>0) { dropped++; next } print }
                ' "$f" > "$f.mut.$$" && mv "$f.mut.$$" "$f" || {
                    rm -f "$f.mut.$$"
                    printf 'MUTATION FAILED: %s\n' "$file" >&2
                    return 1
                } ;;
            *)
                printf 'unknown mutation op: %s\n' "$op" >&2
                return 1 ;;
        esac
        # The anchor existed exactly `want` times; the file must now differ. A
        # per-mutation hash is the only landing check that works for every op —
        # counting the new text fails when the new literal is a substring of the
        # old one (proof A prefixes `-- `; proof H drops a trailing comma).
        mut_after="$(file_hash "$f")"
        if [ "$mut_before" = "$mut_after" ]; then
            printf 'MUTATION DID NOT LAND (%s unchanged; anchor "%s" matched but no line was altered)\n' \
                "$file" "$old" >&2
            return 1
        fi
        applied=$((applied+1))
    done < <(mutations_table)
    [ "$applied" -gt 0 ] || { printf 'no mutations defined for %s\n' "$id" >&2; return 1; }
    return 0
}

print_mutations() { # id record-file
    local id="$1" rec="$2" pid file op old new want
    while IFS='|' read -r pid file op old new want; do
        [ "$pid" = "$id" ] || continue
        old="$(decode_pipes "$old")"
        new="$(decode_pipes "$new")"
        printf '  %s\n    - %s\n    + %s\n' "$file" "$old" "$new"
        [ -n "$rec" ] && printf '  %s\n    - %s\n    + %s\n' "$file" "$old" "$new" >> "$rec"
    done < <(mutations_table)
}

# ------------------------------------------------------------------ run one

run_one() { # id
    local id="$1"
    local fields label pkg test expect
    fields="$(proof_field "$id")" || die "unknown proof '$id' (see --list)"
    IFS='|' read -r label pkg test expect <<<"$fields"

    printf '\n=== proof %s — %s ===\n' "$id" "$label"
    printf 'expected: %s\n' \
        "$( [ "$expect" = bite ] && echo 'test FAILS (the mutation bites)' || echo 'test STAYS GREEN (expected non-bite)' )"
    printf 'mutations:\n'
    print_mutations "$id" ""

    local before after
    before="$(guard_hash)"

    # Inside an --all run dir when there is one, so one cleanup removes every
    # proof's copy; a single-proof run gets its dir removed by the trap itself.
    local scratch_parent="${RUN_DIR:-$SCRATCH_BASE}"
    CURRENT="$(mktemp -d "$scratch_parent/mutation-proof.$id.XXXXXX")" || {
        printf 'RESULT: ERROR — could not create scratch under %s\n' "$scratch_parent" >&2
        return 1
    }
    local mod="$CURRENT/$BASE_NAME"
    if ! copy_module "$CURRENT"; then
        printf 'RESULT: ERROR — could not copy the module to %s\n' "$CURRENT" >&2
        return 1
    fi
    if ! apply_mutations "$id" "$mod"; then
        printf 'RESULT: ERROR — mutation could not be applied (see above); nothing was run.\n' >&2
        return 1
    fi

    local goflags="-mod=mod"
    [ -d "$mod/vendor" ] && goflags="-mod=vendor"
    local log="$CURRENT/go-test.log"
    printf 'running: go test ./internal/... -run ^%s$ -count=1  (GOFLAGS=%s GOPROXY=off)\n' "$test" "$goflags"

    ( cd "$mod" && \
      GOFLAGS="$goflags" GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOWORK=off \
      go test ./internal/... -run "^${test}\$" -count=1 ) >"$log" 2>&1
    local rc=$?

    after="$(guard_hash)"
    if [ "$before" != "$after" ]; then
        printf 'FATAL: the real tree changed during proof %s — aborting (this is a harness bug, not a proof).\n' "$id" >&2
        return 2
    fi

    local buildfail=0
    grep -qF '[build failed]' "$log" && buildfail=1
    grep -qF '[setup failed]' "$log" && buildfail=1

    if [ "$buildfail" = 1 ]; then
        printf 'RESULT: ERROR — the mutated copy did not build (inconclusive, NOT a bite).\n' >&2
        grep -E '^#|\.go:[0-9]+:' "$log" | sed 's/^[[:space:]]*//' | head -20 >&2 || true
        return 1
    fi

    local verdict
    if [ "$rc" -eq 0 ]; then
        if [ "$expect" = bite ]; then
            verdict="DID NOT BITE"
        else
            verdict="EXPECTED NON-BITE — test stayed green, as §5.3 records"
        fi
    else
        if [ "$expect" = bite ]; then
            verdict="BITE — test failed as recorded"
        else
            verdict="UNEXPECTED BITE — a documented non-bite mutation made the test fail"
        fi
    fi
    printf 'RESULT: %s  (go test exit %s)\n' "$verdict" "$rc"

    if [ "$expect" = bite ]; then
        printf 'observed failing line(s):\n'
        grep -E '\.go:[0-9]+:' "$log" | sed 's/^[[:space:]]*//' | head -8 || true
        grep -E '^(FAIL|--- FAIL)' "$log" | head -8 || true
    fi

    if [ "$expect" = bite ] && [ "$verdict" = "BITE — test failed as recorded" ]; then
        return 0
    fi
    if [ "$expect" = nobite ] && [ "$rc" -eq 0 ]; then
        return 0
    fi
    return 1
}

# ------------------------------------------------------------------ list

list_proofs() {
    printf 'mutation proofs known to %s\n\n' "$(basename "$0")"
    printf '%-22s %-8s %-52s %s\n' 'ID' 'EXPECT' 'RULE' 'TEST'
    local pid plabel ppkg ptest pexpect
    while IFS='|' read -r pid plabel ppkg ptest pexpect; do
        printf '%-22s %-8s %-52s %s\n' "$pid" "$pexpect" "$plabel" "$ptest"
    done < <(proofs_table)
    printf '\nfiles each proof mutates:\n'
    while IFS='|' read -r pid rest; do
        local files
        files="$(mutations_table | awk -F'|' -v id="$pid" '$1==id {print $2}' | sort -u | tr '\n' ' ')"
        printf '  %-22s %s\n' "$pid" "${files% }"
    done < <(proofs_table)
}

# ------------------------------------------------------------------ main

usage() {
    sed -n '3,/^set -uo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
}

[ $# -gt 0 ] || { usage; exit 1; }

mkdir -p "$SCRATCH_BASE" 2>/dev/null || die "cannot create scratch base $SCRATCH_BASE"
[ -w "$SCRATCH_BASE" ] || die "scratch base $SCRATCH_BASE is not writable"
# Prune our own leftovers older than a day (a kill -9 skips the trap).
find "$SCRATCH_BASE" -maxdepth 1 -name 'mutation-proof.*' -type d -mmin +1440 -exec rm -rf {} + 2>/dev/null || true

case "${1:-}" in
    --list)
        list_proofs
        exit 0 ;;
    --all)
        RUN_DIR="$(mktemp -d "$SCRATCH_BASE/mutation-proof.all.XXXXXX")" || die "cannot create run dir"
        before_all="$(guard_hash)"
        failed=0; total=0
        for id in $(proof_id_list); do
            total=$((total+1))
            run_one "$id" || failed=$((failed+1))
        done
        after_all="$(guard_hash)"
        printf '\n=== summary ===\n'
        printf '%s proof(s) run, %s misbehaved\n' "$total" "$failed"
        if [ "$before_all" != "$after_all" ]; then
            printf 'FATAL: the shared tree changed across --all\n' >&2
            exit 2
        fi
        printf 'shared tree unchanged (hash guard over: %s)\n' "$TARGET_FILES"
        [ "$failed" -eq 0 ] || exit 1
        exit 0 ;;
    -h|--help)
        usage
        exit 0 ;;
    *)
        if ! proof_exists "$1"; then
            printf 'mutation-proof: unknown proof %s\n' "$1" >&2
            list_proofs >&2
            exit 1
        fi
        run_one "$1"
        exit $? ;;
esac
