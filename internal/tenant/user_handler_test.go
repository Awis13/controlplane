package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"controlplane/internal/auth"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/user"
)

// --- Mock stores for user handler ---

type mockUserTenantStore struct {
	mockTenantStore
	ownerTenants map[string][]Tenant
	createErr    error
}

func newMockUserTenantStore() *mockUserTenantStore {
	return &mockUserTenantStore{
		mockTenantStore: mockTenantStore{tenants: make(map[string]*Tenant)},
		ownerTenants:    make(map[string][]Tenant),
	}
}

func (m *mockUserTenantStore) CreateWithOwner(_ context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	t := &Tenant{
		ID:           "user-tenant-id",
		Name:         req.Name,
		ProjectID:    req.ProjectID,
		NodeID:       req.NodeID,
		Subdomain:    req.Subdomain,
		Status:       "provisioning",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	m.tenants[t.ID] = t
	m.ownerTenants[ownerID] = append(m.ownerTenants[ownerID], *t)
	return t, nil
}

func (m *mockUserTenantStore) ListByOwnerID(_ context.Context, ownerID string) ([]Tenant, error) {
	return m.ownerTenants[ownerID], nil
}

type mockUserNodeStore struct {
	mockNodeStore
	leastLoaded *node.Node
}

func newMockUserNodeStore() *mockUserNodeStore {
	return &mockUserNodeStore{
		mockNodeStore: mockNodeStore{nodes: make(map[string]*node.Node)},
	}
}

func (m *mockUserNodeStore) GetLeastLoaded(_ context.Context, _ int) (*node.Node, error) {
	return m.leastLoaded, nil
}

type mockUserProjectStore struct {
	mockProjectStore
	defaultProject *project.Project
}

func newMockUserProjectStore() *mockUserProjectStore {
	return &mockUserProjectStore{
		mockProjectStore: mockProjectStore{projects: make(map[string]*project.Project)},
	}
}

func (m *mockUserProjectStore) GetDefault(_ context.Context) (*project.Project, error) {
	return m.defaultProject, nil
}

// --- Test helpers ---

var testUserID = uuid.MustParse("55555555-5555-5555-5555-555555555555")

func testUser() *user.User {
	return &user.User{
		ID:          testUserID,
		Email:       "test@example.com",
		DisplayName: "Test User",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func userTenantRouter(h *UserHandler) *chi.Mux {
	r := chi.NewRouter()
	// Simulate JWT middleware by injecting user into context
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := testUser()
			ctx := auth.SetUserForTest(r.Context(), u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Get("/tenants", h.List)
	r.Post("/tenants", h.Create)
	return r
}

func userTenantRouterNoAuth(h *UserHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/tenants", h.List)
	r.Post("/tenants", h.Create)
	return r
}

// --- Tests ---

func TestUserList_Empty(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	req := httptest.NewRequest("GET", "/tenants", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		Items []Tenant `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(result.Items))
	}
}

func TestUserList_ReturnOwnTenants(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ownerID := testUserID.String()
	ts.ownerTenants[ownerID] = []Tenant{
		{ID: "t1", Name: "My Radio", OwnerID: &ownerID, Status: "active"},
		{ID: "t2", Name: "My Other Radio", OwnerID: &ownerID, Status: "provisioning"},
	}

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	req := httptest.NewRequest("GET", "/tenants", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result struct {
		Items []Tenant `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Items))
	}
}

func TestUserList_Unauthenticated(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouterNoAuth(h)

	req := httptest.NewRequest("GET", "/tenants", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestUserCreate_Success(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ns.leastLoaded = activeNode()
	ps.defaultProject = testProjectObj()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp Tenant
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "provisioning" {
		t.Errorf("status = %q, want provisioning", resp.Status)
	}
	if resp.OwnerID == nil || *resp.OwnerID != testUserID.String() {
		t.Errorf("owner_id = %v, want %s", resp.OwnerID, testUserID)
	}

	<-prov.provisionDone
	if !prov.wasProvisionCalled() {
		t.Error("expected provisioner.Provision to be called")
	}
}

func TestUserCreate_MissingName(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUserCreate_MissingSubdomain(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUserCreate_InvalidSubdomain(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	invalids := []string{"-bad", "bad-", "A", "a", "has space"}
	for _, sub := range invalids {
		body, _ := json.Marshal(UserCreateRequest{Name: "Test", Subdomain: sub})
		req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("subdomain %q: expected 400, got %d", sub, w.Code)
		}
	}
}

func TestUserCreate_ReservedSubdomain(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "Admin Radio", Subdomain: "admin"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUserCreate_NoProject(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ps.defaultProject = nil // no projects configured

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_NoAvailableNode(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ps.defaultProject = testProjectObj()
	ns.leastLoaded = nil // no nodes with capacity

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_InsufficientCapacity(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ps.defaultProject = testProjectObj()
	ns.leastLoaded = activeNode()
	ns.reserveErr = node.ErrInsufficientCapacity

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_Unauthenticated(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov)
	r := userTenantRouterNoAuth(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
