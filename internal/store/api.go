package store

// This file is the API CONTRACT for the data layer (TASK-004 implements it;
// cmd/seed, admin handlers, and public pages consume it). Types here are
// authoritative; method signatures must match exactly. Persian error messages
// are REQUIRED for all user-triggerable validation failures (PROJECT_SPEC §70).

import "time"

// ---------- Entities ----------

type Season struct {
	ID       int64
	Name     string // official label, e.g. «۱۴۰۵–۱۴۰۶»
	StartsOn *string
	EndsOn   *string
	IsActive bool
}

type AgeGroup struct {
	ID          int64
	Age         int
	DisplayName string
}

type Club struct {
	ID       int64
	Name     string // official name, authority-controlled (D7)
	IsActive bool
}

type Team struct {
	ID          int64
	ClubID      int64
	ClubName    string
	Label       string
	DisplayName string
	IsActive    bool

	// AgeGroupID is the team's age category — part of a team's identity
	// (PROJECT_SPEC §27: club + age category + group). It is a pointer because
	// the column is nullable **by design**: cmd/seed and pre-0004 rows have no
	// category, and a historic row's category is not derivable, so it stays nil
	// rather than being guessed (D20).
	AgeGroupID *int64
	// AgeGroupName is the joined age_groups.display_name, so the teams list can
	// show the category without a second round trip. Empty when AgeGroupID is
	// nil; the join is a LEFT JOIN, so a team without one is still returned.
	AgeGroupName string
}

type Competition struct {
	ID            int64
	SeasonID      int64
	SeasonName    string
	AgeGroupID    int64
	AgeGroupAge   int
	Level         string  // "premier" | "league1"
	GroupName     *string // nil for premier, «A».. for league1
	DisplayName   string
	Registrations int // count of registered teams (filled by queries)
}

type Registration struct {
	ID            int64
	CompetitionID int64
	TeamID        int64
	ClubID        int64
	TeamName      string
	ClubName      string
}

type Match struct {
	ID            int64
	CompetitionID int64
	HomeTeamID    int64
	AwayTeamID    int64
	HomeTeamName  string
	AwayTeamName  string
	Week          *int
	ScheduledDate *string // canonical ISO Gregorian; render via internal/jalali (D9)
	ScheduledTime *string // «HH:MM»
	Venue         *string
	Status        string // "scheduled" | "finished"
	HomeScore     *int
	AwayScore     *int
	Notes         *string
}

type AuditEntry struct {
	ID        int64
	Action    string
	Entity    string
	EntityID  int64
	Before    *string
	After     *string
	Admin     string
	CreatedAt time.Time
}

// ---------- DataStore contract ----------

// DataStore is implemented by *Store (TASK-004). All mutating methods write an
// audit-log row. All validation errors are Persian and reference the entity
// context (e.g. include team/competition names where known).
type DataStore interface {
	// Lifecycle
	Close() error
	Migrate() error // applies embedded migrations in order, idempotent

	// Seasons
	CreateSeason(name string, startsOn, endsOn *string) (int64, error)
	UpdateSeason(id int64, name string, startsOn, endsOn *string) error
	ActivateSeason(id int64) error // atomically deactivates all others
	ActiveSeason() (*Season, error)
	Seasons() ([]Season, error)

	// Age groups
	EnsureAgeGroups(ages ...int) error // inserts missing, sorted; idempotent
	AgeGroups() ([]AgeGroup, error)

	// Clubs
	CreateClub(name string) (int64, error) // duplicate → «باشگاهی با این نام قبلاً ثبت شده است: …»
	Clubs(includeInactive bool) ([]Club, error)
	RenameClub(id int64, newName string) error // duplicate → same friendly error
	DeactivateClub(id int64) error             // blocked if any active team has active registrations

	// Teams
	CreateTeam(clubID int64, label, displayName string) (int64, error) // no category: age_group_id NULL
	// CreateTeamWithAgeGroup is the admin create path and stores the age
	// category the team form requires (PROJECT_SPEC §27). A nil ageGroupID keeps
	// age_group_id NULL — that is what CreateTeam and cmd/seed do. An unknown
	// ageGroupID is refused in Persian, never as a raw FK error.
	CreateTeamWithAgeGroup(clubID int64, ageGroupID *int64, label, displayName string) (int64, error)
	Teams(clubID *int64) ([]Team, error) // nil clubID = all
	// TeamsPage returns one page of teams plus the total number matching the
	// filter, ordered exactly as Teams — the page query and the count share one
	// WHERE, so a caller can neither drop nor duplicate a row while paging.
	// Inputs are clamped like AuditLogFiltered: limit <= 0 → 50, limit > 500 →
	// 500, offset < 0 → 0.
	TeamsPage(clubID *int64, limit, offset int) (teams []Team, total int, err error)
	// TeamByID is the single-row read the edit form needs. Teams()[0]-style
	// lookups would load every team to edit one.
	TeamByID(id int64) (Team, error) // unknown id → ErrNotFound in Persian
	// UpdateTeam is the admin edit path (TASK-037): it persists the label,
	// the display name and the age category in one transaction, and audits the
	// true before/after of every field it touches — including a nil↔value
	// transition on the category.
	//
	// A nil ageGroupID means "leave the category unchanged", not "clear it":
	// the form always submits a category (it is required, and CreateTeamWithAgeGroup
	// enforces the same), so nil can only come from a programmatic caller — and
	// for that caller "don't touch what I did not send" is the safe reading.
	// The column stays nullable for teams that predate migration 0004, not as a
	// state the admin UI produces. An unknown ageGroupID is refused in Persian.
	UpdateTeam(id int64, label, displayName string, ageGroupID *int64) error
	DeactivateTeam(id int64) error // blocked if active registrations exist

	// Competitions
	CreateCompetition(seasonID, ageGroupID int64, level string, groupName *string, displayName string) (int64, error)
	Competition(id int64) (*Competition, error)
	Competitions(seasonID, ageGroupID *int64) ([]Competition, error)
	// RenameCompetition: groupName applies to league1 only (premier must pass nil).
	// Trimmed only — official names stay exactly as typed (D7).
	RenameCompetition(id int64, displayName string, groupName *string) error
	// DeleteCompetition: refused while the competition has registrations or matches.
	DeleteCompetition(id int64) error

	// Registrations
	Register(competitionID, teamID int64) (int64, error) // premier club rule violations → «یک باشگاه در هر لیگ برتر فقط یک تیم می‌تواند داشته باشد» wrapped with names
	Unregister(registrationID int64) error               // blocked if the team has matches in that competition
	Registrations(competitionID int64) ([]Registration, error)
	RegisteredTeamIDs(competitionID int64) (map[int64]bool, error)

	// Matches
	CreateMatch(competitionID, homeTeamID, awayTeamID int64, week *int, scheduledDate, scheduledTime, venue *string) (int64, error)
	Match(id int64) (*Match, error)
	Matches(competitionID int64, week, status *string) ([]Match, error) // filters optional (week as string to keep signature simple; parse to int in SQL)
	SetResult(matchID int64, homeScore, awayScore int) error            // validates ≥0; only for scheduled matches
	ClearResult(matchID int64) error
	UpdateMatchSchedule(id int64, week *int, scheduledDate, scheduledTime, venue *string) error
	DeleteMatch(id int64) error
	MatchCount(competitionID int64) (total, finished int, err error)

	// Standings input helper
	FinishedMatches(competitionID int64) ([]FinishedMatch, error)

	// Audit
	AuditLog(limit int) ([]AuditEntry, error)
	// AuditLogFiltered returns matching audit rows newest-first plus the total
	// number of matching rows (for honest pagination). Empty action/entity
	// means "any"; both match exactly (the action vocabulary is a fixed small
	// set — no fuzzy/LIKE matching). Inputs are clamped so callers can rely on
	// them: limit <= 0 → 50, limit > 500 → 500, offset < 0 → 0. Ordering is
	// deterministic (id DESC, id being unique) so paging can neither drop nor
	// duplicate a row.
	AuditLogFiltered(action, entity string, limit, offset int) (entries []AuditEntry, total int, err error)

	// Backup
	BackupTo(destPath string) error // consistent snapshot; timestamped naming handled by caller
}

// FinishedMatch is the standings-engine input shape.
type FinishedMatch struct {
	HomeTeamID int64
	AwayTeamID int64
	HomeGoals  int
	AwayGoals  int
}
