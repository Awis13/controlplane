package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouteID(t *testing.T) {
	tests := []struct {
		subdomain string
		want      string
	}{
		{"mystudio", "tenant_mystudio"},
		{"test-app", "tenant_test-app"},
		{"a", "tenant_a"},
	}
	for _, tt := range tests {
		got := routeID(tt.subdomain)
		if got != tt.want {
			t.Errorf("routeID(%q) = %q, want %q", tt.subdomain, got, tt.want)
		}
	}
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("http://localhost:2019", "", "")
	if c.serverName != "srv1" {
		t.Errorf("expected default serverName 'srv1', got %q", c.serverName)
	}
	if c.domain != "freeradio.app" {
		t.Errorf("expected default domain 'freeradio.app', got %q", c.domain)
	}
}

func TestAddRoute_HappyPath(t *testing.T) {
	var receivedBody []byte
	var receivedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	err := c.AddRoute(context.Background(), "mystudio", "10.10.10.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := "/config/apps/http/servers/srv1/routes"
	if receivedPath != expectedPath {
		t.Errorf("expected path %q, got %q", expectedPath, receivedPath)
	}

	var route caddyRoute
	if err := json.Unmarshal(receivedBody, &route); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if route.ID != "tenant_mystudio" {
		t.Errorf("expected @id 'tenant_mystudio', got %q", route.ID)
	}
	if len(route.Match) != 1 || len(route.Match[0].Host) != 1 || route.Match[0].Host[0] != "mystudio.freeradio.app" {
		t.Errorf("unexpected match: %+v", route.Match)
	}
	if len(route.Handle) != 1 || len(route.Handle[0].Upstreams) != 1 || route.Handle[0].Upstreams[0].Dial != "10.10.10.5:80" {
		t.Errorf("unexpected handle: %+v", route.Handle)
	}
	if !route.Terminal {
		t.Error("expected terminal=true")
	}
}

func TestAddRoute_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	err := c.AddRoute(context.Background(), "mystudio", "10.10.10.5")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain status code 500, got: %v", err)
	}
}

func TestAddRoute_ConnectionError(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "srv1", "freeradio.app")
	err := c.AddRoute(context.Background(), "mystudio", "10.10.10.5")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "add route") {
		t.Errorf("expected error to mention 'add route', got: %v", err)
	}
}

func TestRemoveRoute_HappyPath(t *testing.T) {
	var receivedPath string
	var receivedMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	err := c.RemoveRoute(context.Background(), "mystudio")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", receivedMethod)
	}
	expectedPath := "/id/tenant_mystudio"
	if receivedPath != expectedPath {
		t.Errorf("expected path %q, got %q", expectedPath, receivedPath)
	}
}

func TestRemoveRoute_NotFound_Idempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	err := c.RemoveRoute(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for 404 (idempotent), got: %v", err)
	}
}

func TestRemoveRoute_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	err := c.RemoveRoute(context.Background(), "mystudio")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain 500, got: %v", err)
	}
}

func TestListRoutes_MultipleRoutes(t *testing.T) {
	routes := []caddyRoute{
		{
			ID:    "tenant_studio1",
			Match: []caddyMatch{{Host: []string{"studio1.freeradio.app"}}},
			Handle: []caddyHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: "10.10.10.5:80"}},
			}},
			Terminal: true,
		},
		{
			ID:    "tenant_studio2",
			Match: []caddyMatch{{Host: []string{"studio2.freeradio.app"}}},
			Handle: []caddyHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: "10.10.10.6:80"}},
			}},
			Terminal: true,
		},
		// Non-tenant route (should be filtered out)
		{
			ID:    "ci_jenkins",
			Match: []caddyMatch{{Host: []string{"ci.home.lan"}}},
			Handle: []caddyHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: "127.0.0.1:8090"}},
			}},
			Terminal: true,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(routes)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	result, err := c.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 tenant routes, got %d", len(result))
	}

	if result[0].Subdomain != "studio1" || result[0].TargetIP != "10.10.10.5" {
		t.Errorf("route 0: got %+v", result[0])
	}
	if result[1].Subdomain != "studio2" || result[1].TargetIP != "10.10.10.6" {
		t.Errorf("route 1: got %+v", result[1])
	}
}

func TestListRoutes_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	result, err := c.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 routes, got %d", len(result))
	}
}

func TestListRoutes_MixedTenantNonTenant(t *testing.T) {
	routes := []caddyRoute{
		{ID: "some_other_route"},
		{ID: ""},
		{
			ID: "tenant_myapp",
			Handle: []caddyHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: "10.10.10.7:80"}},
			}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(routes)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	result, err := c.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 tenant route, got %d", len(result))
	}
	if result[0].Subdomain != "myapp" || result[0].TargetIP != "10.10.10.7" {
		t.Errorf("unexpected route: %+v", result[0])
	}
}

func TestListRoutes_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "srv1", "freeradio.app")
	_, err := c.ListRoutes(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}
