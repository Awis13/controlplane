package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/node"
	"controlplane/internal/project"
)

// --- Mock tenant store ---

type mockTenantStore struct {
	tenants          map[string]*Tenant
	createErr        error
	deleteErr        error
	setActiveErr     error
	setErrorErr      error
	setDeletingErr   error
	setDeletedErr    error
	setSuspendedErr  error
	setResumedErr    error
	updateErr        error
}

func newMockTenantStore() *mockTenantStore {
	return &mockTenantStore{tenants: make(map[string]*Tenant)}
}

func (m *mockTenantStore) List(_ context.Context) ([]Tenant, error) {
	var result []Tenant
	for _, t := range m.tenants {
		result = append(result, *t)
	}
	return result, nil
}

func (m *mockTenantStore) ListPaginated(_ context.Context, limit, offset int, status, nodeID, projectID string) ([]Tenant, int, error) {
	var result []Tenant
	for _, t := range m.tenants {
		if status != "" && t.Status != status {
			continue
		}
		if nodeID != "" && t.NodeID != nodeID {
			continue
		}
		if projectID != "" && t.ProjectID != projectID {
			continue
		}
		result = append(result, *t)
	}
	total := len(result)
	if offset < len(result) {
		end := offset + limit
		if end > len(result) {
			end = len(result)
		}
		result = result[offset:end]
	} else {
		result = nil
	}
	return result, total, nil
}

func (m *mockTenantStore) Update(_ context.Context, id string, req UpdateTenantRequest) (*Tenant, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	t, ok := m.tenants[id]
	if !ok {
		return nil, nil
	}
	if req.Name != nil {
		t.Name = *req.Name
	}
	if req.StripeSubscriptionID != nil {
		t.StripeSubscriptionID = req.StripeSubscriptionID
	}
	if req.StripeCustomerID != nil {
		t.StripeCustomerID = req.StripeCustomerID
	}
	return t, nil
}

func (m *mockTenantStore) GetByID(_ context.Context, id string) (*Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *mockTenantStore) Create(_ context.Context, req CreateTenantRequest) (*Tenant, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	t := &Tenant{
		ID:           "new-tenant-id",
		Name:         req.Name,
		ProjectID:    req.ProjectID,
		NodeID:       req.NodeID,
		Subdomain:    req.Subdomain,
		Status:       "provisioning",
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	m.tenants[t.ID] = t
	return t, nil
}

func (m *mockTenantStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.tenants, id)
	return nil
}

func (m *mockTenantStore) SetActive(_ context.Context, id string, lxcID int) error {
	if m.setActiveErr != nil {
		return m.setActiveErr
	}
	if t, ok := m.tenants[id]; ok {
		t.Status = "active"
		t.LXCID = &lxcID
	}
	return nil
}

func (m *mockTenantStore) SetError(_ context.Context, id string, errMsg string) error {
	if m.setErrorErr != nil {
		return m.setErrorErr
	}
	if t, ok := m.tenants[id]; ok {
		t.Status = "error"
		t.ErrorMessage = &errMsg
	}
	return nil
}

func (m *mockTenantStore) SetDeleting(_ context.Context, id string) error {
	if m.setDeletingErr != nil {
		return m.setDeletingErr
	}
	if t, ok := m.tenants[id]; ok {
		t.Status = "deleting"
	}
	return nil
}

func (m *mockTenantStore) SetDeleted(_ context.Context, id string) error {
	if m.setDeletedErr != nil {
		return m.setDeletedErr
	}
	if t, ok := m.tenants[id]; ok {
		t.Status = "deleted"
	}
	return nil
}

func (m *mockTenantStore) SetSuspended(_ context.Context, id string) error {
	if m.setSuspendedErr != nil {
		return m.setSuspendedErr
	}
	if t, ok := m.tenants[id]; ok {
		t.Status = "suspended"
	}
	return nil
}

func (m *mockTenantStore) SetResumed(_ context.Context, id string) error {
	if m.setResumedErr != nil {
		return m.setResumedErr
	}
	if t, ok := m.tenants[id]; ok {
		t.Status = "active"
	}
	return nil
}

// --- Mock node store ---

type mockNodeStore struct {
	nodes      map[string]*node.Node
	reserveErr error
	releaseErr error
}

func newMockNodeStore() *mockNodeStore {
	return &mockNodeStore{nodes: make(map[string]*node.Node)}
}

func (m *mockNodeStore) GetByID(_ context.Context, id string) (*node.Node, error) {
	n, ok := m.nodes[id]
	if !ok {
		return nil, nil
	}
	return n, nil
}

func (m *mockNodeStore) ReserveRAM(_ context.Context, _ string, _ int) error {
	return m.reserveErr
}

func (m *mockNodeStore) ReleaseRAM(_ context.Context, _ string, _ int) error {
	return m.releaseErr
}

// --- Mock project store ---

type mockProjectStore struct {
	projects map[string]*project.Project
}

func newMockProjectStore() *mockProjectStore {
	return &mockProjectStore{projects: make(map[string]*project.Project)}
}

func (m *mockProjectStore) GetByID(_ context.Context, id string) (*project.Project, error) {
	p, ok := m.projects[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

// --- Mock provisioner ---

type mockProvisioner struct {
	mu                sync.Mutex
	provisionCalled   bool
	deprovisionCalled bool
	suspendCalled     bool
	resumeCalled      bool
	deprovisionErr    error
	suspendErr        error
	resumeErr         error
	provisionDone     chan struct{} // signaled when Provision completes
}

func newMockProvisioner() *mockProvisioner {
	return &mockProvisioner{
		provisionDone: make(chan struct{}, 1),
	}
}

func (m *mockProvisioner) Provision(_, _, _, _ string, _ int) {
	m.mu.Lock()
	m.provisionCalled = true
	m.mu.Unlock()
	select {
	case m.provisionDone <- struct{}{}:
	default:
	}
}

func (m *mockProvisioner) Deprovision(_ context.Context, _, _, _ string, _, _ int) error {
	m.mu.Lock()
	m.deprovisionCalled = true
	m.mu.Unlock()
	return m.deprovisionErr
}

func (m *mockProvisioner) Suspend(_ context.Context, _, _ string, _ int) error {
	m.mu.Lock()
	m.suspendCalled = true
	m.mu.Unlock()
	return m.suspendErr
}

func (m *mockProvisioner) Resume(_ context.Context, _, _ string, _ int) error {
	m.mu.Lock()
	m.resumeCalled = true
	m.mu.Unlock()
	return m.resumeErr
}

func (m *mockProvisioner) wasProvisionCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.provisionCalled
}

func (m *mockProvisioner) wasDeprovisionCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deprovisionCalled
}

// --- Test helpers ---

func testRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/tenants", h.List)
	r.Post("/tenants", h.Create)
	r.Get("/tenants/{tenantID}", h.Get)
	r.Delete("/tenants/{tenantID}", h.Delete)
	return r
}

const validProjectID = "11111111-1111-1111-1111-111111111111"
const validNodeID = "22222222-2222-2222-2222-222222222222"
const validTenantID = "33333333-3333-3333-3333-333333333333"

func activeNode() *node.Node {
	return &node.Node{
		ID:             validNodeID,
		Name:           "test-node",
		ProxmoxURL:     "https://10.0.0.1:8006",
		TailscaleIP:    "100.1.2.3",
		TotalRAMMB:     8192,
		AllocatedRAMMB: 0,
		Status:         "active",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
}

func testProjectObj() *project.Project {
	return &project.Project{
		ID:         validProjectID,
		Name:       "test-project",
		TemplateID: 100,
		RAMMB:      1536,
		HealthPath: "/api/health",
		CreatedAt:  time.Now(),
	}
}

func createRequest(name, subdomain string) CreateTenantRequest {
	return CreateTenantRequest{
		Name:      name,
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		Subdomain: subdomain,
	}
}

// --- Tests ---

func TestCreate_Returns202(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ns.nodes[validNodeID] = activeNode()
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	body, _ := json.Marshal(createRequest("my-tenant", "myapp"))
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp Tenant
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "provisioning" {
		t.Errorf("expected status 'provisioning', got %q", resp.Status)
	}

	// Wait for the goroutine to complete
	<-prov.provisionDone
	if !prov.wasProvisionCalled() {
		t.Error("expected provisioner.Provision to be called")
	}
}

func TestCreate_NodeNotFound(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	// Node NOT added to store
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	body, _ := json.Marshal(createRequest("my-tenant", "myapp"))
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_ProjectNotFound(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ns.nodes[validNodeID] = activeNode()
	// Project NOT added to store

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	body, _ := json.Marshal(createRequest("my-tenant", "myapp"))
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_InsufficientCapacity(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ns.nodes[validNodeID] = activeNode()
	ps.projects[validProjectID] = testProjectObj()
	ns.reserveErr = node.ErrInsufficientCapacity

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	body, _ := json.Marshal(createRequest("my-tenant", "myapp"))
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_ReservedSubdomain(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ns.nodes[validNodeID] = activeNode()
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	reserved := []string{"www", "api", "admin", "app", "mail", "cdn"}
	for _, sub := range reserved {
		body, _ := json.Marshal(createRequest("tenant-"+sub, sub))
		req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("subdomain %q: expected 400, got %d: %s", sub, w.Code, w.Body.String())
		}
	}
}

func TestCreate_InactiveNode(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	inactiveNode := activeNode()
	inactiveNode.Status = "maintenance"
	ns.nodes[validNodeID] = inactiveNode
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	body, _ := json.Marshal(createRequest("my-tenant", "myapp"))
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_MissingFields(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	body, _ := json.Marshal(map[string]string{"name": "test"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_InvalidSubdomain(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	invalids := []string{"A", "-bad", "bad-", "a"}
	for _, sub := range invalids {
		body, _ := json.Marshal(CreateTenantRequest{
			Name:      "test",
			ProjectID: validProjectID,
			NodeID:    validNodeID,
			Subdomain: sub,
		})
		req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("subdomain %q: expected 400, got %d: %s", sub, w.Code, w.Body.String())
		}
	}
}

func TestDelete_ActiveTenant(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	lxcID := 105
	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		LXCID:     &lxcID,
		Subdomain: "myapp",
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !prov.wasDeprovisionCalled() {
		t.Error("expected provisioner.Deprovision to be called")
	}
}

func TestDelete_ErrorTenantNoLXC(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		LXCID:     nil, // no LXC container
		Subdomain: "myapp",
		Status:    "error",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Should NOT call deprovision (no LXC)
	if prov.wasDeprovisionCalled() {
		t.Error("did not expect provisioner.Deprovision to be called for tenant without LXC")
	}

	// Should be marked as deleted
	if ts.tenants[validTenantID].Status != "deleted" {
		t.Errorf("expected status 'deleted', got %q", ts.tenants[validTenantID].Status)
	}
}

func TestDelete_NotFound(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDelete_CannotDeleteProvisioningTenant(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		Subdomain: "myapp",
		Status:    "provisioning",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDelete_CannotDeleteDeletedTenant(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		Subdomain: "myapp",
		Status:    "deleted",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDelete_DeprovisionError(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()
	prov.deprovisionErr = errors.New("deprovision failed")

	lxcID := 105
	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		LXCID:     &lxcID,
		Subdomain: "myapp",
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDelete_ConcurrentDeleteReturns409(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	// Simulate state conflict (tenant already being deleted)
	prov.deprovisionErr = ErrStateConflict

	lxcID := 105
	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		LXCID:     &lxcID,
		Subdomain: "myapp",
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	ps.projects[validProjectID] = testProjectObj()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGet_Found(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	ts.tenants[validTenantID] = &Tenant{
		ID:        validTenantID,
		Name:      "test",
		ProjectID: validProjectID,
		NodeID:    validNodeID,
		Subdomain: "myapp",
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGet_NotFound(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGet_InvalidID(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

	req := httptest.NewRequest("GET", "/tenants/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestList_Empty(t *testing.T) {
	ts := newMockTenantStore()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	prov := newMockProvisioner()

	h := NewHandler(ts, ns, ps, prov, nil)
	r := testRouter(h)

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
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected empty list, got %d", len(result.Items))
	}
}
