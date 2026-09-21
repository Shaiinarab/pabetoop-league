# DEVELOPMENT — Toolchain, commands, conventions

> Pinned versions are requirements, not suggestions (DECISIONS.md D1–D5).
> `TASK-nnn` references throughout the docs are historical work items from the
> original build; they are kept because code comments cite them.

## 1. Prerequisites

- **Go 1.26** (`go version` → 1.26.x). Pure-Go SQLite — no cgo/gcc needed.
- **Bun 1.4** (optional) — JS package manager and client tests.
- **sqlite3 CLI 3.37+** (optional, for inspect/backup).
- **No Postgres, no Node runtime required.** htmx is vendored
  (`web/static/js/htmx-4.0.0.min.js`); production serves zero JS toolchain.
- **Proxy discipline:** export `NO_PROXY=127.0.0.1,localhost` in every shell
  that touches localhost, or a global proxy will capture local requests. Keep
  registry traffic off the proxy if you use a regional mirror (D5).

## 2. First run

```bash
bun install                                  # optional; one-time
ADMIN_PASSWORD='choose-a-password' SEED=1 tools/serve.sh --background
tools/smoke.sh data/pabetoop-league.db
```

Seed shape (`cmd/seed`, deterministic seed 1405): season «۱۴۰۵–۱۴۰۶» (active),
ages 10–14, premier 12 teams per age + League 1 groups (U10: A,B · U11: A,B,C ·
U12: A,B,C,D · U13: A,B,C · U14: A,B — 10 teams each), double round-robin,
~55% of weeks finished, engineered U12-premier tie-break edge cases.

## 3. Everyday commands

| Task | Command |
|---|---|
| Run (foreground) | `ADMIN_PASSWORD='…' tools/serve.sh` |
| Run (background) | `tools/serve.sh --background` (log: `data/server.log`) |
| Fresh demo DB | `SEED=1 tools/serve.sh` (only when DB file absent) or `go run ./cmd/seed --db data/x.db --force` |
| Go build / vet / tests | `go build ./... && go vet ./... && go test ./internal/... -count=1` |
| Client typecheck / build / test | `bun run check` (`tsc --noEmit`) · `bun run build` (`app.ts → app.js`) · `bun test` |
| Live HTTP gate | `tools/smoke.sh [path/to.db]` / `SEED=1 tools/smoke.sh` |
| Inspect DB | `sqlite3 data/pabetoop-league.db ".tables"` / `.schema matches` |
| Backup (CLI fallback) | `sqlite3 data/pabetoop-league.db ".backup data/backup-$(date +%Y%m%d-%H%M%S).db"` |

Env vars: `ADDR` (`:8080`, flag `-addr` wins), `DB_PATH`
(`data/pabetoop-league.db`, flag `-db` wins), `ADMIN_PASSWORD` /
`ADMIN_PASSWORD_HASH` (hash wins), `ADMIN_USER` (`admin`), `SESSION_SECRET` /
`SESSION_SECRET_FILE` (`data/secret.key`, 0600), `TEMPLATES_DIR`,
`STATIC_DIR`. Never commit `data/` (git-ignored).

## 4. Conventions (enforced in review)

- **Persian UI, English code.** User-facing text (incl. all validation errors)
  is Persian with Persian digits via `toFa`; code comments and identifiers are
  English. Templates contain no inline styles/scripts and use only classes from
  `web/static/css/main.css`.
- **htmx 4 rules:** explicit inheritance (`hx-headers:inherited` for CSRF),
  `htmx:after:request`-style event names, 422 swappable validation partials,
  `<hx-partial>` for multi-target updates, `defaultTimeout` config. Banned
  attrs: `hx-ext hx-vars hx-inherit hx-disinherit hx-params`.
- **Dates:** ISO Gregorian TEXT in DB; every render goes through
  `internal/jalali` (funcs `toFa jalaliDate jalaliLong`). Never import
  `go-persian-calendar` outside that package. Times are `HH:MM` text.
- **Boundaries:** SQL only in `internal/store`; standings computed in
  `internal/standing`, never stored; TS never duplicates domain rules.
  `internal/store/api.go` + applied migrations are **frozen** — schema changes
  mean a new numbered migration, never an edit in place. In `.gitignore`, `bin/`
  is root-anchored (`/bin/`, D22): a bare pattern matches at any depth and would
  silently untrack nested `bin/` directories.
- **Migrations:** never edit an applied file; add `NNNN_name.sql`, applied in
  filename order, tracked in `schema_migrations`. Deletions that would break
  history are blocked (deactivate instead); seasons are never deleted.

## 5. Working on a change

- **One concern per change.** A schema change and a UI redesign are two changes.
- **Check before you build.** The standard library and the dependencies already in
  `go.mod` solve most problems here; adding a dependency needs a reason in the pull
  request, not a preference (D12).
- **Define done before you start.** Write the failing test first when the change is a
  bug fix; the acceptance gate for handlers and templates is `tools/smoke.sh`.
- **Update the docs the change invalidates** — `ROUTES.md` for a new route,
  `DATABASE.md` for a new rule, `TESTING.md` §5 when an integrity rule changes.
- **Prove a rule, don't just count tests.** A change to an integrity rule updates the
  `TESTING.md` §5 matrix *and* adds a mutation proof (`tools/mutation-proof.sh`): the
  rule is only "enforced and tested" if breaking the enforcement makes a test fail (D23).

## 6. Definition of done (per change)

`go build ./...` + `go vet ./...` clean · affected `go test` green ·
`tools/smoke.sh` passes when handlers/templates moved · Persian error paths
covered · audit row written for new mutations · docs touched (`ROUTES.md` for
new routes, `DATABASE.md` for new rules, this file for new commands). A change
to a §7 integrity rule also updates the `TESTING.md` §5 matrix with a **mutation
proof**, not just a green test count (`DECISIONS.md` D23).

## 7. Dependencies and build environment

**Nothing is vendored.** Dependencies resolve from the module proxy and are pinned by
`go.sum` (decision D16). A fresh clone therefore needs network access once:

```bash
go mod download     # warm the module cache
go build ./...
```

An earlier iteration committed both a Go toolchain tarball and a `vendor/` tree so a
clone could build with no network at all. That was removed: it added ~200 MB to every
clone, and — more importantly — a vendored tree only receives a dependency's security
fix when someone remembers to re-run `go mod vendor`.

### Pitfalls worth knowing

- **A globally-exported `GOFLAGS=-mod=vendor`** (written into `~/.config/go/env` by some
  unrelated project's tooling) makes a non-vendored module fail with the misleading
  error `inconsistent vendoring in <dir>` even though no `vendor/` exists. Override per
  command — `GOFLAGS=-mod=mod go build ./...` — rather than editing the shared config.
- **Never extract a Go toolchain inside the module.** Its `test/` corpus joins the
  module and `go build ./...` fails with hundreds of bogus package errors. Extract
  outside the module.
- **Do not route registry traffic through an outbound proxy** if you are using a
  regional mirror: the mirror answers based on the caller's address, and a foreign exit
  IP will either break it or serve you something stale (D5).

### Adding or updating a dependency

```bash
go get example.com/pkg@v1.2.3
go mod tidy
go test ./...
```

Then explain in the pull request why the standard library was not sufficient.

## 8. The SQLite driver

`modernc.org/sqlite` is a **pure-Go** translation of SQLite: no cgo, no gcc, and a
single static binary that cross-compiles without a toolchain dance. That is why the
`modernc.org/libc` dependency exists, and why the module graph is larger than it looks
(D1 records the trade-off).

Two consequences worth remembering:

- **Do not add a cgo dependency casually.** It gives up the property that makes the
  deployment story simple — `CGO_ENABLED=0` on any platform, no build server.
- **`internal/store` is the only package that opens a database.** The driver choice is
  therefore a one-file decision, not a refactor.

## 9. JavaScript (not vendored)

The JS surface is light (`app.ts` → `app.js`, tests) and `node_modules/` is
git-ignored. htmx 4 is vendored as a static file
(`web/static/js/htmx-4.0.0.min.js`, tracked), so **production needs no JS
toolchain at all**. To work on the client, install Bun and let it use the
registry directly — never through a proxy (D5):

```bash
bun install          # ~2 min on first run, one-time thanks to the lockfile
bun test             # client-side tests
```
