package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// promisedSecurityHeaders is the header contract SECURITY.md §2 promises on
// every response, written out literally and on purpose.
//
// It is deliberately NOT a mirror of baselineSecurityHeaders(): the bug TASK-022
// closed was not "a header went missing", it was "nothing noticed a header was
// missing". If the expected values were read from the same list the middleware
// sends, deleting a header would delete its own expectation and the suite would
// stay green. Written out here, deleting or weakening a header fails a test.
//
// Changing the contract means changing SECURITY.md §2 and this table together.
var promisedSecurityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "no-referrer",
	"Content-Security-Policy": "default-src 'self'; style-src 'self'; script-src 'self'; " +
		"img-src 'self' data:; frame-ancestors 'none'",
	"Permissions-Policy": "accelerometer=(), autoplay=(), camera=(), display-capture=(), " +
		"encrypted-media=(), fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), " +
		"microphone=(), midi=(), payment=(), picture-in-picture=(), " +
		"publickey-credentials-get=(), screen-wake-lock=(), serial=(), usb=(), " +
		"xr-spatial-tracking=()",
}

// capabilitiesThisProductDoesNotUse must appear as an explicit empty allow-list
// in Permissions-Policy: the app has no media, sensors or device APIs (D1–D5).
var capabilitiesThisProductDoesNotUse = []string{
	"camera", "microphone", "geolocation", "display-capture",
	"usb", "serial", "payment", "xr-spatial-tracking",
}

// knownSecurityHeaderNames are the response headers a reviewer would expect to
// find decided one way or the other. Anything set on a response that is in this
// list but not in the contract is drift: it shipped without being documented.
var knownSecurityHeaderNames = []string{
	"X-Content-Type-Options",
	"X-Frame-Options",
	"Referrer-Policy",
	"Content-Security-Policy",
	"Permissions-Policy",
	"Cross-Origin-Opener-Policy",
	"Cross-Origin-Embedder-Policy",
	"Cross-Origin-Resource-Policy",
	"X-Permitted-Cross-Domain-Policies",
	"X-XSS-Protection",
}

// assertBaselineHeaders checks the complete protocol-independent contract.
func assertBaselineHeaders(t *testing.T, h http.Header) {
	t.Helper()
	for name, want := range promisedSecurityHeaders {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestSecurityHeadersOnEveryResponse pins the promise "on every response": the
// middleware wraps the whole tree, so a 404 and the admin login page must carry
// the same headers as the public home page. Responses are the only place this
// is observable — go build and go vet cannot see a missing header.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newTestServer(t).Handler()
	cases := []struct{ name, path string }{
		{"public home", "/"},
		{"admin login", "/admin/login"},
		{"unknown route", "/no-such-route-t022"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			assertBaselineHeaders(t, rec.Header())

			// HSTS is TLS-only by decision (SECURITY.md §2): over plain HTTP it
			// must be absent, not merely different.
			if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
				t.Errorf("Strict-Transport-Security = %q over plain HTTP, want unset", got)
			}
		})
	}
}

// TestSecurityHeadersHSTSOnlyOverTLS documents the deliberate HSTS decision as
// an executable expectation, so the next reader cannot "fix" the TLS gate by
// accident without a red test.
func TestSecurityHeadersHSTSOnlyOverTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	newTestServer(t).Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != hstsValue {
		t.Errorf("Strict-Transport-Security over TLS = %q, want %q", got, hstsValue)
	}
	assertBaselineHeaders(t, rec.Header())
}

// TestPermissionsPolicyIsExplicitDeny asserts the policy denies the device APIs
// this product never uses, rather than relying on an empty or wildcard value —
// a feature left unmentioned keeps its default allow-list.
func TestPermissionsPolicyIsExplicitDeny(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	policy := rec.Header().Get("Permissions-Policy")

	if policy == "" {
		t.Fatal("Permissions-Policy is not set: the policy must be explicit, not absent")
	}
	if strings.Contains(policy, "*") {
		t.Errorf("Permissions-Policy must not use a wildcard allow-list: %q", policy)
	}
	if !strings.HasSuffix(strings.TrimSpace(policy), ")") {
		t.Errorf("Permissions-Policy looks malformed: %q", policy)
	}
	for _, capability := range capabilitiesThisProductDoesNotUse {
		if !strings.Contains(policy, capability+"=()") {
			t.Errorf("Permissions-Policy must deny %s explicitly: %q", capability, policy)
		}
	}
}

// TestNoUndocumentedSecurityHeaders is the other direction of the contract: a
// header that appears out of nowhere is as much of a problem as one that
// disappears, because SECURITY.md would stop describing the running product.
func TestNoUndocumentedSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	h := rec.Header()

	for _, name := range knownSecurityHeaderNames {
		if h.Get(name) == "" {
			continue
		}
		if _, documented := promisedSecurityHeaders[name]; documented {
			continue
		}
		t.Errorf("%s is sent but is not part of the documented contract "+
			"(add it to SECURITY.md §2 and promisedSecurityHeaders, or stop sending it)", name)
	}
}
