# Deploy — youth football platform

A single Go binary plus one SQLite file. No database server, no Node runtime, no external
services, no paid SaaS. The binary embeds the migrations; the templates and static assets ship
in `web/`.

## 1. Run it (dev / demo, 30 seconds)

```bash
cd projects/pabetoop-league

# start empty — migrations alone give a usable install
ADMIN_PASSWORD='choose-a-password' tools/serve.sh --background

# or start with realistic DEMO data (73 clubs, 19 competitions, 1920 matches)
ADMIN_PASSWORD='choose-a-password' SEED=1 tools/serve.sh --background

# afterwards
ADMIN_PASSWORD='choose-a-password' tools/serve.sh            # foreground (systemd-friendly)
```

`SEED=1` is for demo data, **not** to make the app work. On a brand-new database the migrations
create the five age categories (10–14), so a first run can sign in and go
season → activate → competition with no seed and no SQL client. Live proof, both directions:
`bash tmp/verify-fresh.sh` (a fresh file gets 5 categories; a seeded file still boots, unchanged).

Then open <http://127.0.0.1:8080> for the public site and <http://127.0.0.1:8080/admin> for the
admin panel (user `admin`, the password you set).

| Variable | Default | Meaning |
|---|---|---|
| `ADDR` | `:8080` | listen address |
| `DB_PATH` | `data/pabetoop-league.db` | SQLite file (WAL mode) |
| `ADMIN_PASSWORD` | — | bootstrap the admin account on first run (bcrypt-hashed at startup) |
| `ADMIN_PASSWORD_HASH` | — | pre-hashed bcrypt variant, wins over `ADMIN_PASSWORD` |
| `ADMIN_USER` | `admin` | admin username |
| `SESSION_SECRET` | auto | HMAC session secret; if unset, `data/secret.key` is created `0600` |
| `TEMPLATES_DIR` / `STATIC_DIR` | `web/templates` / `web/static` | asset locations |

Always export `NO_PROXY=127.0.0.1,localhost` on a box with a global proxy — otherwise localhost
requests get hijacked (this workspace has one).

## 2. Verify the deployment is healthy

```bash
tools/smoke.sh data/pabetoop-league.db     # live HTTP gate: real data on the pages, admin mounted
curl -s localhost:8080/healthz           # → ok
```

`tools/smoke.sh` is the acceptance gate: it fails if the public pages render construction
placeholders, if the admin surface is unmounted (404), or if unknown ids stop returning a Persian
404. Run it after every deploy.

## 3. Production (economical Iranian VPS)

1. `go build -o /srv/pabetoop-league/pabetoop-league ./cmd/server` on the build box; copy the binary,
   `web/`, and the data directory across.
2. Point a TLS-terminating reverse proxy (Caddy or Nginx) at `127.0.0.1:8080`. The app sets HSTS
   only when it sees `r.TLS`, so terminate TLS at the proxy and forward `X-Forwarded-Proto`.
3. Set `ADMIN_PASSWORD_HASH` (not the plaintext) and `SESSION_SECRET` in the unit's environment —
   no secrets on disk.
4. systemd unit sketch:

```ini
[Unit]
Description=youth football platform
After=network.target

[Service]
User=pabetoop
WorkingDirectory=/srv/pabetoop-league
Environment=ADDR=127.0.0.1:8080
Environment=DB_PATH=/var/lib/pabetoop-league/pabetoop-league.db
ExecStart=/srv/pabetoop-league/pabetoop-league
Restart=always
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

5. **Backups**: the admin panel's «دانلود پشتیبان» button produces a consistent VACUUM INTO snapshot
   of the whole database. It is also fine to copy the file while the server runs (WAL mode), but
   take `sqlite3 <db> ".backup <dest>"` when you want a guaranteed-consistent copy. Snapshots are
   never pruned automatically (`data/backups/`); retention is the operator's call.

## 4. What is deliberately not built

Per the product spec this is *not* a club ERP, player manager, CMS, live-score or analytics
product. The current cut ships the full public site (age-first navigation, league tables, results,
fixtures) and the admin surface for clubs, teams, fixtures, quick result entry, backup and audit.

Season creation is **no longer a gap**: `TASK-015` (competitions + registrations) and `TASK-016`
(seasons) shipped, and migration `0003` seeds the five age categories, so an operator can now run
the whole flow from an empty database — season → activate → competition → registrations → fixtures
→ results → tables — without a SQL client or the seed data.

Closed since that note: `Permissions-Policy` is sent on every response as an explicit deny-all
(`TASK-022`), `/admin/teams` is paginated (`TASK-023`), any coordination lock is advisory and
always expirable (`DECISIONS.md` D21), and the whole public+admin surface carries the
documented header set (`SECURITY.md`, asserted by `TestSecurityHeadersOnEveryResponse`).

Closed since that note (the contract unfreeze, `DECISIONS.md` D24): `/admin/teams` is paged **in the
store** and `TeamsPage` is on the `DataStore` interface — the local capability assertion is gone;
and a team's `age_group_id` (`TASK-029`, migration `0004`) is now both persisted **and displayed**,
as a «ردهٔ سنی» column in the teams list («—» for an uncategorised team, which is still returned).

Remaining known gaps at this cut: paging bounds rows *materialised*, not the scan — the `ORDER BY`
still builds a temp B-tree, so a covering index is the upgrade path at scale; `UpdateTeam` has no
category parameter, so a category can be set at creation but not changed afterwards; and the client
bundle `app.ts` has no tests, so a green client gate means nothing yet — the Go suite is the gate
that matters.

## 5. Where things live

| Path | Purpose |
|---|---|
| `PROJECT_SPEC.md` | product source of truth (§11 maps the older brief section numbers) |
| `DECISIONS.md` | every architecture decision and its rationale |
| `DATABASE.md` | schema and the integrity rules that are enforced where |
| `seed/` | placeholder club list and the demo-season generator |
| `tools/serve.sh` · `tools/smoke.sh` | run it · prove it works |
