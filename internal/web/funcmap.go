// Package web hosts the HTTP layer: server construction, middleware, security
// (sessions/CSRF/login), template parsing, and the template funcmap.
//
// Wiring contract (TASK-005 templates + this package):
//   - web/templates/public_base.html defines the document skeleton with the
//     overridable blocks {{block "title"}} and {{block "content"}}.
//   - each page file (home, age_group, competition) overrides exactly those blocks.
//   - partials/*.html define named partial templates (match_row, standings_table).
//
// Because every page defines the same block names, pages must be parsed into
// SEPARATE clones of the base+partials set ("page templates"), never all at once.
package web

import (
	"html/template"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
)

// isoLayout is the canonical machine date format stored by the store layer (D9).
const isoLayout = "2006-01-02"

// templateFuncMap is the funcmap contract shared with the templates:
//
//	toFa       latin → Persian digits           (jalali.ToPersianDigits)
//	jalaliDate ISO «2006-01-02» → «۱۴۰۵/۰۷/۲۰»  (nil-safe, "" on nil/bad input)
//	jalaliLong ISO            → «۲۰ مهر ۱۴۰۵»   (nil-safe, "" on nil/bad input)
//
// Templates never do date math (DECISIONS.md D9); unparseable input is logged at
// debug level and renders as empty rather than breaking the page.
func templateFuncMap(log *slog.Logger) template.FuncMap {
	if log == nil {
		log = slog.Default()
	}
	render := func(format func(time.Time) string) func(*string) string {
		return func(iso *string) string {
			if iso == nil || *iso == "" {
				return ""
			}
			t, err := time.Parse(isoLayout, *iso)
			if err != nil {
				log.Debug("template: unparseable ISO date", "value", *iso, "error", err)
				return ""
			}
			return format(t)
		}
	}
	return template.FuncMap{
		"toFa":       jalali.ToPersianDigits,
		"jalaliDate": render(jalali.Format),
		"jalaliLong": render(jalali.FormatLong),
	}
}

// pageNames are the renderable public pages, each backed by its own template clone.
var pageNames = []string{"home", "age_group", "competition"}

// parseTemplates builds s.pages: one template set per page, each set containing
// the base + partials + that single page (so block overrides never collide).
// A parse error is returned so main can fail fast.
func (s *Server) parseTemplates() error {
	fsys := s.templateFS()
	funcs := templateFuncMap(s.log)

	base, err := template.New("base").Funcs(funcs).ParseFS(fsys, "public_base.html", "partials/*.html")
	if err != nil {
		return err
	}

	pages := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		clone, err := base.Clone()
		if err != nil {
			return err
		}
		parsed, err := clone.ParseFS(fsys, name+".html")
		if err != nil {
			return err
		}
		pages[name] = parsed
	}
	s.pages = pages
	return nil
}

// templateFS returns the templates filesystem (os.DirFS over s.templatesDir).
func (s *Server) templateFS() fs.FS {
	return os.DirFS(s.templatesDir)
}

// baseTemplateName is the template executed for a page render; the page's
// "title"/"content" overrides are resolved through the block definitions.
const baseTemplateName = "public_base.html"
