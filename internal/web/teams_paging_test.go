// TASK-025 — the team list must not panic on a store-less server.
package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminTeamsListNilStoreRendersEmptyState(t *testing.T) {
	s := newTestServer(t) // nil store, as the foundation's tests build it
	session, _ := loginTestAdmin(t, s)

	rec := adminBrowse(s.Handler(), "/admin/teams", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil-store GET /admin/teams = %d, want 200 (a panic would not reach here)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="empty-state"`) {
		t.Error("nil-store team list must render the Persian empty state")
	}
	if !strings.Contains(body, "/admin/teams/new") {
		t.Error("nil-store team list must still offer the create link")
	}
}
