package web

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/shaiinarab/pabetoop-league/internal/site"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// SiteName is this deployment's display name, resolved once from the
// environment at process start (see internal/site).
//
// It is a variable rather than a constant so a fork re-brands itself with
// LEAGUE_NAME_FA and never has to touch Go source. Tests that need a different
// name call site.Set before constructing a Server.
var SiteName = site.Active().NameFA

// SiteSubtitle is the footer strapline, likewise environment-derived.
var SiteSubtitle = site.Active().Subtitle()

// refreshSiteIdentity re-resolves the exported identity values from the active
// profile. Production resolves them once at process start; tests call this
// after site.Set to simulate a differently-branded deployment.
//
// Note: the two inline error/login templates in security.go are parsed at
// package init, so they carry the name resolved at init time. Re-branding a
// running process is not a supported operation — set LEAGUE_NAME_FA before you
// start the binary.
func refreshSiteIdentity() {
	SiteName = site.Active().NameFA
	SiteSubtitle = site.Active().Subtitle()
}

// Config holds environment-derived settings (all optional; sane defaults for dev).
type Config struct {
	TemplatesDir string // default web/templates (env TEMPLATES_DIR)
	StaticDir    string // default web/static   (env STATIC_DIR)
	SecretFile   string // default data/secret.key (env SESSION_SECRET_FILE)
	Secret       string // optional explicit session secret (env SESSION_SECRET)
	AdminUser    string // default "admin" (env ADMIN_USER)
	AdminPass    string // env ADMIN_PASSWORD (plaintext, hashed at startup)
	AdminHash    string // env ADMIN_PASSWORD_HASH (bcrypt; wins over AdminPass)
}

// ConfigFromEnv reads the process environment into a Config.
func ConfigFromEnv() Config {
	return Config{
		TemplatesDir: envOr("TEMPLATES_DIR", "web/templates"),
		StaticDir:    envOr("STATIC_DIR", "web/static"),
		SecretFile:   envOr("SESSION_SECRET_FILE", filepath.Join("data", "secret.key")),
		Secret:       os.Getenv("SESSION_SECRET"),
		AdminUser:    envOr("ADMIN_USER", "admin"),
		AdminPass:    os.Getenv("ADMIN_PASSWORD"),
		AdminHash:    os.Getenv("ADMIN_PASSWORD_HASH"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Server is the HTTP application. It owns the store handle, the parsed page
// templates, and the security primitives (sessions, CSRF, login limiter).
type Server struct {
	store store.DataStore
	log   *slog.Logger

	cfg Config

	sessions *sessionManager
	limiter  *loginLimiter

	adminUser string
	adminHash []byte

	templatesDir string
	staticDir    string

	pages        map[string]*template.Template
	templatesErr error
}

// New builds a Server. Templates are parsed immediately; a parse error is kept
// in TemplateErr() so main can fail fast (the brief's log.Fatal contract).
// store may be nil: the foundation renders placeholders and performs no data
// access yet (TASK-004 wiring lands with the first data-backed handler).
func New(st store.DataStore) *Server {
	return NewWithConfig(st, ConfigFromEnv())
}

// NewWithConfig is New with explicit configuration (tests use it directly).
func NewWithConfig(st store.DataStore, cfg Config) *Server {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	secret, err := sessionSecret(cfg)
	if err != nil {
		log.Error("session secret", "error", err)
	}
	_ = secret // never logged

	adminHash := []byte(nil)
	switch {
	case cfg.AdminHash != "":
		adminHash = []byte(cfg.AdminHash) // already bcrypt
	case cfg.AdminPass != "":
		h, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPass), bcrypt.DefaultCost)
		if err != nil {
			log.Error("hash admin password", "error", err)
		} else {
			adminHash = h
		}
	default:
		log.Warn("ADMIN_PASSWORD is not set — admin login disabled (see internal/web/README.md)")
	}

	s := &Server{
		store:        st,
		log:          log,
		cfg:          cfg,
		sessions:     newSessionManager(secret),
		limiter:      newLoginLimiter(),
		adminUser:    cfg.AdminUser,
		adminHash:    adminHash,
		templatesDir: cfg.TemplatesDir,
		staticDir:    cfg.StaticDir,
	}
	if err := s.parseTemplates(); err != nil {
		s.templatesErr = err
	}
	return s
}

// TemplateErr exposes the startup template-parse error (nil when healthy).
func (s *Server) TemplateErr() error { return s.templatesErr }

// sessionSecret returns the HMAC secret from config, or loads/creates the
// persisted key file (0600). Tradeoff documented in internal/web/README.md:
// env var is preferred in production; the file keeps dev/self-hosted installs
// working across restarts without a config step.
func sessionSecret(cfg Config) ([]byte, error) {
	if cfg.Secret != "" {
		return []byte(cfg.Secret), nil
	}
	if b, err := os.ReadFile(cfg.SecretFile); err == nil && len(b) >= 16 {
		return b, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SecretFile), 0o755); err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	secret := []byte(hex.EncodeToString(raw))
	if err := os.WriteFile(cfg.SecretFile, secret, 0o600); err != nil {
		return nil, err
	}
	return secret, nil
}

// Handler builds the routing tree wrapped in the middleware chain.
func (s *Server) Handler() http.Handler {
	return chain(s.mux(),
		recoverer(s.log),
		securityHeaders,
		requestLogger(s.log),
		s.ensureCSRFCookie,
		s.csrfProtect,
	)
}

// mux assembles the routing tree: the single place the route set is defined.
//
// Handler() wraps it in the middleware chain; tests reach for mux() to assert
// the registered *patterns* directly ("which route answers this path"), with no
// middleware in front — csrfProtect would otherwise answer 403 for every POST
// before routing is consulted, making an unregistered POST indistinguishable
// from a registered one.
func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()

	// Public pages (TASK-013): anonymous, read-only, backed by the store.
	// RegisterPublicRoutes is the single registration site — the TASK-008
	// placeholder handlers it replaces are gone (duplicate patterns panic).
	s.RegisterPublicRoutes(mux)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	// Static assets with the cache policy from the brief.
	mux.Handle("GET /static/", staticHandler(s.staticDir))

	// Admin area: login is public, everything else requires the session.
	mux.HandleFunc("GET /admin/login", s.handleLoginGET)
	mux.HandleFunc("POST /admin/login", s.handleLoginPOST)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)
	mux.HandleFunc("GET /admin/logout", s.handleLogout)
	// Admin area (TASK-010): RegisterAdminRoutes registers /admin (+ /admin/{$}
	// as exact forms so there is no 307 hop) and every sub-page, each already
	// wrapped in requireAdmin there.
	s.RegisterAdminRoutes(mux)

	// Feature lanes own their own registration site; the Lead wires them here so
	// server.go stays the single place the route tree is assembled. Each
	// Register*Routes wraps its handlers in requireAdmin itself; registering a
	// route twice panics (http.ServeMux rejects duplicate patterns).
	s.RegisterResultRoutes(mux)        // TASK-011 — quick result entry
	s.RegisterBackupAuditRoutes(mux)   // TASK-014 — backup download + audit log
	s.RegisterCompetitionRoutes(mux)   // TASK-015 — competitions + registrations
	s.RegisterFixtureImportRoutes(mux) // TASK-012 — fixtures + import pipeline
	s.RegisterSeasonRoutes(mux)        // TASK-016 — seasons (root of every record)

	return mux
}

// ---------- public handlers (placeholders; templates already wired) ----------

// viewBase is the data every public page carries (public_base.html contract).
type viewBase struct {
	SiteName     string
	SiteSubtitle string
	ActiveSeason string
	AgeGroups    []ageGroupNav
	IsHome       bool

	// ActiveAgeID is the age group whose tab is current: the age-group page's
	// own id, or the age group a competition belongs to. 0 = none, which is the
	// home page. The nav renders `.age-tab.active` / `aria-current="page"` from
	// it (before this field existed the CSS had the class and no template ever
	// set it, so the tab bar never showed where you were).
	ActiveAgeID int64
}

type ageGroupNav struct {
	ID          int64
	DisplayName string
}

type homeView struct {
	viewBase
	LatestResults []matchRowView
	Upcoming      []matchRowView
	Stats         struct{ Clubs, Matches, Finished, Goals int }
}

type matchRowView struct {
	Match    store.Match
	ShowDate bool
}

type ageGroupView struct {
	viewBase
	AgeGroup store.AgeGroup
	Premier  *store.Competition
	League1  []store.Competition
}

type competitionView struct {
	viewBase
	Comp     store.Competition
	Tab      string
	Table    standingsView
	Results  []matchRowView
	Fixtures []matchRowView
}

type standingsView struct {
	CompetitionName string
	Rows            []emptyRow // placeholder: no data access yet
}

// emptyRow mirrors the shape the standings partial expects, without importing
// business data. It is replaced by []standing.Row when TASK-004 wiring lands.
type emptyRow struct {
	Rank                     int
	TeamName                 string
	Played, Won, Drawn, Lost int
	GoalsFor, GoalsAgainst   int
	GoalDiff, Points         int
}

// renderPage executes the page's template clone through the base skeleton.
//
// The TASK-008 placeholder handlers that used to live here (handleHome,
// handleAgeGroup, handleCompetition, handleAdminHome, adminTmpl) were deleted
// when the real public/admin handler layers were wired in; keeping them would
// have left two competing implementations of the same pages. The view types
// below are still the render data contracts the real handlers populate.
func (s *Server) renderPage(w http.ResponseWriter, page string, data any) {
	tmpl, ok := s.pages[page]
	if !ok || s.templatesErr != nil {
		s.log.Error("render page: templates unavailable", "page", page, "error", s.templatesErr)
		writePersianError(w, http.StatusInternalServerError,
			"قالب صفحه در دسترس نیست. لطفاً بعداً تلاش کنید.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, baseTemplateName, data); err != nil {
		s.log.Error("render page failed", "page", page, "error", err)
	}
}

// staticHandler serves web/static with the brief's cache policy: css/js get a
// short 5-minute TTL (they ship with deployments), everything else is
// fingerprinted-by-nature and cached immutably for a year.
func staticHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		switch {
		case strings.HasSuffix(name, ".css"), strings.HasSuffix(name, ".js"):
			w.Header().Set("Cache-Control", "public, max-age=300")
		default:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.StripPrefix("/static/", fileServer).ServeHTTP(w, r)
	})
}
