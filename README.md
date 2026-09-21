# Pabetoop League

**A competition platform for youth leagues.** Fixtures, results, league tables and
competition administration — one Go binary, one SQLite file, a Persian RTL interface,
and no league, city or club name anywhere in the source.

[![CI](https://github.com/shaiinarab/pabetoop-league/actions/workflows/ci.yml/badge.svg)](https://github.com/shaiinarab/pabetoop-league/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)
![License](https://img.shields.io/badge/license-MIT-blue)

---

## What problem this solves

A youth league is run by volunteers and a spreadsheet. Fixture lists get emailed as
attachments, results arrive by phone, and the standings are recalculated by hand every
week — which is exactly where the arithmetic errors live, and everyone notices when a
table is wrong.

Pabetoop League is the smallest system that fixes that:

- **Anyone** can open the public site and see the current standings, the results already
  played, and the fixtures still to come — per age category, per competition.
- **The administrator** enters a result in one form, imports a whole week of fixtures
  from a spreadsheet, manages clubs and teams, and sees an audit trail of every change.
- **The deployment owner** gets a single static binary with an embedded database. No
  database server, no Node runtime in production, no paid SaaS.

It is deliberately scoped to *competitions*. It is not a club-management or a payments
system — for that, see [Pabetoop Club](https://github.com/Shaiinarab/pabetoop-club).

## Features

| Area | Status | Notes |
|---|---|---|
| Public site | ✅ | Home with latest results, age-group pages, competition pages |
| Standings engine | ✅ | Deterministic; documented tie-break order; pure and unit-tested |
| Fixtures | ✅ | Double round-robin generator, balanced home/away |
| Quick result entry | ✅ | One screen, keyboard-first, htmx-enhanced |
| CSV import | ✅ | Parse → validate → suggest → confirm, never silently guessing |
| Seasons | ✅ | Create and activate; every other record hangs off a season |
| Age categories | ✅ | Seeded by migration (10–14 by default); configurable |
| Clubs & teams | ✅ | Create, rename, page; official names are never rewritten |
| Audit log | ✅ | Every privileged change, with actor, before/after and entity id |
| Backup | ✅ | One-click download plus an integrity/status surface |
| Jalali calendar | ✅ | Persian calendar at the display boundary; UTC in storage |
| Security | ✅ | Sessions, CSRF, per-IP login limiting, HSTS on TLS, CSP |
| White-label | ✅ | Five environment variables re-brand the entire system |
| Persian RTL UI | ✅ | Server-rendered; no SPA to keep in sync |

## Architecture

One process, one file. The server renders HTML with `html/template`; htmx 4 handles
partial swaps (standings refresh, result entry, club paging) as an enhancement — every
page works with JavaScript disabled.

```text
        Public visitor / Administrator browser
                        │  HTTPS · HttpOnly session · CSRF token
                        ▼
        ┌──────────────────────────────────────────────┐
        │  Go single binary (cmd/server)               │
        │                                              │
        │  internal/web      routes, sessions, views   │
        │  internal/site     white-label identity      │
        │  internal/standing standings + tie-breaks    │  ← pure, no I/O
        │  internal/import   CSV parse + validate      │  ← pure, no I/O
        │  internal/jalali   Persian calendar          │  ← pure, no I/O
        │  internal/store    the only SQL, one contract │
        └────────────────┬─────────────────────────────┘
                         ▼
                  data/league.db   (SQLite, WAL mode)
```

Four rules keep it maintainable, and each is enforced rather than hoped for:

1. **Ranking logic lives in exactly one place.** `internal/standing` is pure and
   property-tested; no handler may compute a table.
2. **The store is the only SQL.** Everything else talks to a `DataStore` interface, so a
   second backend is a new package rather than a refactor.
3. **Names are data, never code.** Club names come from the administrator or the seed
   file; the app never normalises, merges or renames them (decision D7).
4. **The template is tagless.** Every league-facing string resolves through
   `internal/site`, and a test fails the build if a name from the source deployment
   reappears in any rendered page.

## Quickstart

### Docker (recommended)

```bash
cp .env.example .env      # then edit the LEAGUE_* block
SEED=1 docker compose up -d --build     # SEED creates a demo season on first boot
# public   → http://127.0.0.1:8080
# admin    → http://127.0.0.1:8080/admin/login   (user `admin`)
# health   → http://127.0.0.1:8080/healthz
```

### From source

Requires Go 1.26+ (Node 22+ only if you touch the TypeScript client).

```bash
cp .env.example .env        # same file the Docker path reads — edit the LEAGUE_* block
go mod download

# optional: a demo season with placeholder clubs and fixtures
go run ./cmd/seed --db data/league.db --force

ADMIN_PASSWORD='choose-a-password' tools/serve.sh --background
# public → http://127.0.0.1:8080   admin → http://127.0.0.1:8080/admin/login
```

`tools/serve.sh` loads `.env` itself via `tools/load-env.sh`, so the branding you set there is what
the process serves. An explicitly exported variable still wins over `.env`
(`ADDR=:9000 tools/serve.sh`). Do not shortcut the loader with `. ./.env` — a value containing a
space (`LEAGUE_NAME=Riverside Youth League`) makes the shell try to run `Youth`, and the variable
silently becomes `Riverside`.

Always export `NO_PROXY=127.0.0.1,localhost` on a host with a global proxy, or localhost
requests get captured by it.

### Verify it works

```bash
go test ./...                                        # 221 tests, ~20s
tools/smoke.sh data/league.db                        # live HTTP acceptance gate
tools/mutation-proof.sh                              # proves tests fail when rules are broken
```

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `LEAGUE_NAME` | `Pabetoop League` | Latin name (docs, backup filenames, logs) |
| `LEAGUE_NAME_FA` | `لیگ فوتبال نوجوانان` | Persian display name in the UI |
| `LEAGUE_DISCIPLINE_FA` | `فوتبال` | Sport noun; derives the footer strapline |
| `LEAGUE_SUBTITLE_FA` | *(derived)* | Explicit strapline override |
| `ADDR` | `:8080` | Listen address (flag `-addr` wins) |
| `DB_PATH` | `data/pabetoop-league.db` | SQLite file (flag `-db` wins) |
| `ADMIN_USER` | `admin` | Bootstrap administrator username |
| `ADMIN_PASSWORD` | *(unset)* | Bootstrap password; hashed at startup |
| `ADMIN_PASSWORD_HASH` | *(unset)* | bcrypt hash; wins over the plaintext value |
| `SESSION_SECRET` | *(generated)* | Explicit session secret, else `data/secret.key` |
| `SEED` | `0` | Container only: `1` seeds demo data if the DB is missing |

Full annotated list: [`.env.example`](.env.example).

## Re-branding a fork

The platform ships **tagless**: no league, city or club name is compiled in.

```bash
./tools/instantiate.sh \
  --name      "Riverside Youth League" \
  --name-fa   "لیگ نوجوانان رودخانه" \
  --discipline-fa "فوتسال" \
  --module-path github.com/yourorg/riverside-league
```

The script rewrites the Go module path, the binary and volume names, and the `LEAGUE_*`
block in `.env`, then runs the build and the leak test as a post-condition. Full contract:
[`docs/BRANDING.md`](docs/BRANDING.md).

## Seeding a demo season

`seed/clubs.json` ships with 73 obvious placeholders (`باشگاه نمونه ۱` … `۷۳`) so nobody
mistakes a demo database for a real competition. Point the generator at your own list:

```bash
go run ./cmd/seed --clubs-file ./my-clubs.json --db data/league.db --force
```

The generator is deterministic: the same club list always produces the same fixtures and
the same results, including engineered tie-break edge cases. See
[`seed/README.md`](seed/README.md).

## Security posture

- Session cookies are `HttpOnly` and `SameSite`; every mutation requires a CSRF token.
- Login attempts are rate-limited per client IP, and the limiter is bounded in memory.
- `X-Frame-Options: DENY`, a restrictive CSP, `nosniff`, and HSTS when served over TLS.
- A single administrator account by design (decision D11); there is no self-registration.
- No secret in the repository; `.env` is git-ignored and the session key is generated
  0600 on first run.

Threat model and residual risks: [`SECURITY.md`](SECURITY.md).

## Documentation map

| Document | Contents |
|---|---|
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Layers, request flow, package contracts, extension points |
| [`PROJECT_SPEC.md`](PROJECT_SPEC.md) | Product source of truth: rules, entities, acceptance criteria |
| [`DATABASE.md`](DATABASE.md) | Schema, integrity rules, where each rule is enforced, backups |
| [`ROUTES.md`](ROUTES.md) | Complete HTTP route table (public + admin) |
| [`DECISIONS.md`](DECISIONS.md) | Every architecture decision and its rationale |
| [`TESTING.md`](TESTING.md) | Suites, gates, integrity-rule traceability, mutation proofs |
| [`DEVELOPMENT.md`](DEVELOPMENT.md) | Toolchain, commands, conventions |
| [`DEPLOY.md`](DEPLOY.md) | Production: systemd, reverse proxy, backups, restore |
| [`SECURITY.md`](SECURITY.md) | Threat model, controls, residual risks |
| [`ADMIN_GUIDE.md`](ADMIN_GUIDE.md) | راهنمای فارسی مدیر مسابقات |
| [`docs/BRANDING.md`](docs/BRANDING.md) | The white-label contract |
| [`seed/README.md`](seed/README.md) | Seed data format and what the generator builds |

## خلاصهٔ فارسی

پابه‌توپ لیگ یک سامانهٔ **بدون‌نام تجاری (white-label)** برای برگزاری مسابقات نوجوانان
است: برنامهٔ دیدارها، ثبت نتایج، جدول رده‌بندی، مدیریت باشگاه‌ها و تیم‌ها، سابقهٔ تغییرات
و پشتیبان‌گیری. کل سامانه یک باینری Go با پایگاه‌دادهٔ SQLite است و رابط کاربری فارسی و
راست‌به‌چپ دارد.

نام لیگ، رشتهٔ ورزشی و شهر **هیچ‌کدام در کد نوشته نشده‌اند**؛ با متغیرهای محیطی
`LEAGUE_*` می‌توانید کل سامانه را برای هر مسابقه‌ای سفارشی کنید (راهنما:
`docs/BRANDING.md`). فهرست باشگاه‌های دادهٔ نمایشی هم کاملاً ساختگی است و با
`--clubs-file` جایگزین می‌شود.

## License

MIT — see [`LICENSE`](LICENSE).
