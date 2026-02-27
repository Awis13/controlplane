package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/audit"
	"controlplane/internal/crypto"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/tenant"
)

// --- Test encryption key (32 bytes hex-encoded) ---

var testKey = hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

// --- Mock stores ---

type mockNodeStore struct {
	nodes      map[string]*node.Node
	createErr  error
	updateErr  error
	deleteErr  error
	reserveErr error
	releaseErr error
	countTenants int
	countErr     error
}

func newMockNodeStore() *mockNodeStore {
	return &mockNodeStore{nodes: make(map[string]*node.Node)}
}

func (m *mockNodeStore) List(_ context.Context) ([]node.Node, error) {
	var result []node.Node
	for _, n := range m.nodes {
		result = append(result, *n)
	}
	return result, nil
}

func (m *mockNodeStore) GetByID(_ context.Context, id string) (*node.Node, error) {
	n, ok := m.nodes[id]
	if !ok {
		return nil, nil
	}
	return n, nil
}

func (m *mockNodeStore) Create(_ context.Context, req node.CreateNodeRequest) (*node.Node, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	n := &node.Node{
		ID:          "new-node-id",
		Name:        req.Name,
		TailscaleIP: req.TailscaleIP,
		ProxmoxURL:  req.ProxmoxURL,
		TotalRAMMB:  req.TotalRAMMB,
		Status:      "active",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	m.nodes[n.ID] = n
	return n, nil
}

func (m *mockNodeStore) Update(_ context.Context, id string, req node.UpdateNodeRequest) (*node.Node, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	n, ok := m.nodes[id]
	if !ok {
		return nil, nil
	}
	if req.Status != nil {
		n.Status = *req.Status
	}
	if req.TotalRAMMB != nil {
		n.TotalRAMMB = *req.TotalRAMMB
	}
	return n, nil
}

func (m *mockNodeStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.nodes, id)
	return nil
}

func (m *mockNodeStore) CountTenants(_ context.Context, _ string) (int, error) {
	if m.countErr != nil {
		return 0, m.countErr
	}
	return m.countTenants, nil
}

func (m *mockNodeStore) ReserveRAM(_ context.Context, _ string, _ int) error {
	return m.reserveErr
}

func (m *mockNodeStore) ReleaseRAM(_ context.Context, _ string, _ int) error {
	return m.releaseErr
}

// ---

type mockProjectStore struct {
	projects     map[string]*project.Project
	createErr    error
	updateErr    error
	deleteErr    error
	countTenants int
	countErr     error
}

func newMockProjectStore() *mockProjectStore {
	return &mockProjectStore{projects: make(map[string]*project.Project)}
}

func (m *mockProjectStore) List(_ context.Context) ([]project.Project, error) {
	var result []project.Project
	for _, p := range m.projects {
		result = append(result, *p)
	}
	return result, nil
}

func (m *mockProjectStore) GetByID(_ context.Context, id string) (*project.Project, error) {
	p, ok := m.projects[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (m *mockProjectStore) Create(_ context.Context, req project.CreateProjectRequest) (*project.Project, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	p := &project.Project{
		ID:         "new-project-id",
		Name:       req.Name,
		TemplateID: req.TemplateID,
		Ports:      req.Ports,
		HealthPath: req.HealthPath,
		RAMMB:      req.RAMMB,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	m.projects[p.ID] = p
	return p, nil
}

func (m *mockProjectStore) Update(_ context.Context, id string, req project.UpdateProjectRequest) (*project.Project, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	p, ok := m.projects[id]
	if !ok {
		return nil, nil
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	return p, nil
}

func (m *mockProjectStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.projects, id)
	return nil
}

func (m *mockProjectStore) CountTenants(_ context.Context, _ string) (int, error) {
	if m.countErr != nil {
		return 0, m.countErr
	}
	return m.countTenants, nil
}

// ---

type mockTenantStore struct {
	tenants        map[string]*tenant.Tenant
	createErr      error
	setDeletingErr error
	setDeletedErr  error
	setSuspendedErr error
	setResumedErr   error
}

func newMockTenantStore() *mockTenantStore {
	return &mockTenantStore{tenants: make(map[string]*tenant.Tenant)}
}

func (m *mockTenantStore) List(_ context.Context) ([]tenant.Tenant, error) {
	var result []tenant.Tenant
	for _, t := range m.tenants {
		result = append(result, *t)
	}
	return result, nil
}

func (m *mockTenantStore) GetByID(_ context.Context, id string) (*tenant.Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *mockTenantStore) Create(_ context.Context, req tenant.CreateTenantRequest) (*tenant.Tenant, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	t := &tenant.Tenant{
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

// ---

type mockProvisioner struct {
	provisionCalled   bool
	deprovisionCalled bool
	suspendCalled     bool
	resumeCalled      bool
	deprovisionErr    error
	suspendErr        error
	resumeErr         error
	invalidatedNodes  []string
}

func newMockProvisioner() *mockProvisioner {
	return &mockProvisioner{}
}

func (m *mockProvisioner) Provision(_, _, _, _ string, _ int) {
	m.provisionCalled = true
}

func (m *mockProvisioner) Deprovision(_ context.Context, _, _ string, _, _ int) error {
	m.deprovisionCalled = true
	return m.deprovisionErr
}

func (m *mockProvisioner) Suspend(_ context.Context, _, _ string, _ int) error {
	m.suspendCalled = true
	return m.suspendErr
}

func (m *mockProvisioner) Resume(_ context.Context, _, _ string, _ int) error {
	m.resumeCalled = true
	return m.resumeErr
}

func (m *mockProvisioner) InvalidateClient(nodeID string) {
	m.invalidatedNodes = append(m.invalidatedNodes, nodeID)
}

// --- Test helpers ---

const (
	testNodeID    = "11111111-1111-1111-1111-111111111111"
	testProjectID = "22222222-2222-2222-2222-222222222222"
	testTenantID  = "33333333-3333-3333-3333-333333333333"
)

func testHandler(t *testing.T) (*Handler, *mockNodeStore, *mockProjectStore, *mockTenantStore, *mockProvisioner) {
	t.Helper()
	ns := newMockNodeStore()
	ps := newMockProjectStore()
	ts := newMockTenantStore()
	prov := newMockProvisioner()

	h, err := NewHandler(ns, ps, ts, nil, prov, testKey, "test-setup-token", nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h, ns, ps, ts, prov
}

// authCookie creates a valid encrypted session cookie for testing.
func authCookie(t *testing.T) *http.Cookie {
	t.Helper()
	sess := sessionPayload{
		UserID:    "admin",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := crypto.Encrypt(string(data), testKey)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: encrypted,
	}
}

// testRouter builds a chi router for testing that skips CSRF but keeps auth.
func testRouter(h *Handler) chi.Router {
	r := chi.NewRouter()

	r.Get("/login", h.loginPage)
	r.Post("/logout", h.logout)

	r.Group(func(r chi.Router) {
		r.Use(requireAuth(h.encryptionKey))
		r.Use(maxBodySize(1 << 20))

		r.Get("/", h.dashboard)
		r.Get("/nodes", h.nodesList)
		r.Get("/nodes/{id}", h.nodeDetail)
		r.Get("/projects", h.projectsList)
		r.Get("/projects/{id}", h.projectDetail)
		r.Get("/tenants", h.tenantsList)
		r.Get("/tenants/{id}", h.tenantDetail)
		r.Get("/audit", h.auditPage)
		r.Get("/settings", h.settingsPage)

		r.Post("/nodes", h.createNode)
		r.Put("/nodes/{id}", h.updateNodeAdmin)
		r.Delete("/nodes/{id}", h.deleteNodeAdmin)

		r.Post("/projects", h.createProject)
		r.Put("/projects/{id}", h.updateProjectAdmin)
		r.Delete("/projects/{id}", h.deleteProjectAdmin)

		r.Post("/tenants", h.createTenant)
		r.Delete("/tenants/{id}", h.deleteTenant)
		r.Post("/tenants/{id}/suspend", h.suspendTenant)
		r.Post("/tenants/{id}/resume", h.resumeTenant)
		r.Get("/tenants/{id}/row", h.tenantRow)

		r.Delete("/webauthn/credentials/{id}", h.deleteCredential)
	})

	return r
}

func doRequest(t *testing.T, h *Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(authCookie(t))

	w := httptest.NewRecorder()
	testRouter(h).ServeHTTP(w, req)
	return w
}

// --- Auth tests ---

func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	testRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}
}

// --- Dashboard tests ---

func TestDashboard(t *testing.T) {
	h, ns, ps, ts, _ := testHandler(t)

	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", TotalRAMMB: 8192, AllocatedRAMMB: 2048,
		Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", TemplateID: 100, RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		Status: "active", HealthStatus: "healthy", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// --- Node tests ---

func TestNodesList(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/nodes", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreateNode_Valid(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":         {"test-node"},
		"tailscale_ip": {"100.1.2.3"},
		"proxmox_url":  {"https://10.0.0.1:8006"},
		"api_token":    {"test-token"},
		"total_ram_mb": {"8192"},
	}
	w := doRequest(t, h, "POST", "/nodes", form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

func TestCreateNode_MissingFields(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{"name": {"only-name"}}
	w := doRequest(t, h, "POST", "/nodes", form)

	// Flash error via HX-Retarget
	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error, got headers: %v", w.Header())
	}
}

func TestCreateNode_InvalidName(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":         {"INVALID-NAME"},
		"tailscale_ip": {"100.1.2.3"},
		"proxmox_url":  {"https://10.0.0.1:8006"},
		"api_token":    {"test-token"},
		"total_ram_mb": {"8192"},
	}
	w := doRequest(t, h, "POST", "/nodes", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for invalid name")
	}
}

func TestCreateNode_InvalidIP(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":         {"test-node"},
		"tailscale_ip": {"not-an-ip"},
		"proxmox_url":  {"https://10.0.0.1:8006"},
		"api_token":    {"test-token"},
		"total_ram_mb": {"8192"},
	}
	w := doRequest(t, h, "POST", "/nodes", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for invalid IP")
	}
}

func TestCreateNode_InvalidProxmoxURL(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":         {"test-node"},
		"tailscale_ip": {"100.1.2.3"},
		"proxmox_url":  {"http://insecure.com"},
		"api_token":    {"test-token"},
		"total_ram_mb": {"8192"},
	}
	w := doRequest(t, h, "POST", "/nodes", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for non-HTTPS URL")
	}
}

func TestNodeDetail(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", TailscaleIP: "100.1.2.3",
		ProxmoxURL: "https://10.0.0.1:8006", TotalRAMMB: 8192,
		Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/nodes/"+testNodeID, nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNodeDetail_NotFound(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/nodes/"+testNodeID, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestNodeDetail_InvalidID(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/nodes/not-uuid", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestUpdateNode(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", TotalRAMMB: 8192,
		Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{"status": {"maintenance"}}
	w := doRequest(t, h, "PUT", "/nodes/"+testNodeID, form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/admin/nodes/"+testNodeID {
		t.Errorf("HX-Redirect = %q, want /admin/nodes/%s", loc, testNodeID)
	}
}

func TestUpdateNode_InvalidStatus(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{"status": {"invalid-status"}}
	w := doRequest(t, h, "PUT", "/nodes/"+testNodeID, form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for invalid status")
	}
}

func TestUpdateNode_RAMBelowAllocated(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", TotalRAMMB: 8192, AllocatedRAMMB: 4096,
		Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ns.updateErr = node.ErrRAMBelowAllocated

	form := url.Values{"total_ram_mb": {"2048"}}
	w := doRequest(t, h, "PUT", "/nodes/"+testNodeID, form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for RAM below allocated")
	}
}

func TestDeleteNode(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ns.countTenants = 0

	w := doRequest(t, h, "DELETE", "/nodes/"+testNodeID, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/admin/nodes" {
		t.Errorf("HX-Redirect = %q, want /admin/nodes", loc)
	}
}

func TestDeleteNode_HasTenants(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ns.countTenants = 3

	w := doRequest(t, h, "DELETE", "/nodes/"+testNodeID, nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error when node has tenants")
	}
}

// --- Project tests ---

func TestProjectsList(t *testing.T) {
	h, _, ps, _, _ := testHandler(t)
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", TemplateID: 100,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/projects", nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreateProject_Valid(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":        {"test-project"},
		"template_id": {"100"},
		"ports":       {"80,443"},
		"health_path": {"/health"},
		"ram_mb":      {"2048"},
	}
	w := doRequest(t, h, "POST", "/projects", form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

func TestCreateProject_MissingFields(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{"name": {""}}
	w := doRequest(t, h, "POST", "/projects", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for missing fields")
	}
}

func TestCreateProject_InvalidTemplateID(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":        {"test"},
		"template_id": {"abc"},
	}
	w := doRequest(t, h, "POST", "/projects", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for invalid template ID")
	}
}

func TestCreateProject_InvalidPort(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{
		"name":        {"test"},
		"template_id": {"100"},
		"ports":       {"80,99999"},
	}
	w := doRequest(t, h, "POST", "/projects", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for invalid port")
	}
}

func TestProjectDetail(t *testing.T) {
	h, _, ps, _, _ := testHandler(t)
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", TemplateID: 100,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/projects/"+testProjectID, nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestProjectDetail_NotFound(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/projects/"+testProjectID, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestUpdateProject(t *testing.T) {
	h, _, ps, _, _ := testHandler(t)
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", TemplateID: 100,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{"name": {"updated-name"}}
	w := doRequest(t, h, "PUT", "/projects/"+testProjectID, form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/admin/projects/"+testProjectID {
		t.Errorf("HX-Redirect = %q, want /admin/projects/%s", loc, testProjectID)
	}
}

func TestDeleteProject(t *testing.T) {
	h, _, ps, _, _ := testHandler(t)
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "DELETE", "/projects/"+testProjectID, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestDeleteProject_HasTenants(t *testing.T) {
	h, _, ps, _, _ := testHandler(t)
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.countTenants = 2

	w := doRequest(t, h, "DELETE", "/projects/"+testProjectID, nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error when project has tenants")
	}
}

// --- Tenant tests ---

func TestTenantsList(t *testing.T) {
	h, ns, ps, ts, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		Status: "active", Subdomain: "test", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/tenants", nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestTenantsListWithFilters(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/tenants?status=active&node_id="+testNodeID, nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreateTenant_Valid(t *testing.T) {
	h, ns, ps, _, prov := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", TotalRAMMB: 8192, Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{
		"name":       {"my-tenant"},
		"subdomain":  {"myapp"},
		"project_id": {testProjectID},
		"node_id":    {testNodeID},
	}
	w := doRequest(t, h, "POST", "/tenants", form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !prov.provisionCalled {
		t.Error("expected provisioner.Provision to be called")
	}
}

func TestCreateTenant_MissingFields(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	form := url.Values{"name": {"only-name"}}
	w := doRequest(t, h, "POST", "/tenants", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for missing fields")
	}
}

func TestCreateTenant_InvalidSubdomain(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	invalids := []string{"A", "-bad", "bad-", "a"}
	for _, sub := range invalids {
		form := url.Values{
			"name": {"test"}, "subdomain": {sub},
			"project_id": {testProjectID}, "node_id": {testNodeID},
		}
		w := doRequest(t, h, "POST", "/tenants", form)

		if w.Header().Get("HX-Retarget") != "#flash" {
			t.Errorf("subdomain %q: expected flash error", sub)
		}
	}
}

func TestCreateTenant_ReservedSubdomain(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	for _, sub := range []string{"www", "api", "admin", "cdn"} {
		form := url.Values{
			"name": {"test-" + sub}, "subdomain": {sub},
			"project_id": {testProjectID}, "node_id": {testNodeID},
		}
		w := doRequest(t, h, "POST", "/tenants", form)

		if w.Header().Get("HX-Retarget") != "#flash" {
			t.Errorf("subdomain %q: expected flash error for reserved subdomain", sub)
		}
	}
}

func TestCreateTenant_NodeNotFound(t *testing.T) {
	h, _, ps, _, _ := testHandler(t)
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{
		"name": {"test"}, "subdomain": {"myapp"},
		"project_id": {testProjectID}, "node_id": {testNodeID},
	}
	w := doRequest(t, h, "POST", "/tenants", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for node not found")
	}
}

func TestCreateTenant_InactiveNode(t *testing.T) {
	h, ns, ps, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "maintenance",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{
		"name": {"test"}, "subdomain": {"myapp"},
		"project_id": {testProjectID}, "node_id": {testNodeID},
	}
	w := doRequest(t, h, "POST", "/tenants", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for inactive node")
	}
}

func TestCreateTenant_InsufficientRAM(t *testing.T) {
	h, ns, ps, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ns.reserveErr = node.ErrInsufficientCapacity
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{
		"name": {"test"}, "subdomain": {"myapp"},
		"project_id": {testProjectID}, "node_id": {testNodeID},
	}
	w := doRequest(t, h, "POST", "/tenants", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for insufficient RAM")
	}
}

func TestTenantDetail(t *testing.T) {
	h, ns, ps, ts, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		Status: "active", Subdomain: "test", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/tenants/"+testTenantID, nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestTenantDetail_NotFound(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/tenants/"+testTenantID, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteTenant(t *testing.T) {
	h, _, ps, ts, prov := testHandler(t)
	lxcID := 105
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		LXCID: &lxcID, Status: "active", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "DELETE", "/tenants/"+testTenantID, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !prov.deprovisionCalled {
		t.Error("expected provisioner.Deprovision to be called")
	}
}

func TestDeleteTenant_WrongStatus(t *testing.T) {
	h, _, _, ts, _ := testHandler(t)
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", Status: "provisioning",
		ProjectID: testProjectID, NodeID: testNodeID, Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "DELETE", "/tenants/"+testTenantID, nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for wrong status")
	}
}

func TestDeleteTenant_AlreadyDeleting(t *testing.T) {
	h, _, ps, ts, prov := testHandler(t)
	lxcID := 105
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		LXCID: &lxcID, Status: "active", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, RAMMB: 1536, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	prov.deprovisionErr = tenant.ErrStateConflict

	w := doRequest(t, h, "DELETE", "/tenants/"+testTenantID, nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for concurrent delete")
	}
}

func TestSuspendTenant(t *testing.T) {
	h, _, _, ts, prov := testHandler(t)
	lxcID := 105
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		LXCID: &lxcID, Status: "active", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "POST", "/tenants/"+testTenantID+"/suspend", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !prov.suspendCalled {
		t.Error("expected provisioner.Suspend to be called")
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/admin/tenants/"+testTenantID {
		t.Errorf("HX-Redirect = %q, want /admin/tenants/%s", loc, testTenantID)
	}
}

func TestSuspendTenant_NotActive(t *testing.T) {
	h, _, _, ts, _ := testHandler(t)
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", Status: "suspended",
		ProjectID: testProjectID, NodeID: testNodeID, Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "POST", "/tenants/"+testTenantID+"/suspend", nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for non-active tenant")
	}
}

func TestSuspendTenant_Rollback(t *testing.T) {
	h, _, _, ts, prov := testHandler(t)
	lxcID := 105
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		LXCID: &lxcID, Status: "active", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	prov.suspendErr = errors.New("proxmox error")

	w := doRequest(t, h, "POST", "/tenants/"+testTenantID+"/suspend", nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error on suspend failure")
	}
	// DB state should be rolled back to active
	if ts.tenants[testTenantID].Status != "active" {
		t.Errorf("status = %q, want 'active' after rollback", ts.tenants[testTenantID].Status)
	}
}

func TestResumeTenant(t *testing.T) {
	h, _, _, ts, prov := testHandler(t)
	lxcID := 105
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		LXCID: &lxcID, Status: "suspended", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "POST", "/tenants/"+testTenantID+"/resume", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !prov.resumeCalled {
		t.Error("expected provisioner.Resume to be called")
	}
}

func TestResumeTenant_NotSuspended(t *testing.T) {
	h, _, _, ts, _ := testHandler(t)
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", Status: "active",
		ProjectID: testProjectID, NodeID: testNodeID, Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "POST", "/tenants/"+testTenantID+"/resume", nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for non-suspended tenant")
	}
}

func TestResumeTenant_Rollback(t *testing.T) {
	h, _, _, ts, prov := testHandler(t)
	lxcID := 105
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		LXCID: &lxcID, Status: "suspended", Subdomain: "test",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	prov.resumeErr = errors.New("proxmox error")

	w := doRequest(t, h, "POST", "/tenants/"+testTenantID+"/resume", nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error on resume failure")
	}
	// DB state should be rolled back to suspended
	if ts.tenants[testTenantID].Status != "suspended" {
		t.Errorf("status = %q, want 'suspended' after rollback", ts.tenants[testTenantID].Status)
	}
}

// --- Audit page tests ---

func TestAuditPage(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/audit", nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// --- Settings page tests ---

func TestSettingsPage(t *testing.T) {
	h, ns, _, _, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/settings", nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// --- Tenant row polling ---

func TestTenantRow(t *testing.T) {
	h, ns, ps, ts, _ := testHandler(t)
	ns.nodes[testNodeID] = &node.Node{
		ID: testNodeID, Name: "node-1", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ps.projects[testProjectID] = &project.Project{
		ID: testProjectID, Name: "project-1", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	ts.tenants[testTenantID] = &tenant.Tenant{
		ID: testTenantID, Name: "tenant-1", ProjectID: testProjectID, NodeID: testNodeID,
		Status: "provisioning", Subdomain: "test", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/tenants/"+testTenantID+"/row", nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestTenantRow_NotFound(t *testing.T) {
	h, _, _, _, _ := testHandler(t)

	w := doRequest(t, h, "GET", "/tenants/"+testTenantID+"/row", nil)
	// 286 = stop polling
	if w.Code != 286 {
		t.Errorf("status = %d, want 286", w.Code)
	}
}

// --- Unused import guard (compile check) ---

var _ audit.Entry
