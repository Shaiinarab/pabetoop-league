# Adopting this template

From "Use this template" to a running competition site for *your* league.
Every command below is the verified path for this repo — copy-paste order, not a sketch.

- **§1** re-brand it (one command, ~30 s)
- **§2** run it locally and see it serve your name
- **§3** put your own clubs and season in
- **§4** deploy it
- **§5** what not to change
- **§6** the traps that actually bite

---

## 0. What "tagless" means here

No league, city, club or operator name is compiled in. There are two layers:

| Layer | What it is | Where it lives |
| --- | --- | --- |
| **Structural** | Go module path, binary name, image/volume names, database filename | rewritten by `tools/instantiate.sh` |
| **Cosmetic** | The name page titles, headers and the login page render | `.env` → read by `internal/site` at startup |

Both templates in this family ship a test that **fails the build** if a client identifier ever
reappears in the tree (`internal/web/branding_test.go`). Leave it in place — it is the thing that
keeps a fork from quietly re-acquiring someone else's name.

---

## 1. Re-brand it

Create your repo from the template (the green **Use this template** button), clone it, then:

```bash
./tools/instantiate.sh \
  --name "Riverside Youth League" \
  --name-fa "لیگ نوجوانان رودساید" \
  --discipline-fa "فوتسال" \
  --module-path github.com/riverside/riverside-league \
  --binary riverside-league
```

Everything except the two Persian display strings is optional; `--module-path` defaults to the
current one and `--binary` defaults to the last path segment of the module.

The script refuses to run on a dirty tree (so a bad run is `git checkout .`), then **verifies its own
work**: it builds the tree and runs the branding leak test. If either fails it exits non-zero and
tells you to inspect the diff.

`--dry-run` prints the plan and writes nothing.

Writes **gitignored**`.env` from `.env.example`, carrying your names. `.env` is never committed.

---

## 2. Run it locally (from source)

```bash
cp .env.example .env        # already done by instantiate.sh, if you used it
go mod download

# optional: a demo season, so the pages have something to render
go run ./cmd/seed --db data/league.db --force

ADMIN_PASSWORD='choose-a-password' tools/serve.sh --background
# public → http://127.0.0.1:8080     admin → http://127.0.0.1:8080/admin/login
```

`tools/serve.sh` builds the binary, creates `data/`, and **loads `.env` itself**. An explicitly
exported variable still wins over `.env`, so `ADDR=:9000 tools/serve.sh` does what you expect.

Confirm it serves *your* brand before going further:

```bash
curl -s http://127.0.0.1:8080/ | grep -o '<title>[^<]*</title>'
```

If that prints the template's default name rather than yours, `.env` is not being read — see §6.

### The verification gate

```bash
go test ./...            # unit + integration
SEED=1 tools/smoke.sh    # boots the real binary and asserts the pages serve DATA, not placeholders
```

`tools/smoke.sh` is the gate that matters. It fails loudly if the public pages render construction
placeholders, if the admin surface is unmounted (404), or if data never reaches the templates. A
green `go build` + `go vet` proves none of that — see §6.

---

## 3. Put your own data in

### Clubs

Club names come from `seed/clubs.json`, and only from there — the seeder never invents them. Replace
the placeholder list with your own (see `seed/README.md` for the shape). Run with
`--clubs-file` to point at a different list:

```bash
go run ./cmd/seed --db data/league.db --force --clubs-file seed/my-clubs.json
```

### The generated season

`cmd/seed` builds one complete, deterministic demo season — age groups 10–14, a premier competition
per age group plus lower divisions, double round-robin fixtures, balanced home/away, deterministic
results, and engineered tie-break edge cases in one age group so you can see the standings rules
work. It is **demo data**: it exists so you can look at a populated UI.

The product itself never auto-generates fixtures (this is a deliberate design decision, D8 in
`DECISIONS.md`). Real seasons are entered through the admin surface.

### Admin account

Set `ADMIN_PASSWORD` (or `ADMIN_PASSWORD_HASH`, bcrypt — it wins if both are set) before first
start. `ADMIN_USER` defaults to `admin`. Rotate the password after your first login.

### Sessions

Either set `SESSION_SECRET`, or let the server create `data/secret.key` (0600) on first run.
Rotating either invalidates every existing session.

---

## 4. Deploy it

### Docker Compose

```bash
cp .env.example .env        # then edit the LEAGUE_* block
SEED=1 docker compose up -d --build     # SEED creates a demo season on first boot
```

`docker compose` reads `.env` through its `env_file:` entry, which is why the same file works for
both the Docker path and the from-source path.

### What production needs

- **`ADMIN_PASSWORD` or `ADMIN_PASSWORD_HASH`** — one of them, set for real. Then rotate.
- **`SESSION_SECRET` or `SESSION_SECRET_FILE`** — a generated `data/secret.key` is fine.
- **TLS in front.** Put a reverse proxy (Caddy, nginx) in front of the binary; the app sets HSTS when
  it sees `r.TLS != nil`, so terminate TLS at the proxy or at the app but not nowhere.
- **Backups.** The whole state is one SQLite file plus `data/secret.key`.

```bash
sqlite3 data/league.db ".backup data/backup-$(date +%Y%m%d-%H%M%S).db"
```

Restore is "stop the server, put the file back". `DATABASE.md` has the full runbook.

- **`SEED`** must be unset (`SEED=0`) on a real deployment — it creates a demo season.
- **Keep the container non-root.** The image already runs as a dedicated user; the entrypoint handles
  its own file permissions.

### Health

`GET /healthz` returns 200 when the server is up. Wire it to your supervisor, not to your users.

---

## 5. What not to change

- **The stack is pinned** — Go 1.26, SQLite via a pure-Go driver, `html/template`, htmx 4, TypeScript 7,
  Bun. Do not "downgrade for safety"; the offline build, the templates and the tests all assume it.
- **`internal/store/api.go` and applied migrations are the contract.** Add migrations, don't edit
  applied ones. Route registration goes through `register*Routes` functions — do not add a second
  registration for a path that already has one (§6).
- **`internal/web/branding_test.go`** — leave it. It is your de-identification net.
- **Never commit `.env`.** It is gitignored; keep it that way.

---

## 6. The traps that actually bite

Each of these cost a real bug in this codebase's history.

**1. Sourcing `.env` breaks on any value containing a space.**
`. ./.env` makes the shell try to *execute* the second word: `LEAGUE_NAME=Riverside Youth League`
leaves `LEAGUE_NAME=Riverside` and runs `Youth`. Use `tools/load-env.sh` (what `serve.sh` does), or
export the values inline.

**2. A duplicate route registration panics on the first request, not at build time.**
`net/http`'s ServeMux panics on a duplicate pattern *while the mux is being built*. The build, vet and
the test suite all stay green; the server dies on contact. If you add routes, check for duplicates:

```bash
grep -rhoP 'Router\.(GET|POST|PUT|PATCH|DELETE)\("\K[^"]+' internal/web/*.go | sort | uniq -d
```

Empty output is the only good output. (`internal/app/routes_test.go` does this for the club template;
the league's equivalent guard is its smoke gate.)

**3. A script with a shebang but no executable bit.**
Running `./tools/whatever.sh` the way the docs say gives "Permission denied". If you add a script:

```bash
chmod +x tools/yours.sh && git update-index --chmod=+x tools/yours.sh
```

**4. `go build` + `go vet` prove nothing about whether the app runs.**
Both of the failures above were invisible to them. Boot the thing and read the bytes — `tools/smoke.sh`
exists for exactly this.

**5. A global proxy eats localhost.**
On a host with `HTTPS_PROXY` set, requests to `127.0.0.1` get captured and fail mysteriously. Always
`export NO_PROXY=127.0.0.1,localhost`. `serve.sh` and `smoke.sh` set this for themselves.

**6. `data/` does not exist in a fresh clone.**
It is gitignored, and SQLite cannot create a file inside a missing directory. `tools/serve.sh`
creates it, and `cmd/seed` creates it before opening the database — but if you invoke the seed
generator yourself with a custom path, create the parent first.

---

## 7. Go-live checklist

- [ ] `./tools/instantiate.sh` ran and exited 0
- [ ] `curl -s http://<host>/ | grep '<title>'` shows **your** league name
- [ ] `go test ./...` green
- [ ] `SEED=1 tools/smoke.sh` prints `SMOKE OK`
- [ ] `ADMIN_PASSWORD` / `ADMIN_PASSWORD_HASH` set, and rotated after first login
- [ ] `SESSION_SECRET` or `SESSION_SECRET_FILE` in place
- [ ] TLS terminated somewhere in front
- [ ] `SEED=0` (or unset) on the real deployment
- [ ] `data/league.db` in your backup schedule, and one restore rehearsed
- [ ] `.env` not committed
