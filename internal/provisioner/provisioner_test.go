package provisioner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/proxmox"
	"controlplane/internal/tenant"
)

// --- Mock stores ---

type mockNodeStore struct {
	mu     sync.Mutex
	nodes  map[string]*node.Node
	tokens map[string]string
	ram    map[string]int
}

func newMockNodeStore() *mockNodeStore {
	return &mockNodeStore{
		nodes:  make(map[string]*node.Node),
		tokens: make(map[string]string),
		ram:    make(map[string]int),
	}
}

func (m *mockNodeStore) GetByID(_ context.Context, id string) (*node.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return nil, nil
	}
	return n, nil
}

func (m *mockNodeStore) GetEncryptedTokenByID(_ context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[id], nil
}

func (m *mockNodeStore) ReleaseRAM(_ context.Context, nodeID string, ramMB int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ram[nodeID] -= ramMB
	if m.ram[nodeID] < 0 {
		m.ram[nodeID] = 0
	}
	return nil
}

type mockTenantStore struct {
	mu              sync.Mutex
	statuses        map[string]string
	lxcIDs          map[string]int
	errors          map[string]string
	tenants         map[string]*tenant.Tenant
	dashboardTokens map[string]string

	setDeletingErr error
}

func newMockTenantStore() *mockTenantStore {
	return &mockTenantStore{
		statuses:        make(map[string]string),
		lxcIDs:          make(map[string]int),
		errors:          make(map[string]string),
		tenants:         make(map[string]*tenant.Tenant),
		dashboardTokens: make(map[string]string),
	}
}

func (m *mockTenantStore) SetActive(_ context.Context, id string, lxcID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[id] = "active"
	m.lxcIDs[id] = lxcID
	return nil
}

func (m *mockTenantStore) SetError(_ context.Context, id string, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[id] = "error"
	m.errors[id] = errMsg
	return nil
}

func (m *mockTenantStore) SetDeleting(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setDeletingErr != nil {
		return m.setDeletingErr
	}
	m.statuses[id] = "deleting"
	return nil
}

func (m *mockTenantStore) SetDeleted(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[id] = "deleted"
	return nil
}

func (m *mockTenantStore) SetLXCIP(_ context.Context, id string, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// store for verification
	return nil
}

func (m *mockTenantStore) GetNextAvailableIP(_ context.Context, cidr string) (string, error) {
	return "10.10.10.5", nil
}

func (m *mockTenantStore) SetHealthStatus(_ context.Context, id string, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return nil
}

func (m *mockTenantStore) GetByID(_ context.Context, id string) (*tenant.Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tenants[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *mockTenantStore) SetDashboardToken(_ context.Context, id string, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dashboardTokens[id] = token
	return nil
}

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

// --- Mock station creator ---

type mockStationCreator struct {
	mu      sync.Mutex
	calls   []stationCreateCall
	err     error
}

type stationCreateCall struct {
	TenantID    string
	Name        string
	Subdomain   string
	OwnerID     string
	CaddyDomain string
}

func (m *mockStationCreator) AutoCreateStation(_ context.Context, tenantID, name, subdomain, ownerID, caddyDomain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, stationCreateCall{
		TenantID:    tenantID,
		Name:        name,
		Subdomain:   subdomain,
		OwnerID:     ownerID,
		CaddyDomain: caddyDomain,
	})
	return m.err
}

// --- Mock task (implements Waiter) ---

type mockWaiter struct {
	err error
}

func (w *mockWaiter) Wait(_ context.Context, _ ...proxmox.WaitOption) error {
	return w.err
}

// --- Mock Proxmox client (implements ProxmoxClient with Waiter return) ---

type mockProxmoxClient struct {
	mu                    sync.Mutex
	nextID                int
	nextIDErr             error
	cloneErr              error
	cloneWaitErr          error
	startErr              error
	startWaitErr          error
	stopErr               error
	stopWaitErr           error
	deleteErr             error
	deleteWaitErr         error
	mountPointsErr        error
	cloneCalled           bool
	startCalled           bool
	stopCalled            bool
	deleteCalled          bool
	mountPointsCalled     bool
	deletedIDs            []int
	mountPointsReceived   map[string]string
}

func (m *mockProxmoxClient) GetNextID(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextIDErr != nil {
		return 0, m.nextIDErr
	}
	return m.nextID, nil
}

func (m *mockProxmoxClient) CloneContainer(_ context.Context, _ int, _ proxmox.CloneOptions) (Waiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cloneCalled = true
	if m.cloneErr != nil {
		return nil, m.cloneErr
	}
	return &mockWaiter{err: m.cloneWaitErr}, nil
}

func (m *mockProxmoxClient) StartContainer(_ context.Context, _ int) (Waiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startCalled = true
	if m.startErr != nil {
		return nil, m.startErr
	}
	return &mockWaiter{err: m.startWaitErr}, nil
}

func (m *mockProxmoxClient) StopContainer(_ context.Context, _ int) (Waiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalled = true
	if m.stopErr != nil {
		return nil, m.stopErr
	}
	return &mockWaiter{err: m.stopWaitErr}, nil
}

func (m *mockProxmoxClient) DeleteContainer(_ context.Context, vmid int, _ bool) (Waiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalled = true
	m.deletedIDs = append(m.deletedIDs, vmid)
	if m.deleteErr != nil {
		return nil, m.deleteErr
	}
	return &mockWaiter{err: m.deleteWaitErr}, nil
}

func (m *mockProxmoxClient) ConfigureNetwork(_ context.Context, _ int, _ string) error {
	return nil
}

func (m *mockProxmoxClient) ConfigureMountPoints(_ context.Context, _ int, mounts map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mountPointsCalled = true
	m.mountPointsReceived = mounts
	return m.mountPointsErr
}

// --- Helpers ---

func testProject() *project.Project {
	return &project.Project{
		ID:          "proj-1",
		Name:        "test-project",
		TemplateID:  100,
		RAMMB:       1536,
		HealthPath:  "/api/health",
		NetworkCIDR: "10.10.10.0/24",
		Gateway:     "10.10.10.1",
		Ports:       []int{80},
		CreatedAt:   time.Now(),
	}
}

func testNode() *node.Node {
	return &node.Node{
		ID:             "node-1",
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

// setupProvisioner creates a provisioner with a pre-cached mock client.
func setupProvisioner(nodeStore *mockNodeStore, tenantStore *mockTenantStore, projectStore *mockProjectStore, mockClient *mockProxmoxClient, nodeID string) *Provisioner {
	p := New(nodeStore, tenantStore, projectStore, "test-key")
	p.mu.Lock()
	p.clients[nodeID] = mockClient
	p.mu.Unlock()
	return p
}

// waitForProvision calls Provision and waits for the goroutine to complete.
func waitForProvision(p *Provisioner, tenantID, nodeID, projectID, subdomain string, ramMB int) {
	p.Provision(tenantID, nodeID, projectID, subdomain, ramMB)
	p.wg.Wait()
}

// --- Provision tests ---

func TestProvision_HappyPath(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active', got %q", tenantStore.statuses["tenant-1"])
	}
	if tenantStore.lxcIDs["tenant-1"] != 105 {
		t.Errorf("expected lxc_id 105, got %d", tenantStore.lxcIDs["tenant-1"])
	}

	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.cloneCalled {
		t.Error("expected clone to be called")
	}
	if !mockClient.mountPointsCalled {
		t.Error("expected mount points to be configured")
	}
	if mp0, ok := mockClient.mountPointsReceived["mp0"]; !ok || mp0 != "/mnt/tenants/105/visuals,mp=/root/freeRadio/content/visuals" {
		t.Errorf("unexpected mp0: %q", mp0)
	}
	if mp1, ok := mockClient.mountPointsReceived["mp1"]; !ok || mp1 != "/mnt/tenants/105/music,mp=/root/freeRadio/content/music" {
		t.Errorf("unexpected mp1: %q", mp1)
	}
	if !mockClient.startCalled {
		t.Error("expected start to be called")
	}
}

func TestProvision_AutoCreatesStation(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	ownerID := "owner-123"
	tenantStore.tenants["tenant-1"] = &tenant.Tenant{
		ID:      "tenant-1",
		Name:    "My Station",
		OwnerID: &ownerID,
	}

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	sc := &mockStationCreator{}
	p.WithStationCreator(sc, "freeradio.app")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "my-station", proj.RAMMB)

	tenantStore.mu.Lock()
	status := tenantStore.statuses["tenant-1"]
	tenantStore.mu.Unlock()

	if status != "active" {
		t.Fatalf("expected tenant status 'active', got %q", status)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if len(sc.calls) != 1 {
		t.Fatalf("expected 1 station create call, got %d", len(sc.calls))
	}
	call := sc.calls[0]
	if call.TenantID != "tenant-1" {
		t.Errorf("tenant_id = %q, want 'tenant-1'", call.TenantID)
	}
	if call.Name != "My Station" {
		t.Errorf("name = %q, want 'My Station'", call.Name)
	}
	if call.Subdomain != "my-station" {
		t.Errorf("subdomain = %q, want 'my-station'", call.Subdomain)
	}
	if call.OwnerID != "owner-123" {
		t.Errorf("owner_id = %q, want 'owner-123'", call.OwnerID)
	}
	if call.CaddyDomain != "freeradio.app" {
		t.Errorf("caddy_domain = %q, want 'freeradio.app'", call.CaddyDomain)
	}
}

func TestProvision_StationCreatorError_DoesNotFailProvisioning(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	tenantStore.tenants["tenant-1"] = &tenant.Tenant{
		ID:   "tenant-1",
		Name: "Failing Station",
	}

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	sc := &mockStationCreator{err: fmt.Errorf("db error")}
	p.WithStationCreator(sc, "freeradio.app")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "fail-station", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	// Provisioning should still succeed
	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestProvision_NoStationCreator_NoPanic(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)
	// No WithStationCreator — should not panic

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestProvision_GetNextIDError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB // simulate pre-reserved

	mockClient := &mockProxmoxClient{nextIDErr: errors.New("proxmox unreachable")}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}
	if tenantStore.errors["tenant-1"] == "" {
		t.Error("expected error message to be set")
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestProvision_ErrorMessageSanitized(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{nextIDErr: errors.New("proxmox api 500 Internal Server Error: connection refused to 10.0.0.1:8006")}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	// Error message should be sanitized — no internal details
	errMsg := tenantStore.errors["tenant-1"]
	if errMsg != "provisioning failed: could not allocate container ID" {
		t.Errorf("expected sanitized error message, got %q", errMsg)
	}
}

func TestProvision_CloneError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		nextID:   105,
		cloneErr: errors.New("clone failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestProvision_CloneWaitError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		nextID:       105,
		cloneWaitErr: errors.New("clone task timed out"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestProvision_MountPointsError_TriggersCleanup(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		nextID:         105,
		mountPointsErr: errors.New("mount point config failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	// Cleanup should have attempted to delete the container
	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called for cleanup")
	}
	if len(mockClient.deletedIDs) != 1 || mockClient.deletedIDs[0] != 105 {
		t.Errorf("expected delete of LXC 105, got %v", mockClient.deletedIDs)
	}

	// Start should NOT have been called
	if mockClient.startCalled {
		t.Error("start should not be called after mount point failure")
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestProvision_StartError_TriggersCleanup(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		nextID:   105,
		startErr: errors.New("start failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	// Cleanup should have attempted to delete the container
	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called for cleanup")
	}
	if len(mockClient.deletedIDs) != 1 || mockClient.deletedIDs[0] != 105 {
		t.Errorf("expected delete of LXC 105, got %v", mockClient.deletedIDs)
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestProvision_StartWaitError_TriggersCleanup(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		nextID:       105,
		startWaitErr: errors.New("start task failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called for cleanup after start wait failure")
	}
}

func TestProvision_ProjectNotFound(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	n := testNode()
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	waitForProvision(p, "tenant-1", n.ID, "nonexistent-proj", "myapp", 1536)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}
}

// --- Deprovision tests ---

func TestDeprovision_HappyPath(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}

	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.stopCalled {
		t.Error("expected stop to be called")
	}
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called")
	}

	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestDeprovision_AlreadyStopped(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		stopErr: errors.New("already stopped"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}

	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called even after stop error")
	}
}

func TestDeprovision_StopWaitError_ContinuesWithDelete(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		stopWaitErr: errors.New("stop task failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestDeprovision_DeleteError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		deleteErr: errors.New("delete failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err == nil {
		t.Fatal("expected error from delete failure")
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	// RAM should NOT be released (manual investigation needed since container may still exist)
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != proj.RAMMB {
		t.Errorf("expected ram NOT to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestDeprovision_DeleteWaitError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{
		deleteWaitErr: errors.New("delete task failed"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err == nil {
		t.Fatal("expected error from delete wait failure")
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestDeprovision_StateConflict(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	n := testNode()
	nodeStore.nodes[n.ID] = n

	// Simulate SetDeleting returning state conflict (already being deleted)
	tenantStore.setDeletingErr = tenant.ErrStateConflict

	mockClient := &mockProxmoxClient{}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, 1536)
	if err == nil {
		t.Fatal("expected error from state conflict")
	}

	// Stop/delete should NOT have been called
	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if mockClient.stopCalled {
		t.Error("stop should not be called on state conflict")
	}
	if mockClient.deleteCalled {
		t.Error("delete should not be called on state conflict")
	}
}

// --- Client caching tests ---

func TestGetClient_Caching(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	cachedClient := &mockProxmoxClient{}
	p := New(nodeStore, tenantStore, projectStore, "test-key")
	p.mu.Lock()
	p.clients["node-1"] = cachedClient
	p.mu.Unlock()

	// First call — should use cached client
	c1, err := p.getClient(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c1 != cachedClient {
		t.Error("expected cached client")
	}

	// Second call — still cached
	c2, err := p.getClient(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c1 != c2 {
		t.Error("expected same cached client on subsequent call")
	}
}

func TestGetClient_NodeNotFound(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	p := New(nodeStore, tenantStore, projectStore, "test-key")

	_, err := p.getClient(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestGetClient_EmptyToken(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	n := testNode()
	nodeStore.nodes[n.ID] = n
	nodeStore.tokens[n.ID] = "" // empty token

	p := New(nodeStore, tenantStore, projectStore, "test-key")

	_, err := p.getClient(context.Background(), n.ID)
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

// --- CaddyClient tests ---

type mockCaddyClient struct {
	mu              sync.Mutex
	addedRoutes     map[string]string
	removedRoutes   []string
	removeRouteErr  error
}

func newMockCaddyClient() *mockCaddyClient {
	return &mockCaddyClient{
		addedRoutes: make(map[string]string),
	}
}

func (m *mockCaddyClient) AddRoute(_ context.Context, subdomain, targetIP string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addedRoutes[subdomain] = targetIP
	return nil
}

func (m *mockCaddyClient) RemoveRoute(_ context.Context, subdomain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedRoutes = append(m.removedRoutes, subdomain)
	return m.removeRouteErr
}

func TestDeprovision_WithCaddyClient_RemovesRoute(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	caddyMock := newMockCaddyClient()
	p.WithCaddyClient(caddyMock)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "mystudio", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	caddyMock.mu.Lock()
	defer caddyMock.mu.Unlock()
	if len(caddyMock.removedRoutes) != 1 || caddyMock.removedRoutes[0] != "mystudio" {
		t.Errorf("expected RemoveRoute('mystudio'), got %v", caddyMock.removedRoutes)
	}
}

func TestDeprovision_CaddyClientError_DoesNotFail(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	caddyMock := newMockCaddyClient()
	caddyMock.removeRouteErr = fmt.Errorf("caddy unreachable")
	p.WithCaddyClient(caddyMock)

	// Deprovision should succeed even if Caddy fails
	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "mystudio", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()
	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestDeprovision_NilCaddyClient_NoPanic(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)
	// No WithCaddyClient — caddyClient is nil

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "mystudio", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()
	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}
}

// --- Semaphore / graceful shutdown test ---

func TestProvision_BoundedConcurrency(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	// Launch multiple provisions
	for i := 0; i < 5; i++ {
		p.Provision("tenant-"+string(rune('a'+i)), n.ID, proj.ID, "app"+string(rune('a'+i)), proj.RAMMB)
	}

	// Wait for all to complete
	p.Shutdown()

	// All should have completed (semaphore didn't deadlock)
	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()
	for i := 0; i < 5; i++ {
		id := "tenant-" + string(rune('a'+i))
		if tenantStore.statuses[id] != "active" {
			t.Errorf("tenant %s: expected status 'active', got %q", id, tenantStore.statuses[id])
		}
	}
}

// --- Deprovision: container not found tests ---

func TestDeprovision_ContainerNotFound_DeleteReturnsNotFound(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	// Simulate Proxmox returning "does not exist" when container is already gone
	mockClient := &mockProxmoxClient{
		deleteErr: &proxmox.APIError{
			StatusCode: 500,
			Status:     "500 Internal Server Error",
			Errors:     map[string]string{"vmid": "CT 105 does not exist"},
		},
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("expected no error when container already gone, got: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestDeprovision_ContainerNotFound_OnWait(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	// DeleteContainer succeeds but Wait returns "not found" (task reports container gone)
	mockClient := &mockProxmoxClient{
		deleteWaitErr: &proxmox.TaskError{
			UPID:       "UPID:node:001:task",
			ExitStatus: "ERROR: CT 105 does not exist",
			Type:       "vzdestroy",
		},
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("expected no error when container gone during wait, got: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestDeprovision_ContainerNotFound_GenericError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	// Generic "not found" error string (not APIError)
	mockClient := &mockProxmoxClient{
		deleteErr: fmt.Errorf("container not found"),
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err != nil {
		t.Fatalf("expected no error when container not found, got: %v", err)
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "deleted" {
		t.Errorf("expected tenant status 'deleted', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestDeprovision_RealDeleteError_StillFails(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	// A real error (not "not found") should still fail
	mockClient := &mockProxmoxClient{
		deleteErr: &proxmox.APIError{
			StatusCode: 500,
			Status:     "500 Internal Server Error",
			Errors:     map[string]string{"node": "connection refused"},
		},
	}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, "myapp", 105, proj.RAMMB)
	if err == nil {
		t.Fatal("expected error for real delete failure")
	}

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestIsContainerNotFound(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"does not exist", fmt.Errorf("CT 105 does not exist"), true},
		{"not found", fmt.Errorf("container not found"), true},
		{"no such container", fmt.Errorf("no such container 105"), true},
		{"proxmox api error with does not exist", &proxmox.APIError{
			StatusCode: 500,
			Status:     "500 Internal Server Error",
			Errors:     map[string]string{"vmid": "CT 105 does not exist"},
		}, true},
		{"task error with not exist", &proxmox.TaskError{
			UPID:       "UPID:node:001",
			ExitStatus: "ERROR: CT 105 does not exist",
			Type:       "vzdestroy",
		}, true},
		{"unrelated error", fmt.Errorf("connection refused"), false},
		{"proxmox timeout", &proxmox.APIError{
			StatusCode: 504,
			Status:     "504 Gateway Timeout",
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContainerNotFound(tt.err)
			if got != tt.expected {
				t.Errorf("isContainerNotFound(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}
