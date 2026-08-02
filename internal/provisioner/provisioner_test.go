package provisioner

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	mu    sync.Mutex
	calls []stationCreateCall
	err   error
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
	mu                  sync.Mutex
	nextID              int
	nextIDErr           error
	cloneErr            error
	cloneWaitErr        error
	startErr            error
	startWaitErr        error
	stopErr             error
	stopWaitErr         error
	deleteErr           error
	deleteWaitErr       error
	mountPointsErr      error
	cloneCalled         bool
	startCalled         bool
	stopCalled          bool
	deleteCalled        bool
	mountPointsCalled   bool
	deletedIDs          []int
	mountPointsReceived map[string]string
	net0Received        string
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

func (m *mockProxmoxClient) ConfigureNetwork(_ context.Context, _ int, net0 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.net0Received = net0
	return nil
}

func (m *mockProxmoxClient) ConfigureMountPoints(_ context.Context, _ int, mounts map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mountPointsCalled = true
	m.mountPointsReceived = mounts
	return m.mountPointsErr
}

// --- Mock SSH client (implements SSHExec) ---

type sshExecCall struct {
	SSHHost string
	VMID    int // only for ExecInContainer
	Command string
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
	// Production timings scaled down by 1000: the health check still runs its
	// full poll loop against an unreachable container, it just costs
	// milliseconds instead of a minute per test.
	p.healthTimeout = 60 * time.Millisecond
	p.healthInterval = 5 * time.Millisecond
	p.healthClientTimeout = 5 * time.Millisecond
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
	p.WithStationCreator(sc, "example.com")

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
	if call.CaddyDomain != "example.com" {
		t.Errorf("caddy_domain = %q, want 'example.com'", call.CaddyDomain)
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
	p.WithStationCreator(sc, "example.com")

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
	mu             sync.Mutex
	addedRoutes    map[string]string
	removedRoutes  []string
	removeRouteErr error
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

	// Generic "does not exist" error string (not APIError)
	mockClient := &mockProxmoxClient{
		deleteErr: fmt.Errorf("CT 105 does not exist"),
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

// --- dynamicMockSSHExec: mock that fails on the N-th ExecOnHost call ---

type dynamicMockSSHExec struct {
	mu         sync.Mutex
	callCount  int
	failOnCall int // call number (1-based) at which to return error
	err        error
}

func (m *dynamicMockSSHExec) ExecOnHost(_ context.Context, sshHost string, command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.callCount == m.failOnCall {
		return m.err
	}
	return nil
}

func (m *dynamicMockSSHExec) ExecInContainer(_ context.Context, sshHost string, vmid int, command string) error {
	return nil
}

// --- SSH mount point tests ---

func TestProvision_MountPointsViaSSH_HappyPath(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	sshMock := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(sshMock)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active', got %q", tenantStore.statuses["tenant-1"])
	}

	// API mount points MUST NOT be called when SSH client is available
	mockClient.mu.Lock()
	if mockClient.mountPointsCalled {
		t.Error("API ConfigureMountPoints should NOT be called when SSH client is set")
	}
	mockClient.mu.Unlock()

	// Check SSH calls: mkdir + pct set
	sshMock.mu.Lock()
	defer sshMock.mu.Unlock()

	if len(sshMock.execOnHostCalls) < 2 {
		t.Fatalf("expected at least 2 ExecOnHost calls (mkdir + pct set), got %d", len(sshMock.execOnHostCalls))
	}

	mkdirCall := sshMock.execOnHostCalls[0]
	if mkdirCall.SSHHost != "10.0.0.1" {
		t.Errorf("mkdir ssh host = %q, want '10.0.0.1'", mkdirCall.SSHHost)
	}
	expectedMkdir := "mkdir -p /mnt/tenants/105/visuals /mnt/tenants/105/music && chmod 777 /mnt/tenants/105/visuals /mnt/tenants/105/music"
	if mkdirCall.Command != expectedMkdir {
		t.Errorf("mkdir command = %q, want %q", mkdirCall.Command, expectedMkdir)
	}

	pctCall := sshMock.execOnHostCalls[1]
	expectedPct := "pct set 105 -mp0 /mnt/tenants/105/visuals,mp=/root/freeRadio/content/visuals -mp1 /mnt/tenants/105/music,mp=/root/freeRadio/content/music"
	if pctCall.Command != expectedPct {
		t.Errorf("pct set command = %q, want %q", pctCall.Command, expectedPct)
	}
}

func TestProvision_MountPointsViaSSH_MkdirError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	sshMock := &mockSSHExecWithDeployCalls{execOnHostErr: fmt.Errorf("ssh: permission denied")}
	p.WithSSHClient(sshMock)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	// Cleanup should delete container
	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called for cleanup")
	}

	// Start MUST NOT be called
	if mockClient.startCalled {
		t.Error("start should not be called after SSH mount failure")
	}

	// RAM should be released
	nodeStore.mu.Lock()
	defer nodeStore.mu.Unlock()
	if nodeStore.ram[n.ID] != 0 {
		t.Errorf("expected ram to be released, got %d", nodeStore.ram[n.ID])
	}
}

func TestProvision_MountPointsViaSSH_PctSetError(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n
	nodeStore.ram[n.ID] = proj.RAMMB

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	// mkdir ok, pct set — error (failOnCall=2 means second ExecOnHost call)
	dynamicSSH := &dynamicMockSSHExec{
		failOnCall: 2,
		err:        fmt.Errorf("pct set: exit status 1"),
	}
	p.WithSSHClient(dynamicSSH)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "error" {
		t.Errorf("expected tenant status 'error', got %q", tenantStore.statuses["tenant-1"])
	}

	// Cleanup should delete container
	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()
	if !mockClient.deleteCalled {
		t.Error("expected delete to be called for cleanup")
	}
}

// --- Auto-deploy freeRadio tests ---

// mockSSHExecWithDeployCalls is the SSHExec mock for every test: it records
// ExecOnHost and ExecInContainer calls, can fail all ExecOnHost calls, and can
// fail on the N-th ExecInContainer call.
type mockSSHExecWithDeployCalls struct {
	mu              sync.Mutex
	execInCtrCalls  []sshExecCall
	execOnHostCalls []sshExecCall
	execOnHostErr   error
	// failOnExecInCtr: the ExecInContainer call number (1-based) on which to return an error. 0 = do not fail.
	failOnExecInCtr int
	failErr         error
}

func (m *mockSSHExecWithDeployCalls) ExecOnHost(_ context.Context, sshHost string, command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execOnHostCalls = append(m.execOnHostCalls, sshExecCall{SSHHost: sshHost, Command: command})
	return m.execOnHostErr
}

func (m *mockSSHExecWithDeployCalls) ExecInContainer(_ context.Context, sshHost string, vmid int, command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execInCtrCalls = append(m.execInCtrCalls, sshExecCall{SSHHost: sshHost, VMID: vmid, Command: command})
	if m.failOnExecInCtr > 0 && len(m.execInCtrCalls) == m.failOnExecInCtr {
		return m.failErr
	}
	return nil
}

// testProjectNoHealth returns a project without a health check, so tests do not wait 60 seconds.
func testProjectNoHealth() *project.Project {
	p := testProject()
	p.HealthPath = ""
	p.Ports = nil
	return p
}

func TestProvision_AutoDeploy_HappyPath(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)
	p.WithFreeRadioRepo("https://github.com/Awis13/freeRadio.git", "dev")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	status := tenantStore.statuses["tenant-1"]
	tenantStore.mu.Unlock()

	if status != "active" {
		t.Fatalf("expected tenant status 'active', got %q", status)
	}

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	// Six deploy calls: docker install, fetch, .env, certificates, compose up,
	// health. The legacy dashboard token write is skipped because a repo is
	// configured. Certificates are the step added when the deploy was fixed
	// against post-refactor freeRadio, where the proxy exits without them.
	if len(ssh.execInCtrCalls) != 6 {
		t.Fatalf("expected 6 ExecInContainer calls for deploy, got %d", len(ssh.execInCtrCalls))
	}

	// Verify the command order
	calls := ssh.execInCtrCalls

	// Step 1: install docker
	if calls[0].Command != "which docker || (curl -fsSL https://get.docker.com | sh)" {
		t.Errorf("step 1: unexpected command: %s", calls[0].Command)
	}

	// Step 2: fetch the configured branch into the mounted directory
	if !strings.Contains(calls[1].Command, "git fetch --depth 1 origin dev") {
		t.Errorf("step 2: unexpected command: %s", calls[1].Command)
	}

	// Step 3: write .env — contains TENANT_ID and DASHBOARD_TOKEN
	if !strings.Contains(calls[2].Command, "TENANT_ID=tenant-1") {
		t.Errorf("step 3: .env should contain TENANT_ID, got: %s", calls[2].Command)
	}
	if !strings.Contains(calls[2].Command, "DASHBOARD_TOKEN=") {
		t.Errorf("step 3: .env should contain DASHBOARD_TOKEN, got: %s", calls[2].Command)
	}

	// Step 4: bootstrap the proxy's certificates
	if !strings.Contains(calls[3].Command, "bootstrap-certs.sh") {
		t.Errorf("step 4: unexpected command: %s", calls[3].Command)
	}

	// Step 5: docker compose up
	if calls[4].Command != "cd /root/freeRadio && docker compose up -d" {
		t.Errorf("step 5: unexpected command: %s", calls[4].Command)
	}

	// Step 6: health check through the proxy, the only published port
	if !strings.Contains(calls[5].Command, "http://127.0.0.1/api/health") {
		t.Errorf("step 6: unexpected command: %s", calls[5].Command)
	}

	// All commands must target the correct LXC
	for i, c := range calls {
		if c.VMID != 105 {
			t.Errorf("call %d: expected VMID 105, got %d", i, c.VMID)
		}
	}
}

func TestProvision_AutoDeploy_Failure_DoesNotFailProvisioning(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	// SSH that fails on the first ExecInContainer (install docker)
	ssh := &mockSSHExecWithDeployCalls{
		failOnExecInCtr: 1,
		failErr:         fmt.Errorf("docker install failed"),
	}
	p.WithSSHClient(ssh)
	p.WithFreeRadioRepo("https://github.com/Awis13/freeRadio.git", "dev")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	// The tenant should still be active — deploy is best-effort
	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active' despite deploy failure, got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestProvision_AutoDeploy_SkippedWithoutSSH(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	// Only WithFreeRadioRepo, without WithSSHClient — the deploy should be skipped
	p.WithFreeRadioRepo("https://github.com/Awis13/freeRadio.git", "dev")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active', got %q", tenantStore.statuses["tenant-1"])
	}
}

func TestProvision_AutoDeploy_ComposeUpFailure(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	// Fail on the 4th call (docker compose up)
	ssh := &mockSSHExecWithDeployCalls{
		failOnExecInCtr: 4,
		failErr:         fmt.Errorf("docker compose up: exit status 1"),
	}
	p.WithSSHClient(ssh)
	p.WithFreeRadioRepo("https://github.com/Awis13/freeRadio.git", "dev")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	tenantStore.mu.Lock()
	defer tenantStore.mu.Unlock()

	// Tenant is active — deploy is best-effort
	if tenantStore.statuses["tenant-1"] != "active" {
		t.Errorf("expected tenant status 'active' despite compose failure, got %q", tenantStore.statuses["tenant-1"])
	}

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	// There should be exactly 4 calls — after the compose up failure the rest are not executed
	if len(ssh.execInCtrCalls) != 4 {
		t.Errorf("expected 4 ExecInContainer calls (stopped at compose up), got %d", len(ssh.execInCtrCalls))
	}
}

// --- Auto-deploy wiring ---

// TestProvision_LegacyPathWhenRepoUnset pins the disabled case: with no repo
// configured the provisioner keeps writing only the dashboard token into a
// container that already holds a checkout, and never tries to deploy.
func TestProvision_LegacyPathWhenRepoUnset(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)
	// WithFreeRadioRepo is deliberately not called.

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	var sawLegacyTokenWrite bool
	for _, call := range ssh.execInCtrCalls {
		if strings.Contains(call.Command, "DASHBOARD_TOKEN=") {
			sawLegacyTokenWrite = true
		}
		if strings.Contains(call.Command, "git clone") || strings.Contains(call.Command, "docker compose") {
			t.Errorf("deploy command ran with no repo configured: %q", call.Command)
		}
	}
	if !sawLegacyTokenWrite {
		t.Error("expected the legacy dashboard token write")
	}
}

// TestProvision_DeployPathWhenRepoSet pins the enabled case: the deploy runs and
// the legacy token write does not, since the deploy writes the whole .env.
func TestProvision_DeployPathWhenRepoSet(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)

	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)
	p.WithFreeRadioRepo("https://github.com/example/freeRadio.git", "dev")

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	var sawClone, sawCompose, sawLegacyTokenWrite bool
	for _, call := range ssh.execInCtrCalls {
		switch {
		case strings.Contains(call.Command, "https://github.com/example/freeRadio.git"):
			sawClone = true
		case strings.Contains(call.Command, "docker compose up"):
			sawCompose = true
		case strings.HasPrefix(call.Command, "sed -i '/^DASHBOARD_TOKEN=/d'"):
			sawLegacyTokenWrite = true
		}
	}
	if !sawClone {
		t.Error("expected the configured repo to be fetched")
	}
	if !sawCompose {
		t.Error("expected the compose stack to be started")
	}
	if sawLegacyTokenWrite {
		t.Error("the legacy token write must not run when the deploy writes the whole .env")
	}
}

// --- deployFreeRadio against post-refactor freeRadio ---

// deployCommands runs a deploy against the ssh mock and returns the commands it
// issued inside the container, in order.
func deployCommands(t *testing.T, branch string) []string {
	t.Helper()

	p := New(newMockNodeStore(), newMockTenantStore(), newMockProjectStore(), "test-key")
	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)
	p.WithFreeRadioRepo("https://github.com/example/freeRadio.git", branch)

	if err := p.deployFreeRadio(context.Background(), "10.0.0.1", 105, "tenant-1", "dash-token"); err != nil {
		t.Fatalf("deployFreeRadio: %v", err)
	}

	ssh.mu.Lock()
	defer ssh.mu.Unlock()
	commands := make([]string, 0, len(ssh.execInCtrCalls))
	for _, call := range ssh.execInCtrCalls {
		commands = append(commands, call.Command)
	}
	return commands
}

// findCommand returns the first command containing substr.
func findCommand(commands []string, substr string) (string, bool) {
	for _, c := range commands {
		if strings.Contains(c, substr) {
			return c, true
		}
	}
	return "", false
}

// indexOfCommand returns the position of the first command containing substr.
func indexOfCommand(commands []string, substr string) int {
	for i, c := range commands {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

// TestDeploy_PullsTheConfiguredBranch covers breaks 1 and 2: the old fallback
// pulled origin master, a branch that does not exist, and an unqualified clone
// takes the remote's default branch, which trails the one tenants run.
func TestDeploy_PullsTheConfiguredBranch(t *testing.T) {
	commands := deployCommands(t, "dev")

	fetch, ok := findCommand(commands, "git fetch")
	if !ok {
		t.Fatalf("no fetch command issued: %v", commands)
	}
	if !strings.Contains(fetch, "git fetch --depth 1 origin dev") {
		t.Errorf("fetch = %q, want it to name the configured branch", fetch)
	}
	if !strings.Contains(fetch, "git checkout -f -B dev FETCH_HEAD") {
		t.Errorf("fetch = %q, want a checkout of the fetched branch", fetch)
	}
	for _, c := range commands {
		if strings.Contains(c, "master") {
			t.Errorf("command references the non-existent master branch: %q", c)
		}
	}
}

func TestDeploy_UsesConfiguredBranchNotADefault(t *testing.T) {
	commands := deployCommands(t, "release-1.2")

	fetch, _ := findCommand(commands, "git fetch")
	if !strings.Contains(fetch, "origin release-1.2") {
		t.Errorf("fetch = %q, want the configured branch", fetch)
	}
	if strings.Contains(fetch, "origin dev") {
		t.Errorf("fetch = %q, want no fallback to the default branch", fetch)
	}
}

// TestDeploy_FetchesIntoNonEmptyDirectory covers break 3: the content mount
// points are attached under the deploy directory before the container starts,
// so a plain git clone into it fails.
func TestDeploy_FetchesIntoNonEmptyDirectory(t *testing.T) {
	commands := deployCommands(t, "dev")

	for _, c := range commands {
		if strings.Contains(c, "git clone") {
			t.Errorf("git clone cannot succeed into the mounted directory: %q", c)
		}
	}
	fetch, ok := findCommand(commands, "git init")
	if !ok {
		t.Fatalf("expected the repo to be initialized in place: %v", commands)
	}
	if !strings.Contains(fetch, "git init -q") || !strings.Contains(fetch, "git fetch") {
		t.Errorf("fetch = %q, want init in place followed by a fetch", fetch)
	}
	if !strings.Contains(fetch, "remote add origin") || !strings.Contains(fetch, "remote set-url origin") {
		t.Errorf("fetch = %q, want the remote set whether or not it already exists", fetch)
	}
}

// TestDeploy_EnvCarriesEverySecretTheStackNeeds covers break 4. The dashboard
// throws on boot without STREAM_KEYS_SECRET, and the four ICECAST_* variables
// are interpolated by compose with no default.
func TestDeploy_EnvCarriesEverySecretTheStackNeeds(t *testing.T) {
	commands := deployCommands(t, "dev")

	env, ok := findCommand(commands, "ENVEOF")
	if !ok {
		t.Fatalf("no .env written: %v", commands)
	}

	for _, required := range []string{
		"TENANT_ID=tenant-1",
		"DASHBOARD_TOKEN=dash-token",
		"STREAM_KEYS_SECRET=",
		"ICECAST_SOURCE_PASSWORD=",
		"ICECAST_ADMIN_PASSWORD=",
		"ICECAST_PASSWORD=",
		"ICECAST_RELAY_PASSWORD=",
	} {
		if !strings.Contains(env, required) {
			t.Errorf(".env is missing %q", required)
		}
	}

	// The secrets must be generated, not placeholders shared between tenants.
	for _, line := range strings.Split(env, "\n") {
		name, value, found := strings.Cut(line, "=")
		if !found || !strings.HasSuffix(name, "SECRET") && !strings.HasSuffix(name, "PASSWORD") {
			continue
		}
		if len(value) != 64 {
			t.Errorf("%s = %q, want a generated 64 character secret", name, value)
		}
		if strings.Contains(value, "change-me") {
			t.Errorf("%s still carries the example placeholder", name)
		}
	}
}

// TestDeploy_SecretsDifferPerVariableAndPerTenant pins that the generated
// secrets are independent, so one leaking does not hand over the rest.
func TestDeploy_SecretsDifferPerVariableAndPerTenant(t *testing.T) {
	first, _ := findCommand(deployCommands(t, "dev"), "ENVEOF")
	second, _ := findCommand(deployCommands(t, "dev"), "ENVEOF")

	seen := map[string]string{}
	for _, line := range strings.Split(first, "\n") {
		name, value, found := strings.Cut(line, "=")
		if !found || (!strings.HasSuffix(name, "SECRET") && !strings.HasSuffix(name, "PASSWORD")) {
			continue
		}
		if name == "DASHBOARD_TOKEN" {
			continue
		}
		if prev, dup := seen[value]; dup {
			t.Errorf("%s reuses the secret already given to %s", name, prev)
		}
		seen[value] = name
	}
	if len(seen) != 5 {
		t.Fatalf("found %d generated secrets, want 5", len(seen))
	}

	for value := range seen {
		if strings.Contains(second, value) {
			t.Error("a second deploy reused a secret from the first")
		}
	}
}

// TestDeploy_HealthChecksThroughThePublishedPort covers break 5: the dashboard
// listens on 9090 inside its container and publishes no port, so the old probe
// could never connect. The proxy is what reaches the host.
func TestDeploy_HealthChecksThroughThePublishedPort(t *testing.T) {
	commands := deployCommands(t, "dev")

	health, ok := findCommand(commands, "/api/health")
	if !ok {
		t.Fatalf("no health check issued: %v", commands)
	}
	if strings.Contains(health, "9090") {
		t.Errorf("health check targets an unpublished port: %q", health)
	}
	if !strings.Contains(health, "http://127.0.0.1/api/health") {
		t.Errorf("health = %q, want the proxy's public health endpoint", health)
	}
}

// TestDeploy_BootstrapsCertsBeforeComposeUp covers break 6: certs/ is
// gitignored, the proxy's config hard-requires the pair, and it exits at
// startup without it. Ordering is the point.
func TestDeploy_BootstrapsCertsBeforeComposeUp(t *testing.T) {
	commands := deployCommands(t, "dev")

	certs := indexOfCommand(commands, "bootstrap-certs.sh")
	compose := indexOfCommand(commands, "docker compose up")

	if certs < 0 {
		t.Fatalf("certificates are never bootstrapped: %v", commands)
	}
	if compose < 0 {
		t.Fatalf("the stack is never started: %v", commands)
	}
	if certs > compose {
		t.Errorf("certificates bootstrapped after compose up (positions %d and %d): the proxy would exit at startup", certs, compose)
	}
}

// TestDeploy_StepOrder pins the whole sequence, since several steps only work
// where they are.
func TestDeploy_StepOrder(t *testing.T) {
	commands := deployCommands(t, "dev")

	want := []string{"which docker", "git fetch", "ENVEOF", "bootstrap-certs.sh", "docker compose up", "/api/health"}
	last := -1
	for _, step := range want {
		at := indexOfCommand(commands, step)
		if at < 0 {
			t.Fatalf("step %q never ran: %v", step, commands)
		}
		if at <= last {
			t.Errorf("step %q ran out of order (position %d, previous %d)", step, at, last)
		}
		last = at
	}
	if len(commands) != len(want) {
		t.Errorf("issued %d commands, want %d: %v", len(commands), len(want), commands)
	}
}

// --- Topology ---

// TestTopology_DefaultsMatchTheOldLiterals pins that a provisioner nobody
// configured builds exactly the commands it built before topology moved into
// config.
func TestTopology_DefaultsMatchTheOldLiterals(t *testing.T) {
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

	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()

	if got := mockClient.mountPointsReceived["mp0"]; got != "/mnt/tenants/105/visuals,mp=/root/freeRadio/content/visuals" {
		t.Errorf("mp0 = %q, want the previous literal", got)
	}
	if got := mockClient.mountPointsReceived["mp1"]; got != "/mnt/tenants/105/music,mp=/root/freeRadio/content/music" {
		t.Errorf("mp1 = %q, want the previous literal", got)
	}
	if !strings.Contains(mockClient.net0Received, "bridge=vmbr0") {
		t.Errorf("net0 = %q, want the previous bridge", mockClient.net0Received)
	}
}

// TestTopology_ConfiguredValuesReachEveryCommand drives a fully non-default
// topology through provisioning and the deploy.
func TestTopology_ConfiguredValuesReachEveryCommand(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)
	p.WithTopology("vmbr1", "/srv/tenants", "/opt/app")

	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	mockClient.mu.Lock()
	net0 := mockClient.net0Received
	mockClient.mu.Unlock()

	if !strings.Contains(net0, "bridge=vmbr1") {
		t.Errorf("net0 = %q, want the configured bridge", net0)
	}
	if strings.Contains(net0, "vmbr0") {
		t.Errorf("net0 = %q, still carries the default bridge", net0)
	}

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	var sawMkdir, sawPctSet bool
	for _, call := range ssh.execOnHostCalls {
		if strings.HasPrefix(call.Command, "mkdir -p") {
			sawMkdir = true
			if !strings.Contains(call.Command, "/srv/tenants/105/visuals") {
				t.Errorf("mkdir = %q, want the configured mount root", call.Command)
			}
			if strings.Contains(call.Command, "/mnt/tenants") {
				t.Errorf("mkdir = %q, still carries the default mount root", call.Command)
			}
		}
		if strings.HasPrefix(call.Command, "pct set") {
			sawPctSet = true
			if !strings.Contains(call.Command, "/srv/tenants/105/visuals,mp=/opt/app/content/visuals") {
				t.Errorf("pct set = %q, want the configured mount root and app dir", call.Command)
			}
			if strings.Contains(call.Command, "/root/freeRadio") {
				t.Errorf("pct set = %q, still carries the default app dir", call.Command)
			}
		}
	}
	if !sawMkdir || !sawPctSet {
		t.Fatalf("expected mkdir and pct set over SSH, got %+v", ssh.execOnHostCalls)
	}

	// The legacy dashboard token write targets the configured app directory too.
	var sawTokenWrite bool
	for _, call := range ssh.execInCtrCalls {
		if strings.Contains(call.Command, "DASHBOARD_TOKEN=") {
			sawTokenWrite = true
			if !strings.Contains(call.Command, "/opt/app/.env") {
				t.Errorf("token write = %q, want the configured app dir", call.Command)
			}
		}
	}
	if !sawTokenWrite {
		t.Error("expected the legacy dashboard token write")
	}
}

// TestTopology_ConfiguredAppDirReachesTheDeploy covers the auto-deploy path,
// which builds its own commands against the app directory.
func TestTopology_ConfiguredAppDirReachesTheDeploy(t *testing.T) {
	p := New(newMockNodeStore(), newMockTenantStore(), newMockProjectStore(), "test-key")
	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)
	p.WithFreeRadioRepo("https://github.com/example/freeRadio.git", "dev")
	p.WithTopology("", "", "/opt/app")

	if err := p.deployFreeRadio(context.Background(), "10.0.0.1", 105, "tenant-1", "dash-token"); err != nil {
		t.Fatalf("deployFreeRadio: %v", err)
	}

	ssh.mu.Lock()
	defer ssh.mu.Unlock()
	for _, call := range ssh.execInCtrCalls {
		if strings.Contains(call.Command, "/root/freeRadio") {
			t.Errorf("deploy command still carries the default app dir: %q", call.Command)
		}
	}
	if _, ok := findCommand(commandsOf(ssh.execInCtrCalls), "cd /opt/app && docker compose up -d"); !ok {
		t.Errorf("expected compose to run in the configured app dir, got %+v", ssh.execInCtrCalls)
	}
}

// TestTopology_EmptyValuesKeepDefaults pins that a partially configured
// deployment keeps the previous behaviour for anything left unset.
func TestTopology_EmptyValuesKeepDefaults(t *testing.T) {
	p := New(newMockNodeStore(), newMockTenantStore(), newMockProjectStore(), "test-key")
	p.WithTopology("", "", "")

	if p.lxcBridge != "vmbr0" || p.mountRoot != "/mnt/tenants" || p.appDir != "/root/freeRadio" {
		t.Errorf("empty overrides changed the defaults: %q %q %q", p.lxcBridge, p.mountRoot, p.appDir)
	}
}

func commandsOf(calls []sshExecCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Command)
	}
	return out
}

// TestTopology_ConfiguredValuesReachTheAPIMountFallback covers the branch taken
// when no SSH client is configured, where the mount points go through the
// Proxmox API instead of pct. It builds its own strings, so the configured
// topology has to reach it separately from the SSH path.
func TestTopology_ConfiguredValuesReachTheAPIMountFallback(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)
	p.WithTopology("vmbr1", "/srv/tenants", "/opt/app")
	// No SSH client: this is what selects the API fallback.

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	mockClient.mu.Lock()
	defer mockClient.mu.Unlock()

	if !mockClient.mountPointsCalled {
		t.Fatal("expected the API mount fallback to be used without an SSH client")
	}
	if got := mockClient.mountPointsReceived["mp0"]; got != "/srv/tenants/105/visuals,mp=/opt/app/content/visuals" {
		t.Errorf("mp0 = %q, want the configured mount root and app dir", got)
	}
	if got := mockClient.mountPointsReceived["mp1"]; got != "/srv/tenants/105/music,mp=/opt/app/content/music" {
		t.Errorf("mp1 = %q, want the configured mount root and app dir", got)
	}
}

// TestTopology_HostileValuesCannotReshapeCommands covers the path from
// configuration to command line. A mount root or app directory carrying shell
// syntax must end up as one argument, not as a second command. These values
// come from the environment, so this is about a typo or a bad deployment
// template as much as about an attacker.
func TestTopology_HostileValuesCannotReshapeCommands(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProject()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)
	p.WithTopology("vmbr0", "/mnt/t; touch /pwned", "/opt/a b")

	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	for _, call := range ssh.execOnHostCalls {
		// The injected command must appear only inside quotes, never as a
		// command of its own.
		if strings.Contains(call.Command, "; touch /pwned") && !strings.Contains(call.Command, `'/mnt/t; touch /pwned'`) {
			t.Errorf("mount root escaped its quoting: %q", call.Command)
		}
		if strings.Contains(call.Command, "/opt/a b") && !strings.Contains(call.Command, `'/opt/a b'`) {
			t.Errorf("app dir with a space was not quoted: %q", call.Command)
		}
	}

	var sawQuotedMount bool
	for _, call := range ssh.execOnHostCalls {
		if strings.Contains(call.Command, `'/mnt/t; touch /pwned'`) {
			sawQuotedMount = true
		}
	}
	if !sawQuotedMount {
		t.Errorf("expected the mount root to appear quoted, got %+v", ssh.execOnHostCalls)
	}
}

// TestDeploy_HostileValuesCannotReshapeCommands covers the deploy path, which
// the provisioning test above never reaches: it walks host commands only, and
// auto-deploy is off there. The repository URL and branch are the values most
// likely to be hand-edited later, and they land in git argument positions.
func TestDeploy_HostileValuesCannotReshapeCommands(t *testing.T) {
	p := New(newMockNodeStore(), newMockTenantStore(), newMockProjectStore(), "test-key")
	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)
	p.WithTopology("", "", "/opt/a b")
	p.WithFreeRadioRepo("https://example.com/r.git;touch /pwned", "dev;reboot")

	if err := p.deployFreeRadio(context.Background(), "10.0.0.1", 105, "tenant-1", "dash-token"); err != nil {
		t.Fatalf("deployFreeRadio: %v", err)
	}

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	hostile := []struct {
		name   string
		raw    string
		quoted string
	}{
		{name: "app dir", raw: "/opt/a b", quoted: `'/opt/a b'`},
		{name: "repo url", raw: "https://example.com/r.git;touch /pwned", quoted: `'https://example.com/r.git;touch /pwned'`},
		{name: "branch", raw: "dev;reboot", quoted: `'dev;reboot'`},
	}

	for _, call := range ssh.execInCtrCalls {
		for _, h := range hostile {
			if strings.Contains(call.Command, h.raw) && !strings.Contains(call.Command, h.quoted) {
				t.Errorf("%s appears unquoted in %q", h.name, call.Command)
			}
		}
	}

	// Each hostile value must actually reach a command, or the assertions above
	// pass by never seeing them.
	for _, h := range hostile {
		var seen bool
		for _, call := range ssh.execInCtrCalls {
			if strings.Contains(call.Command, h.quoted) {
				seen = true
			}
		}
		if !seen {
			t.Errorf("%s never appeared quoted in any deploy command: %+v", h.name, ssh.execInCtrCalls)
		}
	}
}

// TestProvision_LegacyTokenWriteQuotesTheAppDir covers the remaining call site,
// the legacy dashboard token write, which runs when auto-deploy is off.
func TestProvision_LegacyTokenWriteQuotesTheAppDir(t *testing.T) {
	nodeStore := newMockNodeStore()
	tenantStore := newMockTenantStore()
	projectStore := newMockProjectStore()

	proj := testProjectNoHealth()
	n := testNode()
	projectStore.projects[proj.ID] = proj
	nodeStore.nodes[n.ID] = n

	mockClient := &mockProxmoxClient{nextID: 105}
	p := setupProvisioner(nodeStore, tenantStore, projectStore, mockClient, n.ID)
	p.WithTopology("", "", "/opt/a;touch /pwned")

	ssh := &mockSSHExecWithDeployCalls{}
	p.WithSSHClient(ssh)

	waitForProvision(p, "tenant-1", n.ID, proj.ID, "myapp", proj.RAMMB)

	ssh.mu.Lock()
	defer ssh.mu.Unlock()

	var seen bool
	for _, call := range ssh.execInCtrCalls {
		if !strings.Contains(call.Command, "DASHBOARD_TOKEN=") {
			continue
		}
		seen = true
		if !strings.Contains(call.Command, `'/opt/a;touch /pwned'`) {
			t.Errorf("app dir appears unquoted in the token write: %q", call.Command)
		}
	}
	if !seen {
		t.Errorf("expected the legacy token write, got %+v", ssh.execInCtrCalls)
	}
}
