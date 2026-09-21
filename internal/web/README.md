# internal/web — HTTP foundation + security layer (TASK-008)

Stdlib `net/http` only (no framework — DECISIONS.md D1). Files:

| File | Responsibility |
|---|---|
| `server.go` | `Server`, config from env, route table, page rendering, static cache policy |
| `middleware.go` | panic recovery, security headers, structured request logging |
| `security.go` | signed sessions, CSRF double-submit, rate-limited bcrypt login, `requireAdmin` |
| `funcmap.go` | template funcmap (`toFa`, `jalaliDate`, `jalaliLong`) + per-page template parsing |

## Route table

| Method + pattern | Handler | Access |
|---|---|---|
| `GET /{$}` | home (real template, placeholder data) | public |
| `GET /age/{id}` | age group page (`در دست ساخت` until data wiring) | public |
| `GET /competition/{id}` | competition page (same) | public |
| `GET /healthz` | `ok` (deployment probe) | public |
| `GET /static/` | file server, cache policy below | public |
| `GET /admin/login` | login form | public |
| `POST /admin/login` | credential check, rotates session | public (CSRF-protected) |
| `GET|POST /admin/logout` | clears session | public (POST is CSRF-protected) |
| `GET /admin`, `GET /admin/{$}` | placeholder dashboard | **admin session required** |

Middleware order (outermost first): recovery → security headers → request log →
CSRF cookie issuance → CSRF verification → mux.

## Environment

| Variable | Default | Notes |
|---|---|---|
| `ADDR` | `:8080` | flag `-addr` wins |
| `DB_PATH` | `data/pabetoop-league.db` | flag `-db` wins |
| `SESSION_SECRET` | — | HMAC secret for session/CSRF cookies |
| `SESSION_SECRET_FILE` | `data/secret.key` | used when `SESSION_SECRET` is unset |
| `ADMIN_USER` | `admin` | fixed single admin (D11) |
| `ADMIN_PASSWORD` | — | bcrypt-hashed at startup; **login disabled if unset** |
| `ADMIN_PASSWORD_HASH` | — | bcrypt hash; wins over `ADMIN_PASSWORD` |
| `TEMPLATES_DIR` | `web/templates` | |
| `STATIC_DIR` | `web/static` | |

## Security decisions

- **Headers on every response:** `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, a strict `Content-Security-Policy`
  (`default-src 'self'`, no inline styles/scripts — htmx and `app.js` are self-hosted),
  and HSTS **only when the request is already TLS** (`r.TLS != nil`).
- **Sessions:** HMAC-SHA256-signed cookie `session`, payload `{admin, iat}`,
  12-hour TTL, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` under TLS. Login rotates
  the session. Forged/tampered cookies fail signature verification (`TestSessionTamperRejected`).
- **CSRF:** signed double-submit. A token is issued on safe requests, embedded by
  templates as hidden `_csrf`, and sent by htmx as `X-CSRF-Token` (templates put
  `hx-headers:inherited` on `<body>` per htmx 4 rules). Every non-GET/HEAD/OPTIONS
  request under `/admin/` is verified in constant time; failure → 403 with the Persian
  message «نشست شما منقضی شده است؛ دوباره وارد شوید.».
- **Login:** fixed username, bcrypt comparison, generic Persian error
  «نام کاربری یا گذرواژه نادرست است.» (no user enumeration), per-IP limit of
  5 failures / 15 minutes → 429 with a Persian message.
- **Admin gate:** browsers get 303 → `/admin/login`; htmx requests get 403 +
  `HX-Redirect: /admin/login` (htmx 4 follows the header without swapping).
- **Static cache policy:** `.css`/`.js` → `max-age=300`; all other assets →
  `max-age=31536000, immutable`. Fingerprint/version asset filenames when deploy tooling lands.
- **Logging:** structured JSON (`slog`) with method, path, status, duration, bytes.
  Never logs bodies, cookies, headers, or query strings (queries can carry credentials).
- **Errors:** users always get Persian pages; stack traces only go to the log.

## Documented tradeoffs

1. **Session secret from file when env is absent.** `SESSION_SECRET` is preferred in
   production; otherwise a 32-byte random secret is generated and persisted to
   `data/secret.key` (0600). Tradeoff: the file lives next to the SQLite database, so
   anyone who can read the data directory can forge sessions — acceptable for a
   single-admin MVP on a private box, not for multi-tenant hosting. Rotating the
   secret invalidates all sessions (intended).
2. **Rate limiter is in-memory.** Per-process state, lost on restart, not shared
   between replicas. Fine for one admin and one process; swap for a store-backed
   counter before running more than one instance.
3. **No proxy headers trusted.** `clientIP` uses `RemoteAddr` only. When a reverse
   proxy (Caddy/Nginx) is added, switch to a configured `X-Forwarded-For` allowlist —
   otherwise the rate limiter can be bypassed by header spoofing.
4. **`ADMIN_PASSWORD` is plaintext env.** It is bcrypt-hashed in memory at startup and
   never logged. Prefer `ADMIN_PASSWORD_HASH` (e.g. from `htpasswd -bnBC 12 "" 'pw'`)
   for deployments where env values leak into process listings.

## Wiring the pages (TASK-005 contract)

`public_base.html` defines the skeleton with overridable `{{block "title"}}` /
`{{block "content"}}`; each page file overrides exactly those blocks. Pages therefore
**must not share one parse set** — `parseTemplates` clones the base+partials set per
page (`home`, `age_group`, `competition`) and renders through `public_base.html`.
Handlers pass view structs whose field names match the template contracts
(`store.Match`, `standing.Row` shapes); data access arrives with the milestone handlers.
