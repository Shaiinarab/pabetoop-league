PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

-- ===================== Seasons =====================
CREATE TABLE seasons (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,              -- official label, e.g. «۱۴۰۵–۱۴۰۶»
    starts_on   TEXT,                       -- canonical ISO date (Gregorian)
    ends_on     TEXT,
    is_active   INTEGER NOT NULL DEFAULT 0 CHECK (is_active IN (0,1)),
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (name)
);

-- ===================== Age groups =====================
-- Fixed five for MVP, but a table (not enum) so future categories need no rewrite.
CREATE TABLE age_groups (
    id           INTEGER PRIMARY KEY,
    age          INTEGER NOT NULL UNIQUE,    -- 10..14
    display_name TEXT NOT NULL,              -- «۱۲ سال»
    sort_order   INTEGER NOT NULL DEFAULT 0
);

-- ===================== Clubs =====================
CREATE TABLE clubs (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,                -- official name, authority-controlled (D7)
    is_active  INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (name)
);

-- ===================== Teams =====================
CREATE TABLE teams (
    id           INTEGER PRIMARY KEY,
    club_id      INTEGER NOT NULL REFERENCES clubs(id),
    label        TEXT NOT NULL DEFAULT '',   -- official component, e.g. «۱», «الف», or «»
    display_name TEXT NOT NULL,              -- composed official display name
    is_active    INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (club_id, label)
);

-- ===================== Competitions =====================
-- level: 'premier' | 'league1'; group_name: NULL for premier, «A».. for league1
CREATE TABLE competitions (
    id           INTEGER PRIMARY KEY,
    season_id    INTEGER NOT NULL REFERENCES seasons(id),
    age_group_id INTEGER NOT NULL REFERENCES age_groups(id),
    level        TEXT NOT NULL CHECK (level IN ('premier','league1')),
    group_name   TEXT,
    display_name TEXT NOT NULL,
    CHECK ((level = 'premier' AND group_name IS NULL) OR (level = 'league1' AND group_name IS NOT NULL)),
    UNIQUE (season_id, age_group_id, level, group_name)
);

-- ===================== Registrations =====================
-- club_id is denormalized from teams for constraint enforcement + historical
-- integrity (the club a team represented at registration time never changes).
CREATE TABLE registrations (
    id             INTEGER PRIMARY KEY,
    competition_id INTEGER NOT NULL REFERENCES competitions(id),
    team_id        INTEGER NOT NULL REFERENCES teams(id),
    club_id        INTEGER NOT NULL REFERENCES clubs(id),
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (competition_id, team_id)
);

-- Premier League rule (D7): at most ONE team per club per premier competition.
-- Triggers can query across tables; CHECK constraints cannot.
CREATE TRIGGER trg_premier_club_unique_insert
BEFORE INSERT ON registrations
WHEN EXISTS (
    SELECT 1
    FROM registrations r2
    JOIN competitions c2 ON c2.id = r2.competition_id
    JOIN competitions c  ON c.id  = NEW.competition_id
    WHERE c2.id = c.id
      AND c.level = 'premier'
      AND r2.club_id = NEW.club_id
)
BEGIN
    SELECT RAISE(ABORT, 'یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد');
END;

CREATE TRIGGER trg_premier_club_unique_update
BEFORE UPDATE OF competition_id, team_id, club_id ON registrations
WHEN EXISTS (
    SELECT 1
    FROM registrations r2
    WHERE r2.competition_id = NEW.competition_id
      AND r2.club_id = NEW.club_id
      AND r2.id != NEW.id
      AND (SELECT level FROM competitions WHERE id = NEW.competition_id) = 'premier'
)
BEGIN
    SELECT RAISE(ABORT, 'یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد');
END;

-- ===================== Matches =====================
CREATE TABLE matches (
    id             INTEGER PRIMARY KEY,
    competition_id INTEGER NOT NULL REFERENCES competitions(id),
    home_team_id   INTEGER NOT NULL REFERENCES teams(id),
    away_team_id   INTEGER NOT NULL REFERENCES teams(id),
    week           INTEGER CHECK (week IS NULL OR week >= 1),
    scheduled_date TEXT,                    -- canonical ISO date (Gregorian), Jalali in UI (D9)
    scheduled_time TEXT,                    -- plain «HH:MM» text (spec A4)
    venue          TEXT,                    -- free text (spec A7)
    status         TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled','finished')),
    home_score     INTEGER CHECK (home_score IS NULL OR home_score >= 0),
    away_score     INTEGER CHECK (away_score IS NULL OR away_score >= 0),
    notes          TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at     TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (home_team_id != away_team_id),
    -- finished matches carry both scores; scheduled matches carry none
    CHECK ( (status = 'finished' AND home_score IS NOT NULL AND away_score IS NOT NULL)
         OR (status = 'scheduled' AND home_score IS NULL AND away_score IS NULL) )
);

CREATE INDEX idx_matches_competition ON matches(competition_id, week);
CREATE INDEX idx_matches_date ON matches(scheduled_date);

-- Duplicate fixture guard: same competition, same pairing, same week
CREATE UNIQUE INDEX idx_matches_unique_fixture
ON matches(competition_id, week, home_team_id, away_team_id)
WHERE week IS NOT NULL;

-- Both teams must be registered in the match's competition (DB backstop;
-- friendly Persian errors come from the service layer).
CREATE TRIGGER trg_match_teams_registered_insert
BEFORE INSERT ON matches
WHEN NEW.home_team_id = NEW.away_team_id
   OR NOT EXISTS (SELECT 1 FROM registrations WHERE competition_id = NEW.competition_id AND team_id = NEW.home_team_id)
   OR NOT EXISTS (SELECT 1 FROM registrations WHERE competition_id = NEW.competition_id AND team_id = NEW.away_team_id)
BEGIN
    SELECT RAISE(ABORT, 'هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند');
END;

CREATE TRIGGER trg_match_teams_registered_update
BEFORE UPDATE OF competition_id, home_team_id, away_team_id ON matches
WHEN NEW.home_team_id = NEW.away_team_id
   OR NOT EXISTS (SELECT 1 FROM registrations WHERE competition_id = NEW.competition_id AND team_id = NEW.home_team_id)
   OR NOT EXISTS (SELECT 1 FROM registrations WHERE competition_id = NEW.competition_id AND team_id = NEW.away_team_id)
BEGIN
    SELECT RAISE(ABORT, 'هر دو تیم مسابقه باید در همین مسابقات ثبت‌نام شده باشند');
END;

-- ===================== Audit log =====================
CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY,
    action     TEXT NOT NULL,               -- 'insert' | 'update' | 'delete' | 'import' | 'backup' ...
    entity     TEXT NOT NULL,               -- 'club' | 'team' | 'match' | ...
    entity_id  INTEGER,
    before     TEXT,                        -- JSON snapshot where useful
    after      TEXT,                        -- JSON snapshot where useful
    admin      TEXT NOT NULL DEFAULT 'admin',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_audit_entity ON audit_log(entity, entity_id);
CREATE INDEX idx_audit_time ON audit_log(created_at);
