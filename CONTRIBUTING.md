# Contributing

This is a small, deliberately conservative codebase. The fastest way to get a change
merged is to keep it small and prove it works.

## Setup

```bash
git clone https://github.com/shaiinarab/pabetoop-league
cd pabetoop-league
cp .env.example .env

go mod download
go test ./...                      # 221 tests, ~20s, no database server needed

go run ./cmd/seed --db data/league.db --force     # demo season
ADMIN_PASSWORD='choose-a-password' tools/serve.sh --background
# public → http://127.0.0.1:8080   admin → http://127.0.0.1:8080/admin/login
```

Bun and Node are needed only if you change the TypeScript client. Production requires
neither.

## Before opening a pull request

```bash
go build ./...
go test ./...
go vet ./...
gofmt -l . | grep -v vendor        # must print nothing
go test ./internal/web -run Legacy # branding leak gate
tools/smoke.sh data/league.db      # live HTTP gate, if handlers or templates moved
```

If your change touches an integrity rule, also run `tools/mutation-proof.sh` and add the
rule to the `TESTING.md` §5 matrix. A rule is only "enforced and tested" when breaking
the enforcement makes a test fail (decision D23) — a green test count proves nothing on
its own.

## What a good change looks like

**Small.** One behaviour per pull request.

**Ranked in the right place.** If a change affects ordering, points or tie-breaks, it
belongs in `internal/standing` with a test — never in a handler. The engine is pure and
property-tested precisely so this stays true.

**Honest about names.** Club names are records: never trimmed, normalised, merged or
auto-renamed (D7). Fuzzy matching may suggest during import and never decides.

**Enforced, not documented.** If a rule matters, there should be a test that fails when
the rule is broken. `TESTING.md` lists the mutation proofs that exist for exactly this.

**Tagless.** If your change introduces a league, city or competition name into code, a
template or a test fixture, move it into `internal/site` and add the surface to
`internal/web/branding_test.go`.

**No new heavy dependency** without a reason in the pull request. The point of this
stack is that it stays small: the standard library, `html/template`, htmx, one calendar
library and a pure-Go SQLite driver.

## Commit messages

Conventional-commit style, imperative mood, explaining *why*:

```
fix(standings): apply goals-for before the name tie-break

Two teams level on points and goal difference were ordered by name, which
contradicts the documented rule and made one table in the U12 Premier
disagree with the published result sheet.
```

## Reporting bugs and security issues

Bugs: open an issue with the route, the exact command you ran, what you expected and
what happened. A failing `tools/smoke.sh` output is the ideal report.

Security: **do not open a public issue.** Use a private security advisory. See
[`SECURITY.md`](SECURITY.md).

## Scope

In scope: fixtures, results, standings, competitions, seasons, clubs and teams, the
import pipeline, the admin surface, deployment ergonomics.

Out of scope: multi-tenant hosting, a second ranking engine, an ORM, player statistics,
payments, and anything that adds a runtime service to the deployment. Those are
deliberate non-goals (see `DECISIONS.md`).

## License

By contributing you agree your contribution is licensed under the MIT License
(see [`LICENSE`](LICENSE)).
