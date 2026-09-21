// TASK-038 — ROUTES.md is a document, not an assertion.
//
// ROUTES.md is the authoritative route table: nine registration sites, ~48
// documented rows. Nothing checked it against the code, so a documented route
// could 404 for a user and a Register*Routes method could be defined and never
// called while `go build`, `go vet` and every other test stayed green. TASK-012
// was exactly that failure: RegisterFixtureImportRoutes existed and was not
// wired, and its whole route surface was silently absent.
//
// The tree is reached through s.mux(), never s.Handler(): Handler() wraps the
// mux in csrfProtect, which answers 403 for every POST *before* routing is
// consulted, so through Handler() an unregistered POST is indistinguishable
// from a registered one.
package web

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// routeProbe is one documented route, plus the concrete request that asks the
// router about it.
type routeProbe struct {
	Pattern string // the registered pattern the doc claims, e.g. `GET /admin/teams/{id}/edit`
	Method  string
	Path    string // Pattern with every wildcard substituted for a concrete value
	Absent  bool   // struck through in the doc: documented as NOT a route
}

var (
	docBackticks = regexp.MustCompile("`([^`]+)`")
	docMethodPat = regexp.MustCompile(`^([A-Z]+(?:,[A-Z]+)*)\s+(/\S*)$`)
	wildcard     = regexp.MustCompile(`\{[^}]*\}`)
	registrarDef = regexp.MustCompile(`func \(s \*Server\) (Register\w+Routes)\(`)
	registrarCal = regexp.MustCompile(`s\.(Register\w+Routes)\(`)
)

// documentedRoutes parses ROUTES.md's tables into probes. It must fail loudly
// rather than return an empty slice: a parser that silently matches nothing
// turns this test into a green no-op, which is the class of bug it exists to
// catch.
func documentedRoutes(t *testing.T) []routeProbe {
	t.Helper()
	raw, err := os.ReadFile("../../ROUTES.md")
	if err != nil {
		t.Fatalf("read ROUTES.md: %v", err)
	}

	var probes []routeProbe
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		cell := cells[1] // the "Method + pattern" column
		// A struck-through row is the doc stating a route deliberately does not
		// exist (e.g. seasons are never deleted). It must stay absent.
		absent := strings.Contains(cell, "~~")
		for _, m := range docBackticks.FindAllStringSubmatch(cell, -1) {
			spec := strings.TrimSpace(m[1])
			// A documented `?query` is a note about the route, not part of the
			// registered pattern (Go patterns never contain a query string).
			spec = strings.SplitN(spec, "?", 2)[0]
			mp := docMethodPat.FindStringSubmatch(spec)
			if mp == nil {
				continue // prose, a handler name, a `{tab}` value list, …
			}
			for _, method := range strings.Split(mp[1], ",") {
				probes = append(probes, routeProbe{
					Pattern: method + " " + mp[2],
					Method:  method,
					Path:    probePath(mp[2]),
					Absent:  absent,
				})
			}
		}
	}
	if len(probes) < 30 {
		t.Fatalf("parsed only %d documented routes from ROUTES.md — the parser is broken, "+
			"so a passing run would prove nothing", len(probes))
	}
	return probes
}

// probePath turns a registered pattern into a concrete request path.
func probePath(pattern string) string {
	return wildcard.ReplaceAllStringFunc(pattern, func(w string) string {
		switch w {
		case "{$}":
			return "" // `GET /admin/{$}` is /admin/ — the exact form, no 307 hop
		case "{tab}":
			return "table"
		default:
			return "1"
		}
	})
}

func TestRoutesDocumentedAreRegistered(t *testing.T) {
	mux := newTestServer(t).mux()
	probes := documentedRoutes(t)

	for _, p := range probes {
		_, pattern := mux.Handler(httptest.NewRequest(p.Method, p.Path, nil))
		if p.Absent {
			if pattern != "" {
				t.Errorf("ROUTES.md documents %q as a route that does NOT exist, but the router answered %q",
					p.Pattern, pattern)
			}
			continue
		}
		if pattern != p.Pattern {
			t.Errorf("ROUTES.md documents %q, but the router answered %q for %s %s — "+
				"a documented route is missing or registered under a different pattern",
				p.Pattern, pattern, p.Method, p.Path)
		}
	}
}

// TestEveryRegistrarIsCalledFromTheRoutingTree is the other direction: a
// `Register…Routes` method that is defined but never called compiles, vets and
// passes every other test while its entire route surface disappears (TASK-012).
// The tree is assembled in server.go's mux(), so that is the one call site.
func TestEveryRegistrarIsCalledFromTheRoutingTree(t *testing.T) {
	defined := registrarNames(t, false)
	called := registrarNames(t, true)

	if len(defined) < 5 {
		t.Fatalf("found only %d Register*Routes methods — the parser is broken", len(defined))
	}
	for name := range defined {
		if !called[name] {
			t.Errorf("(*Server).%s is defined but never called from the routing tree in server.go — "+
				"its routes are silently absent (this is the TASK-012 bug)", name)
		}
	}
	// And the reverse: a call to a method that no longer exists would not
	// compile, but a call from somewhere other than the routing tree is worth
	// knowing about, since then the tree is not assembled in one place.
	for name := range called {
		if !defined[name] {
			t.Errorf("server.go calls s.%s(...) but no such method is defined in this package", name)
		}
	}
}

// registrarNames returns the Register*Routes method names found in this
// package's non-test sources. With calls=true it returns the names invoked from
// server.go instead (the routing tree's single assembly point).
func registrarNames(t *testing.T, calls bool) map[string]bool {
	t.Helper()
	names := map[string]bool{}

	if calls {
		raw, err := os.ReadFile("server.go")
		if err != nil {
			t.Fatalf("read server.go: %v", err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			// A commented-out call is not a call; server.go's prose names these
			// methods on purpose.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range registrarCal.FindAllStringSubmatch(line, -1) {
				names[m[1]] = true
			}
		}
		return names
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range registrarDef.FindAllStringSubmatch(string(raw), -1) {
			names[m[1]] = true
		}
	}
	return names
}
