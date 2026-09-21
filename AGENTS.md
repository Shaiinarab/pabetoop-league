# AGENTS.md — working contract for this repository

Loaded automatically by agent tooling that supports `AGENTS.md`. Read it before making
changes.

## What this repository is

`pabetoop-league` is a **white-label template**. It is a youth-competition platform —
fixtures, results, standings, administration — written as one Go binary serving
server-rendered Persian RTL pages over a single SQLite file.

Three properties define the project, and all three are enforced by tests:

1. **Tagless.** No league, city, competition or club name is compiled in. Every
   league-facing string resolves through `internal/site`.
2. **Ranking logic lives in one place.** `internal/standing` is pure and property-tested;
   no handler may compute a table.
3. **One SQL surface.** `internal/store` is the only package that touches a database.

## Commands

```bash
go build ./...                 # compile everything
go test ./...                  # 221 tests, ~20s
go vet ./...
gofmt -l . | grep -v vendor    # must print nothing

# demo season (73 placeholder clubs, 1,920 fixtures), then run it
go run ./cmd/seed --db data/league.db --force
ADMIN_PASSWORD='choose-a-password' tools/serve.sh --background
tools/smoke.sh data/league.db          # live HTTP acceptance gate
tools/mutation-proof.sh                # proves the tests fail when rules are broken
```

Environment note: this module uses `-mod=mod` and needs network access for the first
`go mod download`. If a host has `GOFLAGS=-mod=vendor` exported globally (in
`~/.config/go/env`), a build fails with the misleading `inconsistent vendoring in <dir>`
even though no `vendor/` exists — override per command with `GOFLAGS=-mod=mod` rather
than editing the shared host config.

## Hard rules

- **Never hard-code a league name.** Not in Go, a template, a fixture or a doc. It
  belongs in `internal/site`. `internal/web/branding_test.go` renders the real routes
  and fails on any legacy name.
- **Never commit a real club list.** `seed/clubs.json` is placeholder-only. Club names
  are records, not branding.
- **Ranking changes go in `internal/standing`, with tests.** If you find yourself
  ordering rows in a handler, stop.
- **Never normalise a club name.** No trimming, no merging, no fuzzy auto-rename (D7).
  Two names that differ by a ZWNJ or an Arabic-vs-Persian letter are two clubs. Fuzzy
  matching may *suggest* during import and never decides.
- **Never edit an applied migration.** Add `NNNN_name.sql` in `internal/store/migrations/`,
  applied in filename order.
- **Authorise, then write, then audit.** An admin mutation checks the session and CSRF,
  applies the rule, saves, and writes an audit row. A privileged route without an audit
  call is a review failure.
- **Public pages carry no operational records**, only aggregate counts and the
  competition data that is meant to be public.
- **Delete dead code rather than commenting it out.** A route that is defined but never
  mounted has already caused one silent outage here (`ROUTES.md` records it).

## Editing the UI

1. Templates live in `web/templates/`. Each page is parsed as its own clone of its base
   template, so a `{{define "title"}}` block belongs in the page file, not the base.
2. Persian copy lives in the templates and in `internal/web`. If a string identifies the
   league, it belongs in `internal/site` instead.
3. Keep RTL intact (`dir="rtl"`, `lang="fa"`) and Persian digits for display; the
   database stores Latin digits and UTC/ISO dates.
4. Run `go test ./internal/web` afterwards — it parses every template and asserts the
   pages render without legacy identity.

## Docs to keep in sync

| Change | Update |
|---|---|
| New route | `ROUTES.md` (there is a test that checks the doc against the routing tree) |
| New integrity rule | `DATABASE.md` + the `TESTING.md` §5 matrix + a mutation proof |
| New env var | `.env.example` + the table in `README.md` |
| New identity surface | `docs/BRANDING.md` §3 + `legacyIdentity`/routes in `branding_test.go` |
| New security-relevant behaviour | `SECURITY.md` |

## Review gate

Before calling any change complete:

- [ ] `go build ./...`, `go test ./...`, `go vet ./...` all pass
- [ ] `gofmt -l` is empty
- [ ] `tools/smoke.sh` passes if handlers or templates moved
- [ ] `go test ./internal/web -run Legacy` passes
- [ ] No new hard-coded league or club name
- [ ] Docs above updated
