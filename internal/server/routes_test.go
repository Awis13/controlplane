package server

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"controlplane/internal/config"
)

// access is how a route is reached: which credential, if any, gets past the
// middleware in front of it.
type access int

const (
	// public answers without credentials: health probes, the Stripe webhook,
	// which cannot authenticate as a user, and the station reads the
	// listener-facing site serves.
	public access = iota
	// bearer requires the API token, for the operator-facing API.
	bearer
	// jwt requires a user session.
	jwt
)

func (a access) String() string {
	switch a {
	case public:
		return "public"
	case bearer:
		return "bearer"
	default:
		return "jwt"
	}
}

// route is one entry in the table below.
type route struct {
	method string
	// pattern is the chi pattern, which is what the router reports.
	pattern string
	// path is a concrete request path; empty means the pattern has no
	// parameters and can be requested as it stands.
	path   string
	access access
}

func (r route) requestPath() string {
	if r.path != "" {
		return r.path
	}
	return r.pattern
}

const someUUID = "11111111-1111-1111-1111-111111111111"

// apiRoutes is the single source of truth for this file. Both the inventory
// assertion and the credential sweep are derived from it, so a route that
// changes group fails the sweep and a route added or removed fails the
// inventory. A new route cannot pass without being classified here.
//
// Admin routes are excluded and are not pinned anywhere: the admin package
// tests its handlers, but nothing asserts its route table. That is a gap worth
// its own ticket rather than something to bolt on here.
//
// The WireGuard group only registers when WG_HUB_PUBLIC_KEY is set, so the test
// config sets it. Without it the router has 41 routes rather than 50 and the
// nine below would silently not exist.
var apiRoutes = []route{
	{method: "GET", pattern: "/healthz", access: public},
	{method: "POST", pattern: "/api/v1/stripe/webhook", access: public},

	// Auth. Login, register and refresh are how a caller obtains a session, so
	// they cannot require one.
	{method: "POST", pattern: "/api/v1/auth/login", access: public},
	{method: "POST", pattern: "/api/v1/auth/register", access: public},
	{method: "POST", pattern: "/api/v1/auth/refresh", access: public},
	{method: "POST", pattern: "/api/v1/auth/logout", access: jwt},
	{method: "GET", pattern: "/api/v1/auth/me", access: jwt},
	{method: "POST", pattern: "/api/v1/auth/password", access: jwt},

	// Stations. Reads are public because the listener-facing site serves them.
	// Writes take the API token, not a user session: this group is operator
	// tooling despite sitting next to the public reads.
	{method: "GET", pattern: "/api/v1/stations/", access: public},
	{method: "GET", pattern: "/api/v1/stations/genres", access: public},
	{method: "GET", pattern: "/api/v1/stations/{slug}", path: "/api/v1/stations/some-slug", access: public},
	{method: "POST", pattern: "/api/v1/stations/", access: bearer},
	{method: "PUT", pattern: "/api/v1/stations/{stationID}", path: "/api/v1/stations/" + someUUID, access: bearer},
	{method: "DELETE", pattern: "/api/v1/stations/{stationID}", path: "/api/v1/stations/" + someUUID, access: bearer},

	// Billing, session-scoped.
	{method: "POST", pattern: "/api/v1/billing/checkout", access: jwt},
	{method: "POST", pattern: "/api/v1/billing/portal", access: jwt},
	{method: "GET", pattern: "/api/v1/billing/status", access: jwt},

	// A user's own tenants.
	{method: "GET", pattern: "/api/v1/user/tenants/", access: jwt},
	{method: "POST", pattern: "/api/v1/user/tenants/", access: jwt},
	{method: "GET", pattern: "/api/v1/user/tenants/{tenantID}", path: "/api/v1/user/tenants/" + someUUID, access: jwt},
	{method: "DELETE", pattern: "/api/v1/user/tenants/{tenantID}", path: "/api/v1/user/tenants/" + someUUID, access: jwt},
	{method: "POST", pattern: "/api/v1/user/tenants/{tenantID}/resume", path: "/api/v1/user/tenants/" + someUUID + "/resume", access: jwt},
	{method: "POST", pattern: "/api/v1/user/tenants/{tenantID}/suspend", path: "/api/v1/user/tenants/" + someUUID + "/suspend", access: jwt},
	{method: "POST", pattern: "/api/v1/user/tenants/{tenantID}/sso-token", path: "/api/v1/user/tenants/" + someUUID + "/sso-token", access: jwt},

	// Operator API, bearer token.
	{method: "GET", pattern: "/api/v1/nodes/", access: bearer},
	{method: "POST", pattern: "/api/v1/nodes/", access: bearer},
	{method: "GET", pattern: "/api/v1/nodes/{nodeID}", path: "/api/v1/nodes/" + someUUID, access: bearer},
	{method: "PUT", pattern: "/api/v1/nodes/{nodeID}", path: "/api/v1/nodes/" + someUUID, access: bearer},
	{method: "DELETE", pattern: "/api/v1/nodes/{nodeID}", path: "/api/v1/nodes/" + someUUID, access: bearer},

	{method: "GET", pattern: "/api/v1/projects/", access: bearer},
	{method: "POST", pattern: "/api/v1/projects/", access: bearer},
	{method: "GET", pattern: "/api/v1/projects/{projectID}", path: "/api/v1/projects/" + someUUID, access: bearer},
	{method: "PUT", pattern: "/api/v1/projects/{projectID}", path: "/api/v1/projects/" + someUUID, access: bearer},
	{method: "DELETE", pattern: "/api/v1/projects/{projectID}", path: "/api/v1/projects/" + someUUID, access: bearer},

	{method: "GET", pattern: "/api/v1/tenants/", access: bearer},
	{method: "POST", pattern: "/api/v1/tenants/", access: bearer},
	{method: "GET", pattern: "/api/v1/tenants/{tenantID}", path: "/api/v1/tenants/" + someUUID, access: bearer},
	{method: "PUT", pattern: "/api/v1/tenants/{tenantID}", path: "/api/v1/tenants/" + someUUID, access: bearer},
	{method: "DELETE", pattern: "/api/v1/tenants/{tenantID}", path: "/api/v1/tenants/" + someUUID, access: bearer},
	{method: "POST", pattern: "/api/v1/tenants/{tenantID}/resume", path: "/api/v1/tenants/" + someUUID + "/resume", access: bearer},
	{method: "POST", pattern: "/api/v1/tenants/{tenantID}/suspend", path: "/api/v1/tenants/" + someUUID + "/suspend", access: bearer},

	{method: "GET", pattern: "/api/v1/wireguard/peers", access: bearer},
	{method: "POST", pattern: "/api/v1/wireguard/peers", access: bearer},
	{method: "GET", pattern: "/api/v1/wireguard/peers/{id}", path: "/api/v1/wireguard/peers/" + someUUID, access: bearer},
	{method: "PUT", pattern: "/api/v1/wireguard/peers/{id}", path: "/api/v1/wireguard/peers/" + someUUID, access: bearer},
	{method: "DELETE", pattern: "/api/v1/wireguard/peers/{id}", path: "/api/v1/wireguard/peers/" + someUUID, access: bearer},
	{method: "POST", pattern: "/api/v1/wireguard/peers/{id}/enable", path: "/api/v1/wireguard/peers/" + someUUID + "/enable", access: bearer},
	{method: "POST", pattern: "/api/v1/wireguard/peers/{id}/disable", path: "/api/v1/wireguard/peers/" + someUUID + "/disable", access: bearer},
	{method: "GET", pattern: "/api/v1/wireguard/peers/{id}/config", path: "/api/v1/wireguard/peers/" + someUUID + "/config", access: bearer},
	{method: "GET", pattern: "/api/v1/wireguard/peers/{id}/qr", path: "/api/v1/wireguard/peers/" + someUUID + "/qr", access: bearer},
}

// testRouter builds the real router.
//
// The pool is real but points at a port nothing listens on. pgxpool connects
// lazily, so construction succeeds and any query fails at request time. A nil
// pool is not usable here: enabling WireGuard makes New start a goroutine that
// immediately queries for peers, and a nil pool turns that into a nil pointer
// dereference which takes the test binary down with it.
//
// No request in this file reaches a database. Every credential assertion is
// answered by middleware, and the public routes either need no data or fail
// with a server error, which is still not a 401.
func testRouter(t *testing.T) chi.Routes {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	h, _, _, _, err := New(pool, &config.Config{
		EncryptionKey:  "test-key",
		JWTSecret:      "test-secret",
		APIToken:       "test-api-token",
		WebAuthnRPID:   "localhost",
		WebAuthnOrigin: "http://localhost",
		// Registers the WireGuard group; without it those nine routes are absent.
		WGHubPublicKey: "test-hub-public-key",
		WGNetworkCIDR:  "10.10.0.0/24",
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

func serve(t *testing.T, routes chi.Routes, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.1:1234"
	routes.(http.Handler).ServeHTTP(rec, req)
	return rec
}

// walkAPIRoutes returns every non-admin route the router actually has.
func walkAPIRoutes(t *testing.T, routes chi.Routes) []string {
	t.Helper()
	var found []string
	err := chi.Walk(routes, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(pattern, "/admin") {
			found = append(found, method+" "+pattern)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(found)
	return found
}

// TestRoutes_Inventory fails when a route is added or removed without being
// classified in the table.
func TestRoutes_Inventory(t *testing.T) {
	var want []string
	for _, r := range apiRoutes {
		want = append(want, r.method+" "+r.pattern)
	}
	sort.Strings(want)

	got := walkAPIRoutes(t, testRouter(t))

	inGot := map[string]bool{}
	for _, g := range got {
		inGot[g] = true
	}
	inWant := map[string]bool{}
	for _, w := range want {
		inWant[w] = true
	}
	for _, w := range want {
		if !inGot[w] {
			t.Errorf("route in the table but not in the router: %s", w)
		}
	}
	for _, g := range got {
		if !inWant[g] {
			t.Errorf("route in the router but not classified in the table: %s", g)
		}
	}
	if len(got) != len(apiRoutes) {
		t.Errorf("router has %d API routes, the table has %d", len(got), len(apiRoutes))
	}
}

// TestRoutes_CredentialsAreRequired sweeps every route in the table rather than
// a chosen subset, which is the point: a route that slips out of its auth group
// only fails a test if every route is checked. A non-public route must refuse an
// anonymous caller, and a public one must not.
func TestRoutes_CredentialsAreRequired(t *testing.T) {
	routes := testRouter(t)

	for _, r := range apiRoutes {
		t.Run(r.access.String()+" "+r.method+" "+r.pattern, func(t *testing.T) {
			rec := serve(t, routes, r.method, r.requestPath())

			if r.access == public {
				if rec.Code == http.StatusUnauthorized {
					t.Error("status = 401: this route is classified public but demands a credential")
				}
				return
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: this route is classified %s but answered an anonymous caller",
					rec.Code, r.access)
			}
		})
	}
}

// TestRoutes_CredentialKindIsCorrect makes the bearer and jwt labels mean
// something. The sweep above only proves a route refuses an anonymous caller,
// which both mechanisms do identically, so a route labelled jwt while actually
// sitting behind the API token would pass it. Presenting the API token
// separates them: a bearer route lets it through, and a jwt route still refuses
// it, because an opaque token is not a signed session.
func TestRoutes_CredentialKindIsCorrect(t *testing.T) {
	routes := testRouter(t)

	for _, r := range apiRoutes {
		if r.access == public {
			continue
		}
		t.Run(r.access.String()+" "+r.method+" "+r.pattern, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(r.method, r.requestPath(), nil)
			req.RemoteAddr = "192.0.2.2:1234"
			req.Header.Set("Authorization", "Bearer test-api-token")
			routes.(http.Handler).ServeHTTP(rec, req)

			if r.access == bearer && rec.Code == http.StatusUnauthorized {
				t.Error("the API token was refused: this route is not behind bearerAuth")
			}
			if r.access == jwt && rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d: the API token got past a route that should demand a session", rec.Code)
			}
		})
	}
}

// TestRoutes_PublicRoutesAreReachable exercises every public route, so the
// sweep above cannot be satisfied by putting everything behind authentication.
//
// There are eight, not the five that are obvious: healthz, the Stripe webhook
// and three station reads, plus login, register and refresh. Those last three
// are how a caller obtains a session in the first place, so they cannot demand
// one, and they are easy to overlook when counting what is exposed.
func TestRoutes_PublicRoutesAreReachable(t *testing.T) {
	routes := testRouter(t)

	var checked int
	for _, r := range apiRoutes {
		if r.access != public {
			continue
		}
		checked++
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			rec := serve(t, routes, r.method, r.requestPath())

			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Errorf("status = %d: this route must answer without credentials", rec.Code)
			}
		})
	}
	if checked != 8 {
		t.Errorf("checked %d public routes, want 8: healthz, the Stripe webhook, three station reads and the three auth entry points", checked)
	}
}

// TestRoutes_RateLimitedGroups is the guard against a route escaping its
// throttle group. Each group is driven past its own limit through one of its
// routes, and membership is proven by the arrival of a 429.
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
				if serve(t, routes, group.method, group.path).Code == http.StatusTooManyRequests {
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

// TestRoutes_BillingIsRateLimited states the specific gap CP-T12 closed. The
// billing group was the only one without a limit, and every call in it reaches
// out to Stripe.
func TestRoutes_BillingIsRateLimited(t *testing.T) {
	routes := testRouter(t)

	var codes []int
	for i := 0; i < 25; i++ {
		codes = append(codes, serve(t, routes, http.MethodGet, "/api/v1/billing/status").Code)
	}

	if codes[len(codes)-1] != http.StatusTooManyRequests {
		t.Errorf("last of 25 requests returned %d, want 429", codes[len(codes)-1])
	}
	// Before the limit is reached the auth middleware answers, which confirms
	// the limiter runs ahead of it rather than replacing it.
	if codes[0] != http.StatusUnauthorized {
		t.Errorf("first request returned %d, want 401 from the JWT middleware", codes[0])
	}
}

// TestRoutes_WebhookIsNotRateLimited pins that the Stripe webhook stays outside
// the throttled group. Stripe retries a 429 and gives up eventually, so a limit
// here would drop payment events under exactly the burst it is meant to survive.
func TestRoutes_WebhookIsNotRateLimited(t *testing.T) {
	routes := testRouter(t)

	for i := 0; i < 40; i++ {
		if serve(t, routes, http.MethodPost, "/api/v1/stripe/webhook").Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was rate limited; Stripe deliveries must not be throttled", i+1)
		}
	}
}
