package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// mockTenantLister implements TenantLister for testing.
type mockTenantLister struct {
	tenants []TenantRoute
	err     error
}

func (m *mockTenantLister) ListActiveWithIP(_ context.Context) ([]TenantRoute, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.tenants, nil
}

func TestReconcile_AllRoutesAdded(t *testing.T) {
	var mu sync.Mutex
	addedRoutes := map[string]string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var route caddyRoute
			json.NewDecoder(r.Body).Decode(&route)
			mu.Lock()
			if len(route.Handle) > 0 && len(route.Handle[0].Upstreams) > 0 {
				addedRoutes[route.ID] = route.Handle[0].Upstreams[0].Dial
			}
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "srv1", "freeradio.app")
	lister := &mockTenantLister{
		tenants: []TenantRoute{
			{Subdomain: "studio1", LXCIP: "10.10.10.5"},
			{Subdomain: "studio2", LXCIP: "10.10.10.6"},
			{Subdomain: "studio3", LXCIP: "10.10.10.7"},
		},
	}

	result, err := Reconcile(context.Background(), client, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success != 3 {
		t.Errorf("expected 3 successful, got %d", result.Success)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", result.Failed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(addedRoutes) != 3 {
		t.Errorf("expected 3 routes added, got %d", len(addedRoutes))
	}
	if addedRoutes["tenant_studio1"] != "10.10.10.5:80" {
		t.Errorf("studio1 route: got %q", addedRoutes["tenant_studio1"])
	}
}

func TestReconcile_SomeRoutesFail(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()

		// Fail the second call
		if n == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "srv1", "freeradio.app")
	lister := &mockTenantLister{
		tenants: []TenantRoute{
			{Subdomain: "studio1", LXCIP: "10.10.10.5"},
			{Subdomain: "studio2", LXCIP: "10.10.10.6"},
			{Subdomain: "studio3", LXCIP: "10.10.10.7"},
		},
	}

	result, err := Reconcile(context.Background(), client, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success != 2 {
		t.Errorf("expected 2 successful, got %d", result.Success)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
}

func TestReconcile_NoTenants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when there are no tenants")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "srv1", "freeradio.app")
	lister := &mockTenantLister{tenants: nil}

	result, err := Reconcile(context.Background(), client, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success != 0 || result.Failed != 0 {
		t.Errorf("expected 0/0, got %d/%d", result.Success, result.Failed)
	}
}

func TestReconcile_ListerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when lister fails")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "srv1", "freeradio.app")
	lister := &mockTenantLister{err: fmt.Errorf("database connection lost")}

	_, err := Reconcile(context.Background(), client, lister)
	if err == nil {
		t.Fatal("expected error from lister failure")
	}
	if err.Error() != "caddy: list active tenants: database connection lost" {
		t.Errorf("unexpected error: %v", err)
	}
}
