# DECISIONS — Pabetoop League

Each decision: context → choice → consequence. Later decisions may supersede earlier ones.
**Standing rule: pinned stack versions (htmx 4, TypeScript 7, Go) are requirements, not
suggestions — never silently substitute or "downgrade for safety."**

> This log was written while the platform was built. Entries that described how the
> original team divided its work — rather than how the product behaves — were removed
> when the codebase was released as a template; their numbers remain as tombstones so
> cross-references from earlier commits, code comments and review notes stay readable.

---

## D1 — Stack: Go 1.26 + SQLite (pure-Go driver) + html/template + htmx 4 + TS 7 (2026-09-11)

**Context.** Build prompt prefers Go + server-side rendering + HTMX 4 + TS 7 + PostgreSQL-or-similar.
Reconnaissance: Go 1.26.5 ✓, no native PostgreSQL, SQLite 3.37.2 ✓, container runtime available,
single admin, modest write volume, no hosting budget, a small VPS as the target.

**Choice.**

```text
Go 1.26 (stdlib net/http ServeMux; no web framework)
SQLite via modernc.org/sqlite (pure Go — no cgo), WAL mode, single file at data/
html/template SSR, fully RTL Persian
htmx 4.0.0 (vendored into web/static/js/)
TypeScript 7.0.2 for minimal client enhancement
Bun 1.4 as the JS toolchain (see D4)
```

**Consequence.** Zero-ops DB (copy file = backup), no cgo toolchain risk, trivial deployment
(single binary + data dir). Growth path to Postgres = repository layer already isolates SQL;
documented, not built. PostgreSQL container compose file provided as optional for the future.

---

## D2 — htmx 4.0.0 pinned; never downgrade to 2.x (2026-09-11, client-corrected)

**Context.** npm's `latest` dist-tag for htmx.org is still 2.0.10 (until early 2027); 4.0.0 ships
under `next`. I initially wrote "htmx 2" into the spec out of caution. **Client rejected this:
pinned stack is a requirement.** htmx 4.0.0 GA'd 2026-08-28 (four.htmx.org) and is production-stable.

**Choice.** Pin exactly `htmx.org@4.0.0` in package.json; vendor `dist/htmx.min.js` as
`web/static/js/htmx-4.0.0.min.js` (no CDN at runtime). htmx-4-specific rules for all templates:

- **Explicit inheritance**: `hx-confirm` / `hx-headers` on a container reach children only with the
  `:inherited` suffix (e.g. `hx-headers:inherited='{"X-CSRF-Token":"..."}'`). Our CSRF attr MUST use
  this — the upgrade-checker example itself flags exactly this case.
- Events: `htmx:before:request`, `htmx:after:request`, `htmx:after:swap`, `htmx:error` (old camelCase
  names do not exist in 4).
- 4xx/5xx **swap by default** → server-rendered Persian validation errors land in `hx-status:422="target:#form-errors"`.
- `<hx-partial>` for multi-target responses (e.g. result entry updates row + standings + flash at once).
- Config: `htmx.config.defaultTimeout` (not `timeout`); removed attrs (hx-ext, hx-vars, hx-inherit,
  hx-disinherit, hx-params) must not appear in templates.
- Run `npx htmx.org@4.0.0 upgrade-check -- .` if ever porting old templates (not needed for greenfield).

**Consequence.** Reference kept at `docs/htmx4-reference.md` (full official docs.md, 2020 lines).

---

## D3 — TypeScript 7 (native/Go compiler) (2026-09-11)

**Context.** Build prompt asks TS 7. Verified on this box: npm `typescript@7.0.2` is `latest` (GA
2026-07-08), binary is plain `tsc` (preview-era `tsgo` folded back in). Type-checking logic is
structurally identical to TS 6 — no language risk.

**Choice.** `typescript: 7.0.2` (dev dependency role; bundled via Bun). Only client enhancement code
is TS (quick-entry keyboard flow, small UI niceties). Server owns all business logic — TS never
duplicates domain rules.

**Consequence.** tsconfig must respect TS 7 removals: no `target: es5`, no `baseUrl`, `types: []`
default behavior. Non-issue for a small greenfield client bundle.

---

## D4 — Bun as the JavaScript toolchain (2026-09-11, client direction)

**Context.** Client asked the JS side be "based on Bun". Bun 1.4.0 is installed (`~/.bun/bin/bun`).
Backend remains Go (pinned in the build prompt) — Bun covers the JavaScript surface only.

**Choice.**

- `bun` = package manager (project-local `package.json` + `bun.lock` at `projects/pabetoop-league/`)
- `bun build` = bundler for `web/static/js/app.ts` → minified `app.js`
- `bun test` = client-side tests
- `bunx tsc` = TS 7 type-checking (`bun run check`)
- htmx stays vendored/static (no runtime dependency on Bun or Node)

**Consequence.** `node_modules/` is git-ignored; CI/dev needs bun installed. Documented install
(`curl -fsSL https://bun.sh/install | bash`). Runtime production dependency: **none** (server is Go).

---

## D5 — Dependency source: install directly, never through a proxy (2026-09-11)

**Context.** Package installs went through a regional npm mirror. That mirror only
answers from a domestic address; routing it through an outbound proxy gives a foreign
exit IP and the mirror refuses (or, worse, silently serves a stale copy).

**Choice.** Package managers talk to the registry **directly**, with no proxy variables
in any script or tooling. If a regional mirror is required, configure it in the user's
package-manager config (e.g. `~/.npmrc`) rather than in per-project scripts, so the
choice is visible and reversible in one place.

**Consequence.** A long first install is expected and normal; the lockfile makes it a
one-time cost. Related pitfall worth keeping: some package managers walk **up** the
directory tree looking for a manifest, so always create the project manifest before the
first install, or the registry root gets a stray `package.json`.

## D6 — Standings & tie-break (2026-09-11)

**Choice.** Deterministic engine, single module (`internal/standing`): Points→GD→GF→Persian-
alphabetical (collation via Persian-normalized compare). Win 3 / Draw 1 / Loss 0. Computed on demand
from FINISHED matches; never persisted as truth. Home/away goals kept separately in schema; the
"away goals matter more" official interpretation is UNKNOWN → no formula invented; ordering hook
isolated so an official rule can slot in. Assumption documented in PROJECT_SPEC §8 (A1).

---

## D7 — Identity rules: administrator is the authority (2026-09-11)

**Choice.** Official names stored exactly as entered (trim whitespace only). No auto-merge of
«نمونه ب» with «نمونه ب نوین»; no auto-rename of «نمونه ب ۱/۲/۳». Fuzzy/normalized matching is used
ONLY to *suggest* during import ("شاید منظور شما… نمونه ب نوین است؟") and never as final authority.
Club ≠ Team in schema; one-club-per-Premier-League-per-age enforced by DB partial unique index.

---

## D8 — Fixtures are official data, not generated (2026-09-11)

**Choice.** Admin enters fixtures manually or imports them (preview → confirm pipeline). No fixture
generator in the product UI. (The seed tool has a round-robin generator for realistic dev data only.)

---

## D9 — Jalali dates centralized (2026-09-11)

**Choice.** Canonical storage = ISO date (UTC-agnostic DATE, and Iran is single-offset +03:30 since
DST abolition). One `internal/jalali` package converts ⇄ formats (۱۴۰۵/۰۷/۲۰ + month names + Persian
digits). Templates never do math on dates. The old prototype's hand-rolled JD math was wrong — the
new engine is unit-tested against known anchors (leap years, month lengths, round-trips).

**Implementation note (added 2026-09-11).** Conversion inside that package delegates to
`github.com/yaa110/go-persian-calendar` (the client's "proven implementations, not hand-rolled
math" direction) rather than being re-derived. This does **not** weaken "single conversion point":
the library is never imported outside `internal/jalali`, so every Jalali⇄Gregorian conversion in
the product still flows through one package. Also fixed this day: `Parse` used `fmt.Sscanf("%d")`,
which prefix-scans and silently accepted digit-prefixed garbage (`"1464ع/08/19"` → a date, no
error); it now validates every character and returns a Persian error, with a regression test.

---

## D10 — Data integrity & audit (2026-09-11)

**Choice.** DB constraints (FKs, CHECKs, partial-unique for Premier uniqueness, unique registrations)
+ service-level validation with Persian error messages. Every admin mutation → audit log row
(action, entity, entity_id, before, after, at, admin). Destructive ops require typed confirmation.
Hard deletes blocked when history would break (deactivate instead).

---

## D13 — Tombstone: internal worker-coordination workflow

This entry recorded the file-based channel used to brief parallel workers. It
described how the original implementation team divided its work, not how the product
behaves, so it was removed for the public release.

The number is kept so cross-references from earlier commits and review notes still
resolve. Nothing in the shipped code depends on it.

## D12 — Prefer existing tooling to ad-hoc scripts (2026-09-11)

**Choice.** Before writing a script or a new tool, check whether the repository, the
local toolchain or a maintained library already solves the problem. Prefer, in order:
something already in this repo, the language standard library, a pinned dependency
already in `go.mod`, then new code. Document *why* when you go past step three.

**Consequence.** Most problems in this codebase are solved by the standard library
(`net/http`, `html/template`, `database/sql`) plus htmx. The pull toward adding a
framework, an ORM or a build tool is usually the thing to resist, and this entry is the
standing permission to say so in review.

## D14 — Competition tab URLs use the path form (2026-09-11, integration decision)

**Context.** The shipped `competition.html` (TASK-005) links `/competition/{id}/{tab}`; the
TASK-013 brief specified `?tab=`. Both worked, which is worse than one working.

**Choice.** `GET /competition/{id}/{tab}` is canonical. `?tab=` remains accepted for deep links
already in the wild; new links (admin, homepage, docs) use the path form.

**Consequence.** One URL shape in tests and docs. TASK-011/012 briefs updated.

---

## D15 — One Premier League per (season, age) is enforced by a partial unique index (2026-09-12)

**Context.** `PROJECT_SPEC` requires exactly one Premier League per age category in a season.
D10 assumed the table-level `UNIQUE (season_id, age_group_id, level, group_name)` covered it. It
**does not**: SQLite treats NULLs as distinct from each other in a UNIQUE index, and premier rows
carry `group_name = NULL` (enforced by the CHECK in `0001_init.sql`). A second Premier League for
the same season+age was created successfully from the admin UI.

**Discovery.** Not by review — by writing the TASK-015 acceptance test
(`TestCompetitionCreateDuplicatePremierRefused`); it failed on first run. Existing seeded data was
then checked and found clean (19 competitions, 0 conflicting groups) before the index was added.

**Choice.** `migrations/0002_competitions_premier_unique.sql`:

```sql
CREATE UNIQUE INDEX idx_competitions_premier_unique
    ON competitions(season_id, age_group_id)
    WHERE level = 'premier';
```

`CreateCompetition` names the rule in Persian when this fires («برای این رده سنی در این فصل، لیگ برتر
قبلاً ساخته شده است»). League 1 needs no equivalent — its `group_name` is NOT NULL, so the
table-level UNIQUE already fires.

**Consequence.** Rule enforced at the DB level, not only in service code, so an import or a future
second writer cannot bypass it. Lesson recorded: "a UNIQUE constraint with a nullable column does
not mean what it looks like" — every nullable-column uniqueness rule needs a partial index.

---

## D16 — Dependencies resolve from the module proxy; nothing is vendored (2026-09-12)

**Context.** The original build committed both halves of the Go toolchain into the
repository — a Go toolchain tarball (~64 MB) and `vendor/` (~137 MB) — so that a clone
could build with zero network access behind a heavily filtered connection.

**Choice.** Remove both. The template resolves modules from the public proxy and pins
them with `go.sum`:

- `vendor/` copied every dependency into the tree, which means a security fix reaches
  you only when someone re-runs `go mod vendor` — the usual way a vendored repository
  quietly runs a two-year-old TLS stack.
- The toolchain tarball pinned one Go release and cost 64 MB in every clone.
- `go.sum` plus `GOPROXY` is already reproducible, and `go mod download` is a one-command
  cache warm for an offline host.

**Consequence.** A fresh clone needs network once. On a host where the registry is
filtered, point `GOPROXY` at a trusted mirror — but **never** at a mirror that cannot
satisfy `go.sum`; a proxy that serves a different module for a known hash is a supply-chain
incident, not a slow download.

**Known host pitfall.** A globally-exported `GOFLAGS=-mod=vendor` (written by some other
project's tooling into `~/.config/go/env`) makes a non-vendored module fail with the
misleading message `inconsistent vendoring in <dir>` even when no `vendor/` exists.
Override per command — `GOFLAGS=-mod=mod go build ./...` — instead of editing the shared
host config.

## D17 — Flash messages work in the same response, not only after a redirect (2026-09-12)

**Context.** `setFlash` wrote `Set-Cookie` headers; `takeFlash` read `r.Cookie(...)`. That pair only
works across a redirect: some handlers (a failed create) call `setFlash` and then render in the
**same** response, where the request has no flash cookie, so `takeFlash` returned empty. Result: a
duplicate club create answered 200 with **no message at all** — it looked like a successful no-op to
the operator, straight against the product's "Persian errors everywhere" rule.

Two compounding causes found in sequence: (1) the values were also being **silently stripped** by
net/http, because a cookie value may only hold ASCII 0x21–0x7e and Persian text is not (fixed by
percent-encoding at the single write/read pair), and (2) the same-response gap above.

**Choice.** `setFlash` now writes three things: the two `Set-Cookie` headers (unchanged, for the
redirect path) **and** the same values attached to the current request via `r.AddCookie`, under
distinct names (`flash_now_msg` / `flash_now_kind`). `takeFlash` checks the current-request pair
first and, when it hits, does **not** clear the pending cookies — they are still owed to the next
request. Distinct names are required because `Request.Cookie` returns the first match and a browser
may legitimately have sent a real `flash_msg` on this request.

**Consequence.** No middleware, no signature change, no per-handler bookkeeping — every existing
`setFlash` caller is fixed at once, and read-once semantics survive on both paths (asserted live).
`internal/web` is now at **100 passing tests with zero skips**; the two tests that pinned this bug
(`TestAdminClubCreateDuplicateShowsError`, `TestAdminClubCreateBlankNameShowsError`) had used
`t.Skipf` as a stand-in for the fix and now assert it.

**Lesson.** A "known bug" test that skips is a bug that has been *documented, not fixed*. When a test
skips on purpose, the skip reason should name the owning file — then the next Lead session can close
it in one pass.

---

## D11 — Auth: single admin (2026-09-11)

**Choice.** One admin account (bcrypt hash, salted), session cookie (HttpOnly, SameSite=Lax, Secure
behind TLS), login rate-limited, CSRF token on all admin forms + `:inherited` htmx header. No RBAC,
no MFA, no social login. Public is anonymous. Password set via env/bootstrap on first run, changeable
in admin UI.

---

## D18 — Seasons screen: no delete route, and activation is a full-page POST (2026-09-12)

**Context.** The store implemented seasons and the single-active rule, but no screen
existed, so a fresh install could not create the season every other record hangs off
(an internal audit finding, F2). TASK-016 closed that. Three product rules are encoded in the shape of
the surface rather than in prose, so they cannot drift:

**1. There is no delete route for a season — and no disabled button hinting at one.**
§7 rule 7: seasons are never deleted. Deleting one would orphan its competitions,
their registrations and their matches, and the audit trail refers to them by id.
The absence is asserted twice (a unit test and a live check both require a 404 on
`POST /admin/seasons/{id}/delete`) and once more from the other side (a scan proving
the rendered page contains no `…/delete` control). A `/admin/seasons/{id}` POST is
also 404 — there is no partial-match handler that could grow into a delete later.

**2. Activation is a plain form POST + `303`, not an htmx row swap.** Every other row
mutation in this admin uses `hx-target="closest tr"` and swaps one row. Activation is
the exception because it changes **two** rows: the new active season and the one losing
the badge. A single-row swap cannot express that, and a stale second «فعال» badge is
precisely the failure this screen exists to prevent. A full reload is the only answer
that cannot show two active seasons. The page's own invariant is asserted live
(exactly one active badge in the markup, and `COUNT(*) WHERE is_active = 1` = 1).

**3. Activating a season with zero competitions succeeds and warns.** The operator may
legitimately activate first and build the competitions afterwards, so this is not an
error and must not block. The warning rides the success flash in Persian
(«… توجه: این فصل هنوز هیچ مسابقه‌ای ندارد؛ …») — visible inline on the resulting page,
semantically not an error, and asserted by kind (success, not error) in both a unit
test and the live script.

**Consequences.** `internal/web` moved from 100 to 111 passing tests with zero skips;
`tmp/verify-016.sh` adds 44 live HTTP checks over a seeded DB. `ActivateSeason` was
already transactional in the store, so the handler never touches the rule itself —
the single-active invariant has exactly one implementation.

**Note.** `store.ErrDuplicateName`'s sentinel text is club-flavoured
(«باشگاهی با این نام قبلاً ثبت شده است»). Season and competition paths wrap it with
the right noun, so the operator always reads the correct sentence; flagged in
`OUTBOX/TASK-016-REPORT.md` for whoever closes the store lane.

---

## D19 — The five age categories are seeded by migration, not by the seed command (2026-09-12)

**Problem.** Fixing the same "a fresh install is a dead end" finding (F2) with the seasons screen
(D18) only *moved* the dead end one screen later. `0001_init.sql` creates the
`age_groups` table but inserts no rows, and the only caller of `EnsureAgeGroups` in the
whole repository was `cmd/seed`. So on a brand-new database — `Migrate()` + `cmd/server`,
no `SEED=1` — the operator could create a season and activate it, then find the
competitions form's «ردهٔ سنی» select with nothing in it. No competition could ever be
created, which blocks the client's headline Definition-of-Done item: *admin can do a full
season flow without documentation* (§10).

**Choice.** Migration `0003_age_groups_seed.sql` inserts the five fixed MVP categories
(10–14) with the exact labels `persianAgeLabel()` produces («۱۰ سال» … «۱۴ سال») and
`sort_order = age`.

**Why a migration and not a code change.** The category set is fixed for the MVP but the
schema deliberately keeps it in a *table* rather than an enum ("so future categories need
no rewrite", 0001). Reference data belongs with the schema that depends on it, versioned
in `schema_migrations`, applied exactly once, and applied identically on every install.
Calling `EnsureAgeGroups` at server boot would also work but would make startup write
domain rows on every boot forever, and would leave a fresh install broken for anyone who
runs the binary without it.

**`INSERT OR IGNORE` is required, not defensive.** This migration also runs on an existing
seeded database, where those rows already exist. A plain `INSERT` would violate
`UNIQUE(age)`, abort `Migrate()`, and take the server's boot with it — turning a fix into
an outage on every production database. Proven both ways before landing
(`tmp/verify-fresh.sh`): a new file gets 5 categories; a seeded file keeps exactly 5,
booting normally with 0003 recorded.

**No audit rows.** Install-time reference data, not an administrator mutation, so
§7 rule 10 does not apply. `EnsureAgeGroups` (which the seed path and every
test still use) remains idempotent on top of the migration.

**Consequence.** `TestFreshInstallSeedsAgeCategoriesFromMigrateAlone` and
`TestFreshInstallCompletesTheSeasonFlow` (new `internal/web/fresh_install_test.go`)
deliberately seed **nothing**: no `seedTestData`, no `EnsureAgeGroups`, no direct
INSERTs. A test that seeds itself cannot prove an install is usable — it proves the
fixtures are. The second test walks season → activate → competition through
`Server.Handler()`, which is the only way this class of bug stays fixed.

---

## D20 — A team's age category is stored; the frozen create path keeps its signature (2026-09-13)

**Context.** `handleAdminTeamCreate` validated a **required** `age_group`, then called
`store.CreateTeam(clubID, label, displayName)` — which had no such parameter — and flashed
«ثبت شد.». The `teams` table had no column. The operator answered a required question and the
answer was silently discarded: a required field that is ignored is worse than a missing one,
because nothing in the UI tells you it went nowhere. `PROJECT_SPEC` §4/§27 make a team's identity
club + age category + group, so the model could not express a spec requirement.

**Choice.** Migration `0004_teams_age_group.sql` adds `age_group_id INTEGER REFERENCES
age_groups(id)`, **nullable by design**; `CreateTeamWithAgeGroup(clubID, ageGroupID *int64, …)` is
the new create path and the frozen `CreateTeam(clubID, label, displayName)` delegates to it with a
nil category, so every existing caller (`cmd/seed`, the whole test suite) compiles unchanged and
stores NULL.

**Why nullable, and why no backfill.** A pre-0004 team's category is **not derivable** — the
competition it is registered in carries its own `age_group_id`, and a team may be registered in
none or several. Inventing a value would be fabricating official data (D8). Historic rows stay NULL
and the column says so.

**Why not change `CreateTeam`'s signature.** `internal/store/api.go` is frozen this run and
`CreateTeam` has out-of-lane callers. A non-existent category is refused with a Persian
«رده سنی با شناسه %d یافت نشد» rather than a raw FK error.

**Consequence / open question.** `UNIQUE (club_id, label)` was deliberately **not** widened to
`(club_id, age_group_id, label)`: with a category now stored, a club still cannot hold the same
label in two categories. Widening it would silently change existing semantics and the import
matcher (which matches by display name), so it is a NAG, not a rogue edit. The handler reached the
new method through a local capability assertion while `DataStore` was frozen; D24 removed that
assertion once the contract carried the method. Fixing create-only left the *edit* path without a
category parameter (`UpdateTeam` has none) — a known gap, recorded here rather than tracked elsewhere.

---

## D21 — An advisory lock must expire, and must never block a session (2026-09-13)

**Context.** Two writers sharing one working tree corrupted this tree once (a duplicated
handler registration, about a second apart), which is what motivated a lock in the first
place.

**Choice.** Any lock this project adds is **advisory and always expirable**:

- One file per (resource, holder), written to a temporary path and `mv`-renamed into
  place, so a lock is never rewritten in place and a crash cannot leave a half-written
  claim.
- Ownership is matched on a field *inside* the file, never on the encoded filename.
- A lock at or past its TTL is **stale** and clears itself; a live lock is never broken.
- Diagnostics warn about a lock but never fail because of one, and a held lock never
  prevents a session from starting.

**Why it never blocks.** A lock that can wedge the tool it protects is a worse defect
than the race it prevents. Choose a TTL by asking how long the longest legitimate holder
can run, then add headroom — not the other way round.

## D22 — Anchor ignore patterns, or they claim more than you think (2026-09-13)

**Context.** The project `.gitignore` had a bare `bin/`, meant to exclude the compiled
binary at `./bin/`. A bare pattern matches at **any** depth, so it also swallowed every
nested `bin/` directory in the tree — including tracked helper scripts, which were then
absent from a fresh clone and deletable by `git clean -xfd`.

**Choice.** Root-level build output is ignored with an anchored pattern (`/bin/`). Verified
in both directions: `./bin/<binary>` is still ignored, and nested `bin/` directories are
trackable.

**The generalisable lesson.** An unanchored ignore pattern is a claim about *every*
directory with that name, not about the one you were looking at. The same applies to
`data/`, `dist/` and `tmp/`: anchor them, or scope them explicitly with a re-include —
and remember that git cannot re-include a file inside an ignored **directory**, so a
whole-directory exclusion can never be selectively undone.

## D23 — "Enforced and tested" is proved by matrix + mutation proof, not by test count (2026-09-13)

**Context.** `PROJECT_SPEC` §10 requires all ten §7 rules to be **enforced and tested**, but the
audit could not point at a test per rule — only at "lots of tests".

**Choice.** `TESTING.md` §5 is a matrix: per rule, the DB object and store/handler code that
enforce it, the **named** test functions that assert it, and an honest status. Rules enforced by a
DB object with no isolating Go test say so (rules 2, 4) rather than claiming coverage. Four rules
carry **mutation proofs** — the enforcement was neutered in a throwaway copy of the module, the
exact failing line recorded, the copy reverted. The proofs run against a **copy, never the shared
tree**, because a proof that mutates shared source can ship a mutation.

**What it caught that a test count could not.** The audit-row sweep pinned action, entity and
row-count, so a change that kept writing rows while dropping the `before`/`after` payload passed it.
The matrix exposed the hole and the new test closes it. A second proof showed rule 7 has *two*
enforcement layers — the store guard's job is the Persian refusal and the sentinel; the `FOREIGN
KEY` is the actual blocker. Both results are in §5.1/§5.2.

**Consequence.** Updated 2026-09-13: **seven** rules are mutation-proved (1, 4, 5, 7, 8, 9, 10 —
proofs A–H) and rules 2, 3, 6 remain **asserted only**; §5.3 records why (for each, a store guard
bites before the DB object, so neutering the object leaves the test green — which is itself the
finding). Treat "asserted" and "proved" as different claims.

---

## D24 — `api.go` is a contract, not a freeze: the methods the code needed move onto `DataStore` (2026-09-13)

**Context.** `internal/store/api.go` has been frozen since TASK-010 as a **process** rule — one
writer on the contract, workers NAG and the Lead changes it — and it worked: no worker ever edited
it. But it was frozen for several runs in a row, and the code outgrew it. `TeamsPage` (TASK-025)
and `CreateTeamWithAgeGroup` (TASK-029) landed on `*Store` and the handler reached them through
local capability type assertions (`teamsPager`, `teamAgeGroupCreator`); `store.Team` had no
`AgeGroupID`, so a field the operator was **required** to supply could not be shown back to them.
One of those workarounds silently dropped data if the assertion failed.

**Choice.** Unfreeze once, deliberately, and pay the whole debt in one pass:

- `TeamsPage(clubID, limit, offset)` and `CreateTeamWithAgeGroup(clubID, ageGroupID, label,
  displayName)` join the `DataStore` interface, documented to the same standard as their neighbours
  (clamps, ordering, error text).
- `store.Team` gains `AgeGroupID *int64` (nullable by design, D20) and `AgeGroupName string`, the
  joined `age_groups.display_name`.
- Both capability assertions are **deleted**; the handler makes plain interface calls.
- `TeamAgeGroupID` is **deleted**. It existed only because `Team` had no field — its own doc comment
  said so. Keeping a redundant accessor whose stated justification has expired is how a codebase
  accumulates answers to questions it no longer asks.

**Why the two methods and not the whole backlog.** A contract change is a cross-cutting event: it
invalidates every caller's assumptions about what `DataStore` is. So it lands as one reviewable
commit with the change announced before it is made — never spread across several sequential
patches, where a half-migrated interface is the normal outcome. Remaining gaps are recorded
explicitly rather than smuggled in.

**Consequence, and the lesson.** The "frozen" label had become self-justifying: work landed with a
capability assertion *because* the file was frozen, and the assertion became the reason not to
unfreeze it. The freeze should be re-evaluated when it starts producing workarounds — and a field
the operator must fill in but cannot see is the signal that it did. Proof the change is real, not
cosmetic: the teams list now renders «ردهٔ سنی», and both new tests fail if the plumbing is removed
(`team_age_group_test.go:52` when `Team.AgeGroupID` is not populated; `:60` when the template cell
is dropped).

## D25 — A team edit can change the label, the name and the category — never the club (2026-09-13)

**Context.** `PROJECT_SPEC` §6 promises “clubs/teams CRUD”. Clubs had create, rename and
deactivate; teams had create and deactivate, so the **U** was simply missing. `store.UpdateTeam`
existed on the `DataStore` interface and was called by **no handler and no route** — dead code — and
it could not carry the age category D20 added. Wiring the form to it unchanged would have marked a
field the form *requires* as editable and then silently discarded it, which is the same defect
TASK-029 fixed on the create side, one layer along. (Known gap, recorded here.)

**Choice.**

- **One method, updated in place**: `UpdateTeam(id, label, displayName string, ageGroupID *int64)`.
  No `UpdateTeamWithAgeGroup` sibling — D24's lesson is that a `…WithAgeGroup` variant invented to
  dodge a frozen signature becomes workaround debt the moment the freeze lifts, and this method had
  exactly zero callers to stay compatible with.
- **`ageGroupID == nil` means “leave the category unchanged”, not “clear it”.** The edit form always
  submits a category (it is required, as on create), so `nil` can only arrive from a programmatic
  caller — and for that caller “do not touch what I did not send” is the safe default: a bug that
  drops the field must not be able to wipe stored data. `age_group_id` stays nullable for the
  pre-`0004` rows D20 describes, not as a state the admin UI can produce.
- **The club is not writable.** A team's club anchors its display name, its `UNIQUE (club_id, label)`
  constraint and every registration in its history; a wrong club is a deactivate-and-recreate, not
  an edit. The form therefore renders the club select **disabled** (with a help line saying why)
  rather than offering a control that silently does nothing — a locked control tells the truth.
- **`TeamByID` joins the contract.** The edit screen needs one row; `Teams(nil)` would load all 219
  to edit one. Single-row reads belong next to the paged read that replaced them.
- **The audit payload carries every field the call can change**, `age_group_id` included, so a
  `null → value` transition is visible in the audit log. A payload that omits a field cannot show
  that field changing, and the mutation proof below is what proves it can.

**Consequence.** P3 is closed and `UpdateTeam` is no longer dead code — its deadness is now a test
failure rather than a reading exercise: unwiring the GET route fails `team_edit_test.go:46`, unwiring
the POST route fails `team_edit_test.go:110`/`:137`/`:169`, binding the old category fails
`team_update_test.go:68`, and dropping the category from the audit payload fails
`team_update_test.go:151` (proofs I–K). The club limitation is recorded rather than left implied: a
future “move team between clubs” is its own task, with its own question about what happens to the
history.
