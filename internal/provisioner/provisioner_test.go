package provisioner

import (
	"context"
	"errors"
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
	mu       sync.Mutex
	statuses map[string]string
	lxcIDs   map[string]int
	errors   map[string]string

	setDeletingErr error
}

func newMockTenantStore() *mockTenantStore {
	return &mockTenantStore{
		statuses: make(map[string]string),
		lxcIDs:   make(map[string]int),
		errors:   make(map[string]string),
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

// --- Mock task (implements Waiter) ---

type mockWaiter struct {
	err error
}

func (w *mockWaiter) Wait(_ context.Context, _ ...proxmox.WaitOption) error {
	return w.err
}

// --- Mock Proxmox client (implements ProxmoxClient with Waiter return) ---

type mockProxmoxClient struct {
	mu             sync.Mutex
	nextID         int
	nextIDErr      error
	cloneErr       error
	cloneWaitErr   error
	startErr       error
	startWaitErr   error
	stopErr        error
	stopWaitErr    error
	deleteErr      error
	deleteWaitErr  error
	cloneCalled    bool
	startCalled    bool
	stopCalled     bool
	deleteCalled   bool
	deletedIDs     []int
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

// --- Helpers ---

func testProject() *project.Project {
	return &project.Project{
		ID:         "proj-1",
		Name:       "test-project",
		TemplateID: 100,
		RAMMB:      1536,
		HealthPath: "/api/health",
		CreatedAt:  time.Now(),
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
	if !mockClient.startCalled {
		t.Error("expected start to be called")
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

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, 105, proj.RAMMB)
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

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, 105, proj.RAMMB)
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

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, 105, proj.RAMMB)
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

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, 105, proj.RAMMB)
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

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, 105, proj.RAMMB)
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

	err := p.Deprovision(context.Background(), "tenant-1", n.ID, 105, 1536)
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
