# ARCHITECTURE — Pabetoop League

> Technical mapping companion to `PROJECT_SPEC.md` (product truth) and
> `DECISIONS.md` (why). Verified 2026-09-12 against the tree: `cmd/`,
> `internal/{store,web,standing,jalali,import}`, `web/templates`,
> `web/static`, `tools/`, `seed/`. Graph orientation via codebase-memory MCP
> (project `home-shai-personal-projects-projects-pabetoop-league`: 1434 nodes,
> 5023 edges; dominant boundary `web → store`, 223 calls).

## 1. Layer diagram

```text
                    ┌─────────────────────────────────┐
                    │  Browser (RTL Persian, mobile-   │
                    │  first public / desktop admin)  │
                    │  htmx 4.0.0 + app.js (TS 7)     │
                    └───────────────┬─────────────────┘
                                    │ HTTP (Persian HTML partials + forms)
                    ┌───────────────▼─────────────────┐
                    │  internal/web (stdlib ServeMux)  │
                    │  server.go = route tree + render │
                    │  public_handlers · admin_handlers│
                    │  result/fixture/import/backup/  │
                    │  audit/competition handlers     │
                    │  middleware · security · funcmap │
                    └───────────────┬─────────────────┘
                                    │ Go calls (DataStore interface)
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
   internal/store            internal/standing      internal/jalali
   (SQLite via               (pure compute:          (single date
    modernc.org/sqlite,       FINISHED → table)       conversion point)
    triggers + CHECKs)
              │                     │                     │
              ▼                     ▼                     ▼
   data/pabetoop-league.db   internal/import          go-persian-calendar
   (WAL, single file)      (CSV → preview→confirm)
```

No ORM, no framework, no background jobs, no cache layer. SQL lives only in
`internal/store`. Templates never do date math or business logic. TypeScript
never duplicates domain rules (keyboard flow + UI niceties only).

## 2. Request flow

**Public (anonymous, read-only):** `GET /` → `handlePublicHome` (latest
results, upcoming, stats) · `GET /age/{id}` → age page (premier + League 1
group cards) · `GET /competition/{id}` or `/competition/{id}/{tab}`
(`{tab}` ∈ table/results/fixtures; `?tab=` accepted as legacy alias, D14) →
standings (computed on demand) + results + fixtures. Unknown ids → Persian 404.

**Admin (session + CSRF):** `GET /admin/login` → form ·
`POST /admin/login` (bcrypt, rate-limited) → signed session cookie ·
every other `/admin/*` via `requireAdmin` (browser → 303 to login; htmx →
403 + `HX-Redirect`). Mutations are POST with `_csrf` hidden field +
`X-CSRF-Token` header via `hx-headers:inherited` on `<body>` (htmx 4 explicit
inheritance, D2). Validation failures render as swappable **422 partials**
(`hx-status:422="target:#form-errors"`); `<hx-partial>` multi-target responses
update row + standings + flash at once (quick result entry).

**Middleware chain** (outermost first): `recoverer` → `securityHeaders` →
`requestLogger` (JSON, never bodies/cookies/queries) → `ensureCSRFCookie` →
`csrfProtect` → mux. Static `/static/` served with `.css/.js → max-age=300`,
everything else immutable year-long.

## 3. Module contracts

| Module | Owns | Consumed via | Must not |
|---|---|---|---|
| `internal/store` (`api.go` contract) | All persistence, integrity enforcement, audit rows, `BackupTo` | `DataStore` interface (seasons, age groups, clubs, teams, competitions, registrations, matches, audit, backup) | Be bypassed — no direct SQL anywhere else (seed goes through the API too) |
| `internal/standing` | Deterministic table: Pts 3/1/0, order Pts→GD→GF→Persian-alpha | `Compute(finished []FinishedMatch)` / `store.FinishedMatches` | Persist anything; invent an away-goals rule (hook reserved, PROJECT_SPEC A1) |
| `internal/jalali` | Sole Jalali⇄Gregorian conversion + Persian-digit rendering | `Format` / `Parse` / `ToPersianDigits`; template funcs `jalaliDate`, `jalaliLong`, `toFa` | Be bypassed — templates do no date math (D9) |
| `internal/import` | CSV import pipeline: parse → normalize → validate → preview → confirm | Preview rows + confirm applier (admin handlers) | Silently mutate: ambiguities are suggestions only (D7) |
| `internal/web` | Routing, rendering, sessions, CSRF, login limiter | `Server.Handler()` assembles `Register*Routes` once each (double-register panics) | Contain business rules — all validation delegates to the store |
| `cmd/seed` | Realistic demo season (seed 1405, deterministic) | `go run ./cmd/seed --db … --force` / `SEED=1 tools/serve.sh` | Ship in the product UI (D8: generator is dev-only) |

## 4. Data flow invariants

1. **Writes:** admin POST → handler parses → `store` validates (Persian
   errors) → DB constraints backstop (triggers/CHECKs/UNIQUE) → audit row in
   the same transaction → 422 partial or `<hx-partial>` success.
2. **Reads:** handlers fetch via `store` → standings computed from
   `FinishedMatches` → templates render with `funcmap` (Jalali + Persian
   digits). Nothing cached; standings are never stored (§7 rule 9).
3. **Dates:** ISO Gregorian TEXT in DB → `internal/jalali` → Persian strings in
   UI. Times are opaque `HH:MM` text (single +03:30 offset, no DST since 2022).
4. **Identity:** `clubs.name` UNIQUE (different spellings = different clubs);
   `registrations.club_id` denormalized at registration time so history never
   mutates retroactively; teams display via official `display_name`.
5. **Lifecycle:** `cmd/server/main.go`: flags/env → `store.Open` → `Migrate`
   (embedded `migrations/`, `schema_migrations` ledger) → `web.New` (fail fast
   on template error) → `http.Server` with read/write/idle timeouts →
   graceful 10s drain on SIGINT/SIGTERM.

## 5. What is deliberately absent (and why)

Single binary + SQLite (zero-ops backup = copy file / `VACUUM INTO`); no
Postgres server (container compose kept as documented growth path, D1); no
RBAC/MFA (single admin, D11); no search, logos, live scores, players, news
(out of MVP scope); no in-memory rate-limiter sharing, no proxy-header trust
(documented tradeoffs in `internal/web/README.md`).
