package server

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/config"
)

// testRouter builds the real router. The pool is nil: New only assembles
// handlers, and every request these tests make is answered by middleware before
// a handler can reach the database.
func testRouter(t *testing.T) chi.Routes {
	t.Helper()
	h, _, _, _, err := New(nil, &config.Config{
		EncryptionKey:  "test-key",
		JWTSecret:      "test-secret",
		APIToken:       "test-api-token",
		WebAuthnRPID:   "localhost",
		WebAuthnOrigin: "http://localhost",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi.Routes")
	}
	return routes
}

// walkRoutes returns every registered route as "METHOD /pattern".
func walkRoutes(t *testing.T, routes chi.Routes) []string {
	t.Helper()
	var found []string
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found = append(found, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(found)
	return found
}

// TestRoutes_Inventory pins the public surface. A route deleted by accident, or
// one added without being considered here, fails this test. Admin routes are
// covered by the admin package's own tests and are excluded so this list stays
// about the API.
func TestRoutes_Inventory(t *testing.T) {
	want := []string{
		"GET /healthz",

		"POST /api/v1/auth/login",
		"POST /api/v1/auth/logout",
		"GET /api/v1/auth/me",
		"POST /api/v1/auth/password",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/auth/register",

		"POST /api/v1/billing/checkout",
		"POST /api/v1/billing/portal",
		"GET /api/v1/billing/status",
		"POST /api/v1/stripe/webhook",

		"GET /api/v1/stations/",
		"POST /api/v1/stations/",
		"GET /api/v1/stations/genres",
		"GET /api/v1/stations/{slug}",
		"PUT /api/v1/stations/{stationID}",
		"DELETE /api/v1/stations/{stationID}",

		"GET /api/v1/user/tenants/",
		"POST /api/v1/user/tenants/",
		"GET /api/v1/user/tenants/{tenantID}",
		"DELETE /api/v1/user/tenants/{tenantID}",
		"POST /api/v1/user/tenants/{tenantID}/resume",
		"POST /api/v1/user/tenants/{tenantID}/sso-token",
		"POST /api/v1/user/tenants/{tenantID}/suspend",

		"GET /api/v1/nodes/",
		"POST /api/v1/nodes/",
		"GET /api/v1/nodes/{nodeID}",
		"PUT /api/v1/nodes/{nodeID}",
		"DELETE /api/v1/nodes/{nodeID}",

		"GET /api/v1/projects/",
		"POST /api/v1/projects/",
		"GET /api/v1/projects/{projectID}",
		"PUT /api/v1/projects/{projectID}",
		"DELETE /api/v1/projects/{projectID}",

		"GET /api/v1/tenants/",
		"POST /api/v1/tenants/",
		"GET /api/v1/tenants/{tenantID}",
		"PUT /api/v1/tenants/{tenantID}",
		"DELETE /api/v1/tenants/{tenantID}",
		"POST /api/v1/tenants/{tenantID}/resume",
		"POST /api/v1/tenants/{tenantID}/suspend",
	}
	sort.Strings(want)

	var got []string
	for _, route := range walkRoutes(t, testRouter(t)) {
		if !strings.Contains(route, " /admin") {
			got = append(got, route)
		}
	}

	missing, unexpected := diff(want, got)
	for _, r := range missing {
		t.Errorf("route is gone: %s", r)
	}
	for _, r := range unexpected {
		t.Errorf("route added without being listed here: %s", r)
	}
}

func diff(want, got []string) (missing, unexpected []string) {
	inGot := make(map[string]bool, len(got))
	for _, g := range got {
		inGot[g] = true
	}
	inWant := make(map[string]bool, len(want))
	for _, w := range want {
		inWant[w] = true
	}
	for _, w := range want {
		if !inGot[w] {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !inWant[g] {
			unexpected = append(unexpected, g)
		}
	}
	return missing, unexpected
}

// TestRoutes_ProtectedRoutesRejectAnonymous is the guard against a route
// escaping its auth group. Membership is asserted by behaviour rather than by
// counting middleware, because a count says nothing about which middleware ran.
// An unauthenticated request must be refused before it reaches a handler, which
// is also why a nil database pool is safe here.
func TestRoutes_ProtectedRoutesRejectAnonymous(t *testing.T) {
	routes := testRouter(t)

	protected := []struct {
		method string
		path   string
	}{
		// Bearer token group.
		{http.MethodGet, "/api/v1/nodes/"},
		{http.MethodPost, "/api/v1/nodes/"},
		{http.MethodGet, "/api/v1/projects/"},
		{http.MethodPost, "/api/v1/projects/"},
		{http.MethodGet, "/api/v1/tenants/"},
		{http.MethodPost, "/api/v1/tenants/"},
		{http.MethodDelete, "/api/v1/tenants/11111111-1111-1111-1111-111111111111"},
		// JWT group.
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodPost, "/api/v1/auth/password"},
		{http.MethodGet, "/api/v1/user/tenants/"},
		{http.MethodPost, "/api/v1/user/tenants/"},
		// Billing, JWT group.
		{http.MethodPost, "/api/v1/billing/checkout"},
		{http.MethodPost, "/api/v1/billing/portal"},
		{http.MethodGet, "/api/v1/billing/status"},
	}

	for _, route := range protected {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			routes.(http.Handler).ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d: this route is not behind its auth middleware",
					rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestRoutes_PublicRoutesAreReachable pins the other side, so the test above
// cannot be satisfied by putting everything behind auth.
func TestRoutes_PublicRoutesAreReachable(t *testing.T) {
	routes := testRouter(t)

	rec := httptest.NewRecorder()
	routes.(http.Handler).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", strings.NewReader("{}")))

	// Billing is unconfigured in this router, so the handler answers 501. The
	// point is that it answered at all: no auth middleware turned it away.
	if rec.Code == http.StatusUnauthorized {
		t.Error("the Stripe webhook must not sit behind authentication: Stripe cannot authenticate")
	}
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d from an unconfigured billing handler", rec.Code, http.StatusNotImplemented)
	}
}

// TestRoutes_RateLimitedGroups is the guard against a route escaping its
// throttle group. Each group is hammered past its own limit through one route,
// and the group is proven to be limited by the arrival of a 429.
func TestRoutes_RateLimitedGroups(t *testing.T) {
	groups := []struct {
		name   string
		method string
		path   string
		limit  int
	}{
		{name: "auth", method: http.MethodPost, path: "/api/v1/auth/login", limit: 10},
		{name: "user tenants", method: http.MethodGet, path: "/api/v1/user/tenants/", limit: 20},
		{name: "billing", method: http.MethodGet, path: "/api/v1/billing/status", limit: 20},
		{name: "stations", method: http.MethodGet, path: "/api/v1/stations/genres", limit: 100},
		{name: "api v1", method: http.MethodGet, path: "/api/v1/nodes/", limit: 100},
	}

	for _, group := range groups {
		t.Run(group.name, func(t *testing.T) {
			// A fresh router per group: httprate counts per limiter instance.
			routes := testRouter(t)

			var sawLimit bool
			for i := 0; i < group.limit+5; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(group.method, group.path, nil)
				req.RemoteAddr = "192.0.2.10:1234"
				routes.(http.Handler).ServeHTTP(rec, req)
				if rec.Code == http.StatusTooManyRequests {
					sawLimit = true
					break
				}
			}

			if !sawLimit {
				t.Errorf("no 429 after %d requests: this route is not inside a rate-limited group", group.limit+5)
			}
		})
	}
}

// TestRoutes_BillingIsRateLimited states the specific gap this closed. The
// billing group was the only one without a limit, and every call in it reaches
// out to Stripe.
func TestRoutes_BillingIsRateLimited(t *testing.T) {
	routes := testRouter(t)

	var codes []int
	for i := 0; i < 25; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/status", nil)
		req.RemoteAddr = "192.0.2.11:1234"
		routes.(http.Handler).ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}

	if codes[len(codes)-1] != http.StatusTooManyRequests {
		t.Errorf("last of 25 requests returned %d, want %d", codes[len(codes)-1], http.StatusTooManyRequests)
	}
	// Before the limit is reached the auth middleware answers, which confirms
	// the rate limiter runs ahead of it rather than replacing it.
	if codes[0] != http.StatusUnauthorized {
		t.Errorf("first request returned %d, want %d from the JWT middleware", codes[0], http.StatusUnauthorized)
	}
}

// TestRoutes_WebhookIsNotRateLimited pins that the Stripe webhook stays outside
// the throttled group. Stripe retries a 429 and gives up eventually, so a limit
// here would drop payment events under exactly the burst it is meant to survive.
func TestRoutes_WebhookIsNotRateLimited(t *testing.T) {
	routes := testRouter(t)

	for i := 0; i < 40; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", strings.NewReader("{}"))
		req.RemoteAddr = "192.0.2.12:1234"
		routes.(http.Handler).ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was rate limited; Stripe deliveries must not be throttled", i+1)
		}
	}
}

// TestRoutes_HealthzIsAnonymous keeps the probe endpoint reachable, since a
// health check that needs credentials is not a health check.
func TestRoutes_HealthzIsAnonymous(t *testing.T) {
	routes := testRouter(t)

	rec := httptest.NewRecorder()
	routes.(http.Handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusTooManyRequests {
		t.Errorf("status = %d: /healthz must answer without credentials", rec.Code)
	}
}
