package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shaiinarab/pabetoop-league/internal/site"
)

// legacyIdentity are strings that belonged to the reference deployment this
// template was extracted from — its city, its league name, and the real clubs
// that appeared in its seed data and fixtures. None of them may ever render
// again. A template that ships a former client's league is the exact failure
// mode this test exists to prevent.
var legacyIdentity = []string{
	"شیراز",
	"Shiraz",
	"shiraz",
	"استقلال",
	"پرسپولیس",
	"سپاهان",
	"فجر",
	"سپاسی",
	"آرارت",
	"کوهسار",
	"برشا",
}

// renderedSurfaces walks the routes a visitor or admin can reach and returns
// each response body so a leak is attributable to a specific page.
func renderedSurfaces(t *testing.T, s *Server) map[string]string {
	t.Helper()
	routes := map[string]string{
		"public home": "/",
		"healthz":     "/healthz",
		"admin login": adminLoginPath,
		"not found":   "/no-such-page",
	}
	out := map[string]string{}
	for name, path := range routes {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		out[name] = rec.Body.String()
	}
	return out
}

func TestRenderedPagesCarryNoLegacyIdentity(t *testing.T) {
	// Pin the profile so this test does not depend on the ambient environment,
	// then restore it for the rest of the suite.
	original := site.Active()
	t.Cleanup(func() { site.Set(original) })
	refreshSiteIdentity()

	for name, body := range renderedSurfaces(t, newTestServer(t)) {
		for _, legacy := range legacyIdentity {
			if strings.Contains(body, legacy) {
				t.Errorf("%s leaks legacy identity %q", name, legacy)
			}
		}
	}
}

// TestPagesFollowActiveSite proves the pages are genuinely white-label rather
// than merely stripped of one client's name.
func TestPagesFollowActiveSite(t *testing.T) {
	original := site.Active()
	t.Cleanup(func() { site.Set(original); refreshSiteIdentity() })

	site.Set(site.Profile{
		Name:         "Riverside Youth League",
		NameFA:       "لیگ نوجوانان رودخانه",
		DisciplineFA: "فوتسال",
	})
	refreshSiteIdentity()

	if SiteName != "لیگ نوجوانان رودخانه" {
		t.Fatalf("SiteName = %q, want the newly active name", SiteName)
	}
	if got := SiteSubtitle; !strings.Contains(got, "فوتسال") {
		t.Fatalf("SiteSubtitle = %q, want it derived from the discipline", got)
	}

	bodies := renderedSurfaces(t, newTestServer(t))
	for _, name := range []string{"public home", "admin login"} {
		if !strings.Contains(bodies[name], "لیگ نوجوانان رودخانه") {
			t.Errorf("%s does not render the active site name", name)
		}
	}
	if !strings.Contains(bodies["public home"], "فوتسال") {
		t.Error("public home does not render the derived subtitle")
	}
}

// TestSiteSubtitleCollapses keeps the derived strapline honest when a
// deployment blanks the optional field.
func TestSiteSubtitleCollapses(t *testing.T) {
	bare := site.Profile{NameFA: "لیگ نمونه"}
	if got := bare.Subtitle(); got == "" || strings.Contains(got, "  ") {
		t.Errorf("Subtitle with no discipline = %q, want a clean generic strapline", got)
	}
	explicit := site.Profile{DisciplineFA: "فوتبال", SubtitleFA: "متن دلخواه"}
	if got := explicit.Subtitle(); got != "متن دلخواه" {
		t.Errorf("Subtitle = %q, want the explicit override", got)
	}
}
