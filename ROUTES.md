# ROUTES — Complete HTTP route table

> Verified 2026-09-12 against `internal/web/server.go`,
> `public_handlers.go`, `admin_handlers.go`, `result_handlers.go`,
> `fixture_handlers.go` (`RegisterFixtureImportRoutes` — **wired** into `Handler()`
> by TASK-012), `backup_handlers.go`, `competition_handlers.go`,
> `season_handlers.go`, `audit_handlers.go`.
> Canonical competition tab form is the path form (D14).

## Public (anonymous)

| Method + pattern | Handler | Notes |
|---|---|---|
| `GET /{$}` | `handlePublicHome` | Latest results, upcoming, age cards, stats |
| `GET /age/{id}` | `handlePublicAgeGroup` | Premier + League 1 group cards for the age |
| `GET /competition/{id}` | `handlePublicCompetition` | Defaults to table tab |
| `GET /competition/{id}/{tab}` | `handlePublicCompetition` | `{tab}` ∈ `table`, `results`, `fixtures`; `?tab=` accepted as legacy alias |
| `GET /healthz` | inline | `ok` (deployment probe) |
| `GET /static/` | `staticHandler` | `.css/.js → max-age=300`; other assets immutable 1y |

Unknown competition/age ids → Persian 404 (`پیدا نشد`).

## Admin — session (`requireAdmin`)

Browsers without a session get `303 → /admin/login`; htmx requests get
`403 + HX-Redirect: /admin/login`.

| Method + pattern | Handler | Notes |
|---|---|---|
| `GET /admin/login` | `handleLoginGET` | Login form |
| `POST /admin/login` | `handleLoginPOST` | bcrypt check, rotates session; CSRF-protected |
| `GET,POST /admin/logout` | `handleLogout` | Clears session (POST is CSRF-protected) |
| `GET /admin`, `GET /admin/{$}` | `handleAdminDashboard` | Dashboard (exact forms, no 307 hop) |
| `GET /admin/seasons` | `handleSeasonsList` | Season list + create form (one page) |
| `GET /admin/seasons/new` | `handleSeasonsList` | (same list view hosts the form) |
| `POST /admin/seasons` | `handleSeasonCreate` | Create season; Jalali dates via `internal/jalali`, stored as Gregorian ISO |
| `POST /admin/seasons/{id}/edit` | `handleSeasonEdit` | Rename / adjust dates (trimmed only, D7) |
| `POST /admin/seasons/{id}/activate` | `handleSeasonActivate` | Single-active rule (store deactivates the rest in one tx); plain POST + redirect |
| ~~`POST /admin/seasons/{id}/delete`~~ | — | **No such route, by design** (§7 rule 7: seasons are never deleted). Pinned by a test and a live check. |
| `GET /admin/clubs` | `handleAdminClubsList` | Club list |
| `GET /admin/clubs/new` | `handleAdminClubNew` | New-club form |
| `POST /admin/clubs` | `handleAdminClubCreate` | Create club (official-name authority, D7) |
| `POST /admin/clubs/{id}/rename` | `handleAdminClubRename` | Rename (duplicate → Persian error) |
| `POST /admin/clubs/{id}/delete` | `handleAdminClubDelete` | Blocked when history exists (deactivate instead) |
| `GET /admin/teams?page=` | `handleAdminTeamsList` | Team list: 50 rows/page, store-side total (`TeamsPage`), each row shows its «ردهٔ سنی» (`Team.AgeGroupName`, «—» when unset); `?page=` past the end → Persian empty state (TASK-023, TASK-025) |
| `GET /admin/teams/new` | `handleAdminTeamNew` | New-team form |
| `POST /admin/teams` | `handleAdminTeamCreate` | Create team under a club |
| `GET /admin/teams/{id}/edit` | `handleAdminTeamEdit` | Edit form, prefilled from `TeamByID`; the club select is **locked** (not writable by `UpdateTeam`); unknown id → Persian 404 (TASK-037) |
| `POST /admin/teams/{id}` | `handleAdminTeamUpdate` | Save an edit (label, display name, age category); per-field 422 re-render like create; unknown category → Persian 422 naming the id (TASK-037) |
| `POST /admin/teams/{id}/deactivate` | `handleAdminTeamDeactivate` | Blocked with active registrations |
| `GET /admin/results` | `handleAdminResults` | Quick result entry: week selector + score inputs |
| `GET /admin/results/standings` | `handleAdminResultsStandings` | Live standings fragment (htmx refresh) |
| `POST /admin/results/{id}` | `handleAdminResultSet` | Set result (Enter-to-save, auto-focus next) |
| `POST /admin/results/{id}/clear` | `handleAdminResultClear` | Return match to SCHEDULED |
| `GET /admin/competitions` | `handleCompetitionsList` | Competition list |
| `GET /admin/competitions/new` | `handleCompetitionsList` | (same list view hosts the form) |
| `POST /admin/competitions` | `handleCompetitionCreate` | Create competition |
| `POST /admin/competitions/{id}/edit` | `handleCompetitionEdit` | Rename / change League 1 group label (trimmed only, D7) |
| `POST /admin/competitions/{id}/delete` | `handleCompetitionDelete` | Blocked while registrations or matches exist |
| `GET /admin/competitions/{id}/registrations` | `handleRegistrations` | Registration list for a competition |
| `POST /admin/competitions/{id}/register` | `handleRegister` | Register team (Premier club rule enforced) |
| `POST /admin/competitions/{id}/unregister` | `handleUnregister` | Blocked if team has matches there |
| `GET /admin/backup` | `handleBackupPage` | Backup page + last-backup timestamp |
| `GET /admin/backup/download` | `handleBackupDownload` | Consistent `VACUUM INTO` snapshot download |
| `GET /admin/backup/status` | `handleBackupStatus` | Backup status fragment |
| `GET /admin/audit?action=&entity=&page=` | `handleAuditPage` | Audit log: exact-match filters, 50 rows/page, store-side total (`AuditLogFiltered`); read-only — no POST route exists |

## Fixtures + import pipeline (**wired** since 2026-09-12)

`RegisterFixtureImportRoutes` defines the six routes below; `Handler()` calls it
(`s.RegisterFixtureImportRoutes(mux)`). They returned 404 for a period because the
handler lane and the mount point were written by different agents and only the mount
point connects them — a whole handler lane that builds, vets and tests clean while being
unreachable in production. Found by an internal route audit and fixed here.

| Route | Handler | Notes |
|---|---|---|
| `GET /admin/fixtures` | `handleAdminFixtures` | Fixture list |
| `POST /admin/fixtures` | `handleAdminFixtureCreate` | Manual fixture entry (D8: official data, never generated) |
| `POST /admin/fixtures/{id}/delete` | `handleAdminFixtureDelete` | Destructive; `data-confirm` |
| `GET /admin/import` | `handleAdminImport` | Import surface |
| `POST /admin/import` | `handleAdminImportPreview` | parse → normalize → **preview** |
| `POST /admin/import/confirm` | `handleAdminImportConfirm` | Commit the previewed rows |

Verified live 2026-09-12: both `GET /admin/fixtures` and `GET /admin/import` → 200
(previously 404), and `tmp/verify-012.sh` exercises the whole lane end to end
(55 checks: create/delete, every refusal, preview, confirm, token replay,
resolution, CSRF, auth).

Both handler lanes now have test files — `fixture_handlers_test.go` (11 tests)
and `import_handlers_test.go` (8 tests), added by TASK-012, which closes the
route audit's finding F3 for this lane. `TestFixtureRoutesAreMounted` is deliberately a
**mount pin**: it asserts 200 through the real `Handler()`, so un-wiring
`RegisterFixtureImportRoutes` fails the suite instead of silently shipping 404s
again.

Load-bearing details, because they are easy to break silently:

- Fixtures are **official data entered by the administrator**; the product never
  generates them (D8). Dates may be entered Jalali («۱۴۰۵/۰۸/۰۱») or ISO and are
  stored as canonical ISO Gregorian (D9).
- Both team dropdowns offer **only teams registered in the selected
  competition**, and the handler mirrors the store's integrity rules before
  writing, so the operator gets one clear Persian message instead of a
  constraint error.
- The import pipeline writes **nothing** before confirmation: the parsed preview
  is held server-side behind a one-shot, expiring token. Error rows never
  import; a row the store rejects is reported by file line (no bulk store method
  exists, so each row is its own transaction — never a silent partial import).
  Name resolution is an explicit per-row pick, and official names are never
  rewritten, merged or auto-corrected (D7).
- These paths use POST-redirect-GET, so the Persian flash message travels in a
  **percent-encoded** cookie: `net/http` silently drops non-ASCII cookie bytes,
  which turned every Persian flash message into its ASCII fragments until
  `render.go` started encoding (see TASK-012's report).

## Conventions

- All `/admin/*` mutations are POST with CSRF (hidden `_csrf` +
  `X-CSRF-Token` via `hx-headers:inherited`).
- Validation failures → **422 partials** into `#form-errors`; multi-target
  successes use `<hx-partial>` (row + standings + flash).
- Every admin mutation writes an `audit_log` row in the same transaction.
