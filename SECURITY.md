# SECURITY

> Threat model for a single-admin, anonymous-public, self-hosted Persian web
> app. Controls verified 2026-09-12 against `internal/web/security.go`,
> `middleware.go`, `server.go`. Method: `ecc-security-review` control classes
> (auth, session, CSRF, headers, input, secrets, logging).
>
> The header contract in §2 is enforced by `internal/web/security_test.go`
> (TASK-022): the promised set is written out literally there, so a header that
> is deleted or weakened fails the suite instead of shipping unnoticed.

## 1. What we protect

- Competition data integrity (fixtures, results, tables are the product).
- The single admin account (sole write authority).
- Operator box (single Go binary + SQLite file on a budget VPS).

Out of scope: DDoS absorption, WAF, multi-user abuse — handled at the
reverse proxy / operator level, not in the app.

## 2. Controls (implemented)

| Area | Control |
|---|---|
| Auth | Fixed single admin (D11). bcrypt-hashed password (`ADMIN_PASSWORD` hashed at startup; `ADMIN_PASSWORD_HASH` preferred). Generic Persian login error (no user enumeration). Per-IP rate limit: 5 failures / 15 min → 429 Persian message. |
| Sessions | HMAC-SHA256-signed cookie (`session`, `{admin, iat}`), 12h TTL, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` under TLS. Login rotates the session; tampered cookies fail verification. |
| CSRF | Signed double-submit token on safe requests; hidden `_csrf` + `X-CSRF-Token` via `hx-headers:inherited` (htmx 4 rule). Every non-GET/HEAD/OPTIONS under `/admin/` verified in constant time → 403 Persian «نشست شما منقضی شده است…». |
| Admin gate | `requireAdmin`: browsers → 303 `/admin/login`; htmx → 403 + `HX-Redirect`. |
| Headers | Every response: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, strict CSP (`default-src 'self'`, no inline styles/scripts, `frame-ancestors 'none'`), and an explicit deny-list `Permissions-Policy` — every capability the product does not use (camera, microphone, geolocation, display-capture, USB, serial, payment, sensors, WebAuthn) carries an empty allow-list, because an unmentioned feature keeps its default allow-list. HSTS is **deliberately off on plain HTTP**: sent only when `r.TLS != nil`, since the app ships behind Caddy/Nginx on HTTP today and HSTS from an HTTP origin is ignored at best. The set is one declarative list (`baselineSecurityHeaders`, `internal/web/security.go`) applied by `securityHeaders` and asserted by `security_test.go`. |
| Input / SQL | All queries parameterized via `database/sql`; validation server-side with Persian messages; DB backstops (FKs, CHECKs, partial-unique, triggers). Import pipeline is preview → confirm, never silent mutation. |
| XSS | `html/template` auto-escaping everywhere; no inline scripts; htmx + `app.js` self-hosted. |
| Secrets | `SESSION_SECRET` env preferred; fallback `data/secret.key` (0600, auto-created). `ADMIN_PASSWORD` never logged. |
| Logging | Structured JSON (`slog`): method, path, status, duration, bytes. Never bodies, cookies, headers, or query strings. Stack traces to log only; users get Persian pages. |
| Timeouts | `ReadHeaderTimeout` 10s, `WriteTimeout` 30s, `IdleTimeout` 60s. |

## 3. Residual risks (accepted, documented)

1. **Secret file next to the DB.** Anyone who can read `data/` can forge
   sessions. Acceptable for a single-admin private box; prefer
   `SESSION_SECRET` env in production.
2. **In-memory rate limiter.** Lost on restart, per-process. Fine for one
   process; needs a store-backed counter before horizontal scaling.
3. **No proxy-header trust.** `clientIP` uses `RemoteAddr` only — correct
   today, but when Caddy/Nginx fronts the app the limiter needs an
   `X-Forwarded-For` allowlist or it can be spoofed.
4. **Plaintext `ADMIN_PASSWORD` env** appears in process listings — prefer
   `ADMIN_PASSWORD_HASH` on shared hosts.
5. **No MFA / no RBAC.** Deliberate (D11): one admin, physical-device
   discipline is the second factor. Revisit if admins multiply.

## 4. Operator checklist

- [ ] Set `ADMIN_PASSWORD_HASH` (not plaintext) + `SESSION_SECRET` in the
  systemd unit environment; no secrets on disk.
- [ ] Terminate TLS at the reverse proxy; forward `X-Forwarded-Proto`.
- [ ] Run as unprivileged user (`NoNewPrivileges=true`, see `DEPLOY.md`).
- [ ] Keep `data/` backups off-box (admin download + `.backup` flow).
- [ ] Re-run `go vet ./...` + `tools/smoke.sh` after every deploy.
