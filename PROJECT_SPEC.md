# PROJECT_SPEC — Pabetoop League (لیگ فوتبال نوجوانان)

> Status: **canonical** after deep local reconnaissance (2026-09-11). This document is the project's
> single source of product truth. Architecture decisions live in `DECISIONS.md`, technical mapping in
> `ARCHITECTURE.md`, schema in `DATABASE.md`.

---

## 1. Product Goal

A focused, reliable, Persian-language web platform for the **youth football competitions**:

> Store, manage, calculate, and publicly display competition **fixtures, results, league tables**,
> and related competition information — for five youth age categories, each with a Premier League
> and a variable number of parallel League 1 groups.

**NOT** (per client direction): club ERP, player management, news CMS, social network, live-score,
video, betting, analytics platform. No external data sources; **our database is authoritative**.

Design foundation so later features (players, scorers, cards, live, club profiles, API) can be added
without destroying the core data model.

---

## 2. Users

| User | Auth | Devices | Priority |
|---|---|---|---|
| **Administrator** (competition office, elderly, low–medium computer literacy) | single account | desktop-first, some mobile | **highest** |
| **Public** (parents, coaches, players, fans) | anonymous | mobile-first | high |

The administrator is the **sole authority over official names** (clubs, teams) and competition data.
The system never "corrects" official names; it may only *suggest* matches during import.

---

## 3. Platform constraints that shaped the stack

The decisions below came from what the target deployment could actually run — a small
VPS with no operational budget and no database administrator — not from preference.

| Constraint | Consequence |
|---|---|
| No PostgreSQL server, and none wanted at this size | SQLite in WAL mode, with the schema owned by migrations (D1). Containerised PostgreSQL is the documented upgrade path. |
| A pure-Go driver keeps the binary static and cross-compilable | `modernc.org/sqlite` — no cgo, no gcc on the build host (D1) |
| **htmx 4.0.0 GA (2026-08-28)** is not npm `latest`, which still resolves to 2.x | Pin `htmx.org@4.0.0` and vendor the built file. 4.x changes that matter here: `fetch()` internals, **explicit inheritance** (an attribute on a container only reaches children with `:inherited`), 4xx/5xx responses swap by default with `hx-status:422="target:#err"` routing, and `hx-ext`/`hx-vars` were removed (D2) |
| **TypeScript 7.0.2** is the Go-rewritten native compiler | Usable for the thin client layer, but it is optional: production serves no JS toolchain (D3) |
| Administrators are volunteers on phones | Server-rendered pages that work without JavaScript; htmx is progressive enhancement |
| One administrator operates the whole competition | A single admin account is a deliberate design choice, not a missing feature (D11) |

**Club names are data, not fixtures.** The platform ships a placeholder list
(`seed/clubs.json`, 73 obviously synthetic entries) that an operator replaces with their
own. No real club list is distributed with the template — see `seed/README.md`.

---

## 3b. Stack Decision (summary; full rationale in DECISIONS.md)

```text
Go 1.26 (stdlib net/http, net/http ServeMux)
SQLite (modernc.org/sqlite, pure Go, single file, WAL)   ← MVP; Postgres path documented
html/template server-side rendering, fully RTL Persian
htmx 4.0.0 (vendored; npm `latest` still serves 2.x — always pin htmx.org@4.0.0)
TypeScript 7.0.2 (npm `latest`) for minimal client enhancement only
Bun 1.4 as the JS toolchain (package manager, bundler, client tests — see DECISIONS.md D4)
No SPA, no ORM, no microservices, no paid SaaS
```

SQLite chosen because: single administrator, modest write volume, zero-ops backup (copy one file +
WAL checkpoint), no DB server to keep alive on a budget VPS. The repository layer uses plain
`database/sql` with parameterized SQL and a migration runner so Postgres later = new driver + new
migrations, not a rewrite.

---

## 4. Domain Model

```
Season (فصل)            e.g. ۱۴۰۵–۱۴۰۶; one active at a time
  └─ AgeGroup (رده سنی)  10..14 (fixed five for MVP, extensible table)
       └─ Competition (مسابقات)  level ∈ {premier, league1}; group ∈ {null, "A".."Z"}
            └─ Registration (ثبت‌نام)  team ∈ competition (season-scoped)
                 └─ Match (مسابقه)  week, date/time, venue(text), home/away team, status, scores
Club (باشگاه)  — the organization; official name is authority-controlled
  └─ Team (تیم)  belongs to exactly one club; official name, optional label/number
AuditLog (گزارش تغییرات) — every admin mutation: action, entity, id, before/after
```

Key distinctions (client-critical):

- **Club ≠ Team.** نمونه ب is a club; نمونه ب ۱ (U12) is a team.
- A club may own **multiple teams in the same age category** and multiple League 1 teams.
- A club may own **at most ONE team per age category's Premier League** (DB constraint).
- «نمونه ب» و «نمونه ب نوین» are **different clubs** — no auto-merging, no auto-renaming.
- Official names stored exactly as entered (whitespace-trimmed only).

### Match

- Belongs to one Competition. Both teams must be registered in that competition.
- `week` (integer, optional), `scheduled_date` (canonical DATE, Persian in UI), `scheduled_time` (optional),
  `venue` (plain text), `status` ∈ {SCHEDULED, FINISHED} (enum extensible, others not exposed in UI),
  `home_score`, `away_score` (nullable ints ≥ 0; both required together when FINISHED).
- **Fixtures are official/admin-entered or imported.** No auto-generation in the product. (A round-robin
  generator exists in the seed tool for realistic dev data only — never exposed in admin UI.)
- Home/away scores stored separately; away-goal ranking interpretation deliberately uninterpreted (see §8).

### Standings

Derived, deterministic, from FINISHED matches of a competition: Played/Won/Drawn/Lost/GF/GA/GD/Pts.
Points 3/1/0. Ordering: **Points → GD → GF → Persian-alphabetical fallback** (isolated in the standings
engine; rules can change in one place).

---

## 5. Information Architecture

### Public (anonymous, age-first nav)

```
خانه  |  ۱۰ سال  ۱۱ سال  ۱۲ سال  ۱۳ سال  ۱۴ سال
   age page: لیگ برتر + لیگ ۱ → گروه A/B/C/… cards
     competition page: جدول | نتایج | برنامه (tabs/sections)
```

Homepage: latest results, upcoming matches, age-category cards, competition highlights, stats.
Empty states in Persian everywhere; never a blank panel.

### Admin (breadcrumbs: فصل / رده / لیگ / گروه)

Dashboard → Clubs → Teams → Competitions → Registrations → Fixtures → **Quick Result Entry** →
Standings (view) → Import → Backup → Audit log. Every screen large controls, Persian labels,
confirmations before destructive ops, generous spacing.

**Quick result entry** is a first-class workflow: week selector → match list with two score inputs →
Enter-to-save → auto-focus next match → visible saved state → standings refresh live (htmx).

---

## 6. Scope

### In scope (MVP)

- Seasons (multiple, one active), 5 age groups, competitions (premier + variable League 1 groups)
- Clubs/teams CRUD with official-name authority, registrations with Premier-League uniqueness rule
- Manual fixture creation (+ clean import design: file → parse → normalize → validate → preview → confirm)
- Result entry (quick workflow), standings engine, public browsing
- Persian/Shamsi dates everywhere in UI, Persian numerals
- Single-admin auth, CSRF, security headers, rate-limited login
- Backup (one-click download; last-backup timestamp shown), audit log, Persian error messages
- Seed data (**73 clubs**, all 5 ages, premier + league1 groups, fixtures, results incl. tie-break edge cases)

### Explicitly out of scope (MVP)

Live scores, players/scorers/cards, news CMS, comments, search (extensible but not built), club logos,
club profile pages (data model ready), notifications, native apps, external APIs/scraping.

---

## 7. Data Integrity Rules (enforced server-side + DB constraints)

1. A team cannot play itself; both teams registered in the match's competition.
2. Duplicate registrations blocked (same team, same competition).
3. Premier League: max one team per club per age category — DB partial-unique constraint.
4. Scores: non-negative integers; FINISHED requires both scores; SCHEDULED requires none.
5. Duplicate fixtures (same competition, home, away, week) detected and rejected.
6. Match must reference valid teams; match competition must match registrations.
7. Deleting is restricted: clubs/teams with matches or registrations cannot be hard-deleted
   (deactivate instead); historical records always reconstructable; seasons are never deleted.
8. Import never silently mutates: preview + confirm; errors in plain Persian; ambiguities reported.
9. Standings always recomputed from finished matches — never stored as source of truth.
10. Every admin mutation writes an audit-log row (action, entity, entity_id, before, after, time).

---

## 8. Known Unknowns / Documented Assumptions

| # | Unknown | Decision (documented, reversible) |
|---|---|---|
| A1 | Exact "away goals matter more" official rule | Keep home/away scores separate in schema; standings engine accepts a pluggable ordering hook; **no formula invented**; tie-break = Points→GD→GF→Persian-alpha. Assumption documented in DECISIONS.md |
| A2 | Premier League size (how many teams?) | Not fixed by client; seed uses 12; real numbers come with real fixtures |
| A3 | Group labels future-proofing | Column is free-text (`group_name`), not enum; "A".."E" now |
| A4 | Match start times | Optional; plain `HH:MM` text, no timezone gymnastics for MVP (Iran abolished DST 2022; single +03:30 offset) |
| A5 | Age-group eligibility dates | Out of MVP; future columns reserved (not built) |
| A6 | Season naming | Free text like «۱۴۰۵–۱۴۰۶»; not derived from calendar |
| A7 | Venue | Free text (e.g. «زمین نمونه»); no venue table in MVP |

---

## 9. Deployment Plan

- **Now:** `go run ./cmd/server` (or built binary) on dev box; DB at `data/pabetoop-league.db`.
- **Later:** single static Go binary + SQLite file on an economical Iranian VPS behind Caddy/Nginx;
  Cloudflare optional in front. No paid SaaS in the critical path. Postgres container documented as
  the growth path (docker-compose example included but **not** default).
- Backups: admin one-click download + documented `sqlite3 .backup` flow; timestamped filenames.

---

## 10. Acceptance Checklist (Definition of Done, condensed)

- [ ] All §7 integrity rules enforced and tested
- [ ] Standings engine: deterministic + unit-tested (ties, GD, GF, Persian-alpha fallback)
- [ ] Jalali date engine: unit-tested conversion both ways, leap years, Persian rendering
- [ ] Admin can do a full season flow without documentation
- [ ] Quick result entry saves in <2 interactions per match
- [ ] Public pages usable on a phone; Persian RTL polished; identity distinctly sport/Persian
- [ ] Migrations versioned; tests pass; `go vet` clean; builds cleanly
- [ ] Backup download works; audit log records mutations; Persian error messages everywhere
- [ ] Seed data realistic and useful for edge-case testing

---

## 11. §-reference map (brief compatibility)

> **Read this before following a section number in a task brief.** Briefs written 2026-09-11
> (TASK-009…TASK-014) cite section numbers from a longer draft of this spec that was consolidated
> down to §1–§10. The requirements were kept; the numbering was not. Use this table to resolve an
> old reference to the current section.

| Brief cites | Actually means | Current section |
|---|---|---|
| §16 | public sees only clean data | §5 (Public) / §7 rule 9 |
| §17 | audit log fields | §7 rule 10 · `DATABASE.md` |
| §19–§21 | admin IA, breadcrumbs | §5 (Admin) |
| §20 | **quick result entry** (flagship workflow) | §5 (Admin, "Quick result entry" paragraph) |
| §21 | breadcrumbs | §5 (Admin) |
| §22–§27 | public information architecture | §5 (Public) |
| §24–§25 | homepage / competition page priorities | §5 (Public) |
| §27 | full team identity rule (club + age + group) | §4 (Club ≠ Team) |
| §36 | Jalali dates / Persian numerals | §6, D9 |
| §41–§43 | backup, destructive-op wording, import UX | §6 (in scope), §7 rule 8 |
| §70 | Persian error wording | §6, §7 |
| §72 | post-save safety | §7 rules 4/5/9 |
| §73 | backup UX — keep it extremely simple | §6 (backup bullet) |

**Rule for new briefs:** cite a current section number from this document, or quote the
requirement inline. A reference that cannot be resolved is an acceptance criterion nobody can
verify — which is how the TASK-006 anchor and leap-list errors slipped through.
