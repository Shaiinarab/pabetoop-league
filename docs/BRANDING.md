# Branding and white-labelling

This repository is a **template**: it must contain no league, city, competition or club
name. That is not a stylistic preference — a former deployment's identity leaking into a
template is a correctness bug, so it is enforced by a test.

## 1. The contract

Every league-facing string resolves through `internal/site`:

```go
profile := site.Active()
profile.NameFA      // "لیگ فوتبال نوجوانان"
profile.Subtitle()  // "سامانهٔ نتایج مسابقات فوتبال" — derived, or overridden
```

`internal/web` exposes two resolved values that templates consume:

| Value | Template field | Used by |
|---|---|---|
| `SiteName` | `.SiteName` | `<title>` blocks, the site header, login and error pages |
| `SiteSubtitle` | `.SiteSubtitle` | Public footer, admin login strapline |

No template, Go file or static asset may contain a hard-coded league name.
`internal/web/branding_test.go` enforces it:

```
TestRenderedPagesCarryNoLegacyIdentity   ← the anti-leak gate
TestPagesFollowActiveSite                ← changing the profile changes every page
TestSiteSubtitleCollapses                ← optional fields degrade cleanly
```

## 2. Configuration

| Variable | Example | Effect when empty |
|---|---|---|
| `LEAGUE_NAME` | `Riverside Youth League` | Latin name keeps the template default |
| `LEAGUE_NAME_FA` | `لیگ نوجوانان رودخانه` | Persian display name |
| `LEAGUE_DISCIPLINE_FA` | `فوتسال` | Derives the strapline; empty yields a generic one |
| `LEAGUE_SUBTITLE_FA` | `سامانهٔ نتایج فوتسال` | Overrides the derived strapline entirely |

A variable set to an empty string is meaningful and distinct from leaving it unset: unset
keeps the template default, empty switches to the derived or generic form.

## 3. Identity surfaces

These are the only places a deployment's identity appears. All read the profile; none
store a name.

| Surface | Source |
|---|---|
| Public page titles | `web/templates/{home,age_group,competition}.html` → `{{.SiteName}}` |
| Site header | `web/templates/public_base.html` |
| Public footer strapline | `web/templates/public_base.html` → `{{.SiteSubtitle}}` |
| Admin page titles | `web/templates/admin_base.html` |
| Admin login page | `web/templates/admin/login.html` |
| Inline error + login pages | `internal/web/security.go` (their own small templates) |
| Backup filenames | `internal/web/backup_handlers.go` (prefix from the binary name) |

### Known limit

`persianErrorTmpl` and `loginTmpl` are parsed at package init. They render the name via
the template data, so they follow `site.Active()` — but the identity is resolved once per
process. **Re-branding a running process is not supported**: set `LEAGUE_*` in the
environment before starting the binary. That is also why `refreshSiteIdentity()` exists
only for tests.

## 4. Instantiating a new deployment

```bash
./tools/instantiate.sh \
  --name          "Riverside Youth League" \
  --name-fa       "لیگ نوجوانان رودخانه" \
  --discipline-fa "فوتسال" \
  --module-path   github.com/yourorg/riverside-league
```

The script:

1. rewrites the Go module path across `go.mod` and every import;
2. renames the binary, the Docker image and the compose volume;
3. writes the `LEAGUE_*` block into `.env` (copying `.env.example` if needed);
4. runs `go build ./...` and the branding leak test as a post-condition.

It refuses to run on a dirty tree, so a botched instantiation is always revertible with
`git checkout .`.

## 5. Club names are data, not branding

The distinction matters:

- **League identity** (the name on the page) is *branding* — it belongs in `internal/site`.
- **Club and team names** are *records*. They live in the database, come from the
  administrator or `seed/clubs.json`, and the application never rewrites them.

A club name must never appear in Go source, a template, a test fixture beyond an obvious
placeholder, or documentation. The shipped seed list is deliberately synthetic
(`باشگاه نمونه ۱` … `۷۳`) and the operator is expected to replace it — see
[`../seed/README.md`](../seed/README.md).

## 6. Adding another locale

The UI is Persian-first by design (RTL, Jalali calendar, Persian digits). To ship a
second locale:

1. **Extract the strings before adding a second one.** Move the Persian copy out of the
   templates and handlers into a message catalogue keyed by id. Translating in place
   produces two languages interleaved through the same files.
2. **Set the direction and language** in `public_base.html` / `admin_base.html` from the
   active locale rather than hard-coding `dir="rtl"` and `lang="fa"`.
3. **Keep the calendar at the boundary.** `internal/jalali` converts for display only;
   storage stays UTC/ISO. A Gregorian locale simply stops calling it.
4. **Number formatting is a display concern.** Persian digits are produced at render
   time; the database stores Latin digits.

This is deliberately left as an exercise rather than a half-built abstraction: the
platform ships one locale done properly instead of two done approximately.

## 7. What must never be committed

- A real league's name in the templates, the defaults, or `.env.example`.
- A real club list in `seed/clubs.json` — including a "just for testing" copy.
- Real member or guardian data of any kind.
- Screenshots of a live competition showing real club names.

Demo data is entirely synthetic and must only ever be combined with a development
database.
