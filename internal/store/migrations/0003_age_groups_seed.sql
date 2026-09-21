PRAGMA foreign_keys = ON;

-- 0003 — the five fixed age categories exist on a fresh install.
--
-- 0001 creates the `age_groups` TABLE but inserts no rows, and the only caller of
-- `EnsureAgeGroups` was `cmd/seed`. So a fresh install (`Migrate()` + `cmd/server`,
-- no SEED=1) had an EMPTY age-group list — which quietly moved the "fresh install
-- is a dead end" bug (fb3 audit F2) one screen later: the operator could now create
-- and activate a season (TASK-016) but the competitions form's «ردهٔ سنی» select had
-- nothing in it, so no competition could ever be created.
--
-- These five are fixed for the MVP: README «five youth age categories (10–14)», and
-- the spec keeps them in a table (not an enum) so a future category needs no rewrite
-- — hence a data migration rather than a code change, and hence `OR IGNORE`.
--
-- INSERT OR IGNORE is required, not defensive: this migration also runs on an
-- EXISTING database whose rows came from `EnsureAgeGroups`, where a plain INSERT
-- would hit UNIQUE(age) and abort `Migrate()` — taking the server's boot with it.
-- Applied once per database (schema_migrations), so existing rows are never touched.
--
-- display_name matches `persianAgeLabel(age)` in store.go exactly («۱۲ سال»), and
-- sort_order matches `age` — the same values the seed path writes. No audit rows are
-- produced here on purpose: this is install-time reference data, not an operator
-- mutation (§7 rule 10 covers changes an administrator makes).
INSERT OR IGNORE INTO age_groups (age, display_name, sort_order) VALUES
    (10, '۱۰ سال', 10),
    (11, '۱۱ سال', 11),
    (12, '۱۲ سال', 12),
    (13, '۱۳ سال', 13),
    (14, '۱۴ سال', 14);
