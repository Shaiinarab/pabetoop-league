PRAGMA foreign_keys = ON;

-- 0002 — one Premier League per (season, age category).
--
-- The table-level `UNIQUE (season_id, age_group_id, level, group_name)` cannot
-- enforce this rule. SQLite treats NULLs as distinct from each other in a UNIQUE
-- index, and premier rows carry `group_name = NULL` (enforced by the CHECK in
-- 0001), so a second premier competition for the same season+age passes the
-- constraint unnoticed.
--
-- A partial unique index is the real backstop. Origin: a duplicate premier was
-- created live on 2026-09-12 (found by
-- internal/web/competition_handlers_test.go::TestCompetitionCreateDuplicatePremierRefused).
-- Existing seeded data was verified free of duplicates before this index was
-- added (19 competitions, zero conflicting groups).
--
-- League 1 needs no equivalent: its group_name is NOT NULL, so the table-level
-- UNIQUE already fires for a repeated (season, age, level, group).
CREATE UNIQUE INDEX idx_competitions_premier_unique
    ON competitions(season_id, age_group_id)
    WHERE level = 'premier';
