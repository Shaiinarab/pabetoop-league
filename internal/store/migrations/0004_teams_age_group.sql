PRAGMA foreign_keys = ON;

-- 0004 — a team remembers the age category it was created for.
--
-- The admin team form has always required «ردهٔ سنی», but `teams` had nowhere to
-- put it: `handleAdminTeamCreate` validated the field, echoed it back on error,
-- then called `CreateTeam(club, label, display_name)` and returned «ثبت شد.»
-- (TASK-029). PROJECT_SPEC §4/§27 give a team identity as club + age category +
-- group, so the model could not express what the form asked for.
--
-- ponytail: nullable for existing rows — a historic team's category is not
-- derivable (it is really carried by the competition a team is registered in,
-- which has its own age_group_id). Ceiling: rows created before this migration
-- keep age_group_id NULL and are never shown a category. Upgrade path: backfill
-- from the earliest registration's competition if a real dataset ever needs it.
-- The column MUST be nullable: a NOT NULL column with no default aborts
-- Migrate() on a populated database (the seeded data/pabetoop-league.db), taking
-- the server's boot with it — the same trap 0003 documented.
ALTER TABLE teams ADD COLUMN age_group_id INTEGER REFERENCES age_groups(id);
