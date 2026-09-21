# TESTING — Suites, commands, acceptance gates

> No test is trusted unless it runs here. Canonical gate order:
> `go build` → `go vet` → `go test` → `bun` client checks → `tools/smoke.sh`.

## 1. Suites (where they live)

| Suite | Covers | Run |
|---|---|---|
| `internal/store/store_test.go` | Integrity rules (§7): Premier uniqueness trigger, registration/match validation, score transitions, delete blocks, audit rows | `go test ./internal/store/ -count=1` |
| `internal/standing/{standing_test,property_test}.go` | Deterministic table: Pts→GD→GF→Persian-alpha, tie-break edge cases | `go test ./internal/standing/ -count=1` |
| `internal/jalali/{jalali_test,property_test}.go` | Gregorian⇄Jalali round-trips, leap years, month lengths, Persian rendering, garbage-input rejection | `go test ./internal/jalali/ -count=1` |
| `internal/import/{parser_test,validate_test}.go` | CSV parse → normalize → validate → preview rows (confirm path is handler-level) | `go test ./internal/import/ -count=1` |
| `internal/web/*_test.go` | Handler contracts: admin CRUD, quick-result set/clear + standings refresh, import preview/confirm, competition/registration, backup download (valid SQLite), audit filters, public pages, template rendering, session/CSRF/security | `go test ./internal/web/ -count=1` |
| Client | **Type-checking only.** There is currently **no client test suite**: no `*.test.ts` / `*.spec.ts` exists, and `bun test` with no tests **exits 0** — so it asserts nothing while reporting success. Do not use it as a gate. | `bun run check` (`tsc --noEmit`) — the real client gate |
| `tools/smoke.sh` | **Live HTTP gate** (not a unit test): boots the real binary on a temp DB and asserts public pages render data (no «در دست ساخت»), competition/age pages serve, unknown ids → Persian 404, admin surface mounted (not 404) | `tools/smoke.sh [db]` or `SEED=1 tools/smoke.sh` |

DB trigger behavior is additionally verified directly in `sqlite3` (8/8
scenarios in `DATABASE.md`: premier duplicate-club ABORT, unregistered-team
ABORT, finished/scheduled score CHECKs, duplicate-fixture UNIQUE, group-shape
CHECKs).

## 2. Commands

```bash
go build ./... && go vet ./... && go test ./internal/... -count=1
bun run check && bun run build      # NOT `bun test` — it exits 0 with no tests (see §1)
tools/smoke.sh data/pabetoop-league.db
tools/db-scenarios.sh               # DATABASE.md's 8 sqlite3 scenarios, fresh DB, offline
```

`go test ./...` answers "does the code still work?"; `tools/smoke.sh`
answers "does the product work?" — the Lead runs both before integrating.

## 3. Acceptance gates (PROJECT_SPEC §10, condensed)

- [ ] All §7 integrity rules enforced **and tested** (store tests + trigger checks)
- [ ] Standings deterministic + unit-tested (ties, GD, GF, Persian-alpha)
- [ ] Jalali engine unit-tested both ways (leap years, Persian rendering)
- [ ] Full season flow doable from admin without docs
- [ ] Quick result entry ≤ 2 interactions per match
- [ ] Public pages phone-usable, RTL-polished, sport/Persian identity
- [ ] Migrations versioned; `go vet` clean; builds cleanly
- [ ] Backup download works; audit log records mutations; Persian errors everywhere
- [ ] Seed data realistic incl. tie-break edge cases

## 4. Known gaps (do not fake green)

- **The smoke gate is anonymous, so it cannot look past the login gate.** `tools/smoke.sh`
  asserts public pages render data (never «در دست ساخت»), unknown ids 404 in Persian, and that
  the admin surface is *mounted* (303 to login, never 404). Everything behind the session is
  covered by the authenticated harnesses instead: `tmp/verify-016.sh` (seasons + competitions,
  44 checks) and `tmp/verify-fresh.sh` (the §10 DoD chain on an empty DB, 64 checks, including
  TASK-029's stored team category **and its display in the teams list**). The
  route-wiring gap this bullet used to describe is **fixed** — `RegisterFixtureImportRoutes` is
  called at `server.go:185` (TASK-012), so the fixtures/import confirm and delete routes are in
  the production tree and the smoke gate's `/admin` probe is backed by real routes. The smoke
  gate still does not *exercise* them; that is scope, not oversight.
- **`/admin/teams` is now paged in the store** (TASK-025): `TeamsPage(clubID, limit, offset)`
  bounds the query and returns the true total from one shared `WHERE` (proved by
  `TestTeamsPageWalk` — no row dropped or duplicated — and mutation-proved against the shared
  filter). `TeamsPage` is on the `DataStore` interface and the `teamsPager` capability assertion
  is gone (the contract unfreeze, 2026-09-13). The one residual is scale: the `ORDER BY c.name
  COLLATE NOCASE, t.label COLLATE NOCASE` still builds a temp B-tree over the joined set, so this
  bounds rows *materialised*, not the scan. A covering index is the upgrade path if the scan ever
  matters.
- **`ROUTES.md` is now checked against the routing tree** (`internal/web/routesdoc_test.go`, `TASK-038`).
  Every documented row must resolve to **exactly** its documented pattern through `s.mux()` — a
  transcription error such as writing `GET /admin/` for `GET /admin/{$}` is caught, not just a
  missing route — and every `Register*Routes` method must be called from the routing tree, which
  catches the `TASK-012` class (defined, never wired, invisible to build/vet). Two deliberate
  negatives are asserted too: rows struck through in the doc must stay unregistered. Proof **L**.
  What it cannot see: a route that is registered, documented and simply wrong for its handler.
- **No coordination lock between concurrent writers sharing one working tree.** Two
  concurrent writers have already broken this tree once, and brief-ID collisions twice.
  A lock would help; adopting one belongs inside the project tooling, not outside it, in
  each wave is the open part.
- **`app.ts` has no tests.** Its keyboard/UI behavior is verified only by hand, and `bun test`
  cannot report that honestly because it exits 0 with an empty test set. Either add a real client
  suite or drop the `test` script from `package.json` so nobody mistakes an empty run for a pass.
- Season/competition/registration **UI creation is no longer partial**: `TASK-015` and
  `TASK-016` shipped, and `0003` seeds the age categories, so an operator can run the whole
  flow from an empty database. Remaining deliberate omissions are in `DEPLOY.md` §4.

---

## 5. §7 integrity-rule traceability matrix

> Closes the first line of `PROJECT_SPEC` §10: *"All §7 integrity rules enforced **and
tested**"*. Rule numbers are `PROJECT_SPEC` §7. `DATABASE.md` carries the mechanism detail;
> this section carries the **evidence** — the specific test that fails if the mechanism is
> removed, and nothing vaguer than a function name.
>
> Enforcement layer: **DB** = schema CHECK / UNIQUE index / trigger · **store** =
> `internal/store` validation (Persian message + sentinel) · **handler** = HTTP boundary.

| # | Rule (`PROJECT_SPEC` §7) | Enforced by | Code | Test evidence | Status |
|---|---|---|---|---|---|
| 1 | Team cannot play itself; both teams registered in the match's competition | **DB** `CHECK (home_team_id != away_team_id)`, `trg_match_teams_registered_insert/update` | store+DB | `TestCreateMatchIntegrity`, `TestFixtureCreateSelfMatchRefused`, `TestFixtureCreateUnregisteredTeamRefused` (web), `TestValidateSelfMatchIsRejected` (import) | 🟢 directly asserted, **mutation-proved (E)** |
| 2 | Duplicate registrations blocked | **store** duplicate pre-check → `ErrInUse` is the biting guard; the DB `UNIQUE (competition_id, team_id)` is its backstop (mutation left the test green — §5.3) | store+DB | `TestCompetitionRegisterDuplicateRefused` (web), `TestRegisterDuplicateRefusedByTheStorePreCheck` (store, the pre-check in isolation) | 🟢 directly asserted at both layers; the UNIQUE backstop is **asserted, not proved** |
| 3 | Premier: max one team per club per age category | **store** raises `ErrPremierClubOnce` before the insert; `trg_premier_club_unique_insert/update` is the backstop, and the `0002` partial index enforces one Premier league per (season, age) (mutation left the test green — §5.3) | store+DB | `TestPremierUniqueness` (store), `TestCompetitionRegisterPremierClubRuleRefused` (web), `TestCompetitionCreateDuplicatePremierRefused` (web, `0002`) | 🟢 directly asserted |
| 4 | Scores non-negative; FINISHED ⇔ both scores; SCHEDULED ⇔ none | **DB** `matches` CHECKs | store+DB | `TestResultLifecycle` (store), `TestSetResultRejectsInvalidScore`, `TestSetResultAcceptsPersianDigits`, `TestSetResultRepairsFinishedRow`, `TestClearResultReturnsScheduledRow` (web), `TestMatchTableCheckRefusesInconsistentScoreState` (store, **§5.2-H**, raw SQL against the table CHECK) | 🟢 directly asserted, **mutation-proved (H)** |
| 5 | Duplicate fixtures rejected | **DB** partial `UNIQUE INDEX idx_matches_unique_fixture WHERE week IS NOT NULL` | store+DB | `TestDuplicateFixtureNeedsAWeek` (store, **§5.2-A**), `TestFixtureCreateDuplicateRefused` (web), `TestValidateDuplicateFixtureInFile`, `TestValidateDifferentWeekIsNotADuplicate` (import) | 🟢 directly asserted, **mutation-proved** |
| 6 | Match references valid teams; competition matches registrations | a service-layer guard refuses first and **its exact layer is still unnamed** (§5.3); **DB** FKs + `trg_match_teams_registered_*` are the backstop | store+DB | `TestCreateMatchIntegrity`, `TestFixtureCreateUnregisteredTeamRefused` (web) | 🟢 directly asserted |
| 7 | Deleting restricted; history reconstructable; seasons never deleted | **DB** FKs as backstop; **no** hard-delete path for clubs/teams; **no** `DeleteSeason` method or route | store+DB | `TestDeleteCompetitionRefusedWhileItHasRefs` (**§5.2-B**), `TestUnregisterBlocked`, `TestDeactivateClubBlocked`, `TestAdminClubDeleteReferencedRefused`, `TestAdminClubDeleteUnreferencedDeactivates`, `TestAdminTeamDeactivateBlockedWhileRegistered`, `TestSeasonDeleteRouteDoesNotExist` | 🟢 directly asserted, **mutation-proved** |
| 8 | Import never silently mutates: preview + confirm, Persian errors, ambiguities reported | **handler** `internal/import` parse → validate → preview; confirm applies only valid rows | handler+import | `TestImportPreviewSeparatesValidAndErrorRows`, `TestImportConfirmImportsOnlyValidRows`, `TestImportConfirmTokenIsOneShot`, `TestImportConfirmReportsRowsTheStoreRejects`, `TestValidateAmbiguousSharedNameNeedsAPick`, `TestParseGarbageRowsReportExactLineNumbers`, `TestParseEmptyFileReportsPersianError` | 🟢 directly asserted, **mutation-proved (F)** |
| 9 | Standings always recomputed, never stored | **structural** — the schema has no standings table (the 8 tables in `0001` hold none); `internal/standing` is a pure function of `FinishedMatches` | none (by design) | `TestStandingsRecompute` (store), `TestFinishedMatchesExcludesScheduledMatches` (store, **§5.2-G**, the finished-only filter), `TestStandingsPartialRecomputesFromFinishedMatches`, `TestPublicCompetitionStandingsMatchCompute` (web), whole `internal/standing` suite | 🟢 directly asserted + structurally provable, and the filter is now **mutation-proved (G)** |
| 10 | Every admin mutation writes an audit row (action, entity, entity_id, before, after, time) | **store** `audit(tx, …)` inside each mutating method's transaction | store | `TestEveryMutationWritesAnAuditRow` (**§5.2-C**, sweeps all 25 steps in `mutationSteps()`), `TestAuditRowsCarryTheBeforeAfterPayload` (**§5.2-D**), `TestAuditLog`, `TestAuditLogFiltered`, `TestAuditListsKnownMutation` | 🟢 directly asserted, **mutation-proved** |

**No rule is marked "indirectly covered".** Where a rule is enforced by a DB object that no Go
unit test isolates, the row says so explicitly (rules 2, 4) rather than claiming a test it does
not have.

### 5.1 Rules whose enforcement is *only* the database

- **Rule 3's league-uniqueness half** (one Premier league per season+age, `0002`) lives purely
  in a partial unique index; it is asserted through the handler (`TestCompetitionCreateDuplicatePremierRefused`),
  which is where an operator meets it.
- **Rule 7 has a DB backstop nobody should rely on for the message.** Mutation-proof B (below)
  showed the `FOREIGN KEY` refuses the delete *even with the store guard neutered* — but as
  `constraint failed: FOREIGN KEY constraint failed (787)`, not as `ErrActiveRefs`. The store
  guard's job is the Persian refusal and the sentinel; the FK's job is to be the last line.
  That is worth knowing before "simplifying" either one.

### 5.2 Mutation proofs

Each proof neuters one enforcement mechanism in a **throwaway copy of the module** and records
the exact failing line. The shared tree is never mutated — a proof that edits shared source is
a proof that can ship a mutation to `main`.

The proofs are re-runnable as commands — `tools/mutation-proof.sh --list` to see them,
`tools/mutation-proof.sh A` for one, `tools/mutation-proof.sh --all` for every one:

```
tools/mutation-proof.sh --all     # copies the module under .openclaw/tmp/, mutates the copy only
```

The script resolves the same offline env these proofs were first run with: it copies the module
out of the tree with `tar` (excluding `vendor/` and `tools/toolchain/`) and builds `-mod=mod`
from the module cache with `GOPROXY=off`. It reports a mutation that does not bite as
`DID NOT BITE` and exits non-zero (`rule2-unique-no-bite` is a real entry for the §5.3 case), and
it hashes the real tree's mutable files before and after every proof, so a run cannot change the
shared tree.

| Proof | Rule | Mutation applied | Recorded failure |
|---|---|---|---|
| **A** | 5 | comment out `idx_matches_unique_fixture` (+ its `ON …` / `WHERE …` lines) in `0001` | `store_test.go:1138: a duplicate fixture with the same week was accepted` |
| **B** | 7 | `if regs > 0 {` → `if false {` in `DeleteCompetition` | `store_test.go:906: delete with registrations → constraint failed: FOREIGN KEY constraint failed (787), want ErrActiveRefs` |
| **C** | 10 | early `return nil` at the top of `audit()` | `store_test.go:1102: CreateSeason wrote 0 audit rows, want exactly 1` |
| **D** | 10 (payload) | pass `nil, nil` instead of `before, after` to the `audit_log` INSERT | `store_test.go:1186: insert after = <nil>, want the created club's name` |
| **E** | 1 | `if homeTeamID == awayTeamID {` → `if false {` in `CreateMatch`, **plus** `CHECK (home_team_id != away_team_id)` → `… OR 1=1` and the trigger's `WHEN NEW.home_team_id = NEW.away_team_id` clause disabled in `0001` | `store_test.go:336: self match → تیم(های)  در «تست» ثبت‌نام ندارند, want ErrSelfMatch` |
| **F** | 8 | comment out `delete(importPreviews.items, token)` in `takeImportPreview` (the one-shot consume) | `import_handlers_test.go:225: a replayed token must be refused in Persian, got: ` |
| **G** | 9 | `AND status = 'finished'` → `AND 1=1` in `FinishedMatches` (the filter dropped) | `integrity_evidence_test.go:59: FinishedMatches: sql: Scan error on column index 2, name "home_score": converting NULL to int is unsupported` |
| **H** | 4 | delete the two `status`/`score` pairing `CHECK` lines from `0001_init.sql` (and the now-dangling comma on the preceding `CHECK`, so the migration stays valid SQL) | `integrity_evidence_test.go:134: raw UPDATE set home_score on a scheduled row, want the matches CHECK to refuse it` |
| **I** | — (P3 persistence) | bind the **old** category in `UpdateTeam`'s `UPDATE` (`int64FromPtr(newAge)` → `oldAge`), leaving the validation intact | `team_update_test.go:68: age_group_id = 0x…, want the edited category 5 — the edit silently dropped it` (+ `:72` for the stale joined name) |
| **J** | 10 (payload) | report the **old** category on the audit row's `after` side (`int64FromPtr(newAge)` → `oldAge`) | `team_update_test.go:151: audit after does not record the new category (want "age_group_id":1)` |
| **K** | — (route reachability) | unwire the two `TASK-037` routes from `RegisterAdminRoutes` | `team_edit_test.go:46: GET edit form = 404, want 200` and `:110`/`:137`/`:169` `update team = 404, want 303`/`422` |
| **L** | — (route table) | unwire `s.RegisterSeasonRoutes(mux)` from the routing tree | `routesdoc_test.go:139: (*Server).RegisterSeasonRoutes is defined but never called from the routing tree … (this is the TASK-012 bug)` + `:119` naming each missing `/admin/seasons*` row |
| **M** | 2 | neuter **both** layers: the store's duplicate pre-check (`if false {`) **and** `UNIQUE (competition_id, team_id)` → `UNIQUE (id)` in `0001` | `integrity_evidence_test.go:181: second Register = <nil>, want ErrInUse from the store pre-check` |
| **N** | 3 | the store's premier pre-check: `if clubHasTeam {` → `if false {` | `competition_handlers_test.go:367: 422 must name the blocking club, got: <div class="flash error">constraint failed: یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد (1811) …` |
| **O** | 6 | the handler's pre-write registration mirror: `!registered[int64(homeID)] \|\| !registered[int64(awayID)]` → `false` | `fixture_handlers_test.go:226: refusal must be Persian and specific, got: ` |

Proof **E** had to neuter **three** layers (the store guard and both DB objects) before the test
went red — rule 1 is deliberately defense-in-depth, and that is worth knowing before anyone
"simplifies" one layer away.

Each mutation was reverted and the suite re-run green immediately afterwards. Proof **D** exists
because proof **C** alone was not enough: dropping the payload from every row still left the row
count correct, so the sweep passed. That gap was found by writing the matrix, which is the
argument for writing one.

### 5.3 Honest limits

- **All ten rules are mutation-proved** (1–10; proofs A–H for 1, 4, 5, 7, 8, 9, 10, then
  M, N, O for 2, 3 and 6). "Asserted" and "proved" are different claims and this matrix still
  keeps them apart — the same single-layer mutations that stayed green are kept in the harness as
  deliberate `nobite` entries (`rule2-unique-no-bite`, `rule2-premier-shadow-no-bite`), so it
  cannot rubber-stamp a proof that no longer bites.
- **Not every claim worth proving is a §7 rule.** Proofs **I–L** cover four non-rule claims that
  would otherwise have shipped as assertions: that the team edit persists the category (not just
  accepts it), that its audit payload records the category that *changed*, and that the two routes
  are actually wired. Proof **K** is the cheapest of the three and catches the `TASK-012` class —
  a registrar that compiles, vets and passes every other test while its whole route surface is gone.
- `TestEveryMutationWritesAnAuditRow` sweeps the **store** contract. A future mutating handler
  that bypasses the store would not be caught by it.
- **Rules 2, 3 and 6 bite at a layer *above* the DB object — that layer is now named and proved.**
  TASK-031 mutated the DB object and every test stayed green; the mutation was aimed one layer too
  low. Each rule refuses at the service or handler layer first, in this order:
  - **Rule 2** — the store's duplicate pre-check is the biting guard, and the `UNIQUE` is its
    backstop. Neuter only the `UNIQUE` and `TestCompetitionRegisterDuplicateRefused` stays green
    (proof `rule2-unique-no-bite`, kept deliberately). Neuter **both** — proof **M** — and
    `TestRegisterDuplicateRefusedByTheStorePreCheck` goes red.
  - **Rule 3** — `ErrPremierClubOnce` is raised by the store pre-check before the insert, so
    replacing both `trg_premier_club_unique_*` `RAISE(ABORT, …)` bodies with `SELECT 1;` leaves
    `TestPremierUniqueness` green. Proof **N** neuters the pre-check (`if clubHasTeam {` →
    `if false {`) and the test goes red — with the message degrading to the raw
    `constraint failed: …` SQLite text, which is precisely what the pre-check exists to prevent.
  - **Rule 6** — the first refusal point is **the handler, not the store**:
    `internal/web/fixture_handlers.go:186` mirrors the registration rule before writing and
    answers with the Persian message «هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند.».
    That is the "something above the store refuses first" TASK-031 recorded without naming it:
    neutering the store's `if registered < 2` guard (`store.go:1258`) **and** the trigger still
    left `TestFixtureCreateUnregisteredTeamRefused` green, because the handler answers first.
    Proof **O** neuters the handler condition and the test goes red.
- **Rule 9 is structural, and its filter is proved separately.** No standings table exists in
  `0001_init.sql` (the 8 tables are clubs, teams, age_groups, seasons, competitions, registrations,
  matches, audit_log), and `internal/standing.Compute` is a pure function of `FinishedMatch`
  values — so the *rule* is structural, not a stored-state invariant. The one part that **is**
  stored-state is the finished-only filter in `FinishedMatches`, and it is now proved: proof **G**
  drops it and `TestFinishedMatchesExcludesScheduledMatches` goes red. Note the mechanism — a
  leaked scheduled row has NULL scores and `FinishedMatch.HomeGoals` is a plain `int`, so the scan
  itself refuses the row. That is the filter biting, but it means the count and standings
  assertions downstream are the second line of defence; if those fields ever become nullable, they
  become the first.
