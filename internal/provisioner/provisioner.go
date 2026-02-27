package provisioner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"controlplane/internal/crypto"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/proxmox"
	"controlplane/internal/tenant"
)

const (
	provisionTimeout  = 10 * time.Minute
	maxConcurrentJobs = 10
)

// NodeStore defines what the provisioner needs from the node store.
type NodeStore interface {
	GetByID(ctx context.Context, id string) (*node.Node, error)
	GetEncryptedTokenByID(ctx context.Context, id string) (string, error)
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

// TenantStore defines what the provisioner needs from the tenant store.
type TenantStore interface {
	SetActive(ctx context.Context, id string, lxcID int) error
	SetError(ctx context.Context, id string, errMsg string) error
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
}

// ProjectStore defines what the provisioner needs from the project store.
type ProjectStore interface {
	GetByID(ctx context.Context, id string) (*project.Project, error)
}

// Waiter can wait for an async operation to complete.
type Waiter interface {
	Wait(ctx context.Context, opts ...proxmox.WaitOption) error
}

// ProxmoxClient defines the Proxmox operations needed by the provisioner.
// Returns Waiter instead of *proxmox.Task so it can be mocked in tests.
type ProxmoxClient interface {
	GetNextID(ctx context.Context) (int, error)
	CloneContainer(ctx context.Context, templateID int, opts proxmox.CloneOptions) (Waiter, error)
	StartContainer(ctx context.Context, vmid int) (Waiter, error)
	StopContainer(ctx context.Context, vmid int) (Waiter, error)
	DeleteContainer(ctx context.Context, vmid int, force bool) (Waiter, error)
}

// proxmoxAdapter wraps a real *proxmox.Client to satisfy ProxmoxClient interface.
// This is needed because *proxmox.Client methods return *proxmox.Task (concrete),
// but our interface requires Waiter.
type proxmoxAdapter struct {
	client *proxmox.Client
}

func (a *proxmoxAdapter) GetNextID(ctx context.Context) (int, error) {
	return a.client.GetNextID(ctx)
}

func (a *proxmoxAdapter) CloneContainer(ctx context.Context, templateID int, opts proxmox.CloneOptions) (Waiter, error) {
	return a.client.CloneContainer(ctx, templateID, opts)
}

func (a *proxmoxAdapter) StartContainer(ctx context.Context, vmid int) (Waiter, error) {
	return a.client.StartContainer(ctx, vmid)
}

func (a *proxmoxAdapter) StopContainer(ctx context.Context, vmid int) (Waiter, error) {
	return a.client.StopContainer(ctx, vmid)
}

func (a *proxmoxAdapter) DeleteContainer(ctx context.Context, vmid int, force bool) (Waiter, error) {
	return a.client.DeleteContainer(ctx, vmid, force)
}

// ClientFactory creates Proxmox clients. Allows injection in tests.
type ClientFactory func(baseURL, apiToken string) ProxmoxClient

// defaultClientFactory creates real Proxmox clients wrapped in an adapter.
func defaultClientFactory(baseURL, apiToken string) ProxmoxClient {
	return &proxmoxAdapter{client: proxmox.NewClient(baseURL, apiToken)}
}

// Provisioner handles async provisioning and deprovisioning of tenant LXC containers.
type Provisioner struct {
	nodeStore     NodeStore
	tenantStore   TenantStore
	projectStore  ProjectStore
	encryptionKey string
	clientFactory ClientFactory
	clients       map[string]ProxmoxClient // keyed by node ID, lazy-initialized
	mu            sync.RWMutex             // guards clients map
	sem           chan struct{}             // bounded concurrency for provision goroutines
	wg            sync.WaitGroup           // tracks in-flight provisions for graceful shutdown
}

// New creates a new Provisioner.
func New(nodeStore NodeStore, tenantStore TenantStore, projectStore ProjectStore, encryptionKey string) *Provisioner {
	return &Provisioner{
		nodeStore:     nodeStore,
		tenantStore:   tenantStore,
		projectStore:  projectStore,
		encryptionKey: encryptionKey,
		clientFactory: defaultClientFactory,
		clients:       make(map[string]ProxmoxClient),
		sem:           make(chan struct{}, maxConcurrentJobs),
	}
}

// WithClientFactory sets a custom client factory (used for testing).
func (p *Provisioner) WithClientFactory(f ClientFactory) {
	p.clientFactory = f
}

// InvalidateClient removes the cached Proxmox client for a node,
// forcing re-creation with fresh credentials on next use.
func (p *Provisioner) InvalidateClient(nodeID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, nodeID)
}

// Shutdown waits for all in-flight provisioning goroutines to complete.
func (p *Provisioner) Shutdown() {
	p.wg.Wait()
}

// getClient returns a cached or newly created Proxmox client for the given node.
// Uses double-checked locking to avoid blocking after initial creation.
func (p *Provisioner) getClient(ctx context.Context, nodeID string) (ProxmoxClient, error) {
	p.mu.RLock()
	if c, ok := p.clients[nodeID]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check after acquiring write lock.
	if c, ok := p.clients[nodeID]; ok {
		return c, nil
	}

	n, err := p.nodeStore.GetByID(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	if n == nil {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	encToken, err := p.nodeStore.GetEncryptedTokenByID(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get encrypted token: %w", err)
	}
	if encToken == "" {
		return nil, fmt.Errorf("node %s has no api token", nodeID)
	}

	token, err := crypto.Decrypt(encToken, p.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt token: %w", err)
	}

	client := p.clientFactory(n.ProxmoxURL, token)
	p.clients[nodeID] = client
	return client, nil
}

// Provision creates an LXC container for a tenant asynchronously.
// This method is designed to be called as a goroutine — it creates its own
// background context with a timeout, independent of the caller's context.
func (p *Provisioner) Provision(tenantID, nodeID, projectID, subdomain string, ramMB int) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("provision: panic recovered", "tenant_id", tenantID, "panic", rec)
				p.setError(context.Background(), tenantID, nodeID, ramMB, "provisioning failed: internal panic")
			}
		}()

		// Acquire semaphore slot (bounded concurrency)
		p.sem <- struct{}{}
		defer func() { <-p.sem }()

		p.doProvision(tenantID, nodeID, projectID, subdomain, ramMB)
	}()
}

func (p *Provisioner) doProvision(tenantID, nodeID, projectID, subdomain string, ramMB int) {
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()

	log := slog.With("tenant_id", tenantID, "node_id", nodeID, "project_id", projectID)

	// Get project for template_id
	proj, err := p.projectStore.GetByID(ctx, projectID)
	if err != nil {
		log.Error("provision: get project", "error", err)
		p.setError(ctx, tenantID, nodeID, ramMB, "provisioning failed: project lookup error")
		return
	}
	if proj == nil {
		log.Error("provision: project not found")
		p.setError(ctx, tenantID, nodeID, ramMB, "provisioning failed: project not found")
		return
	}
	templateID := proj.TemplateID

	// Get Proxmox client
	client, err := p.getClient(ctx, nodeID)
	if err != nil {
		log.Error("provision: get client", "error", err)
		p.setError(ctx, tenantID, nodeID, ramMB, "provisioning failed: node connection error")
		return
	}

	// Get next VMID
	newID, err := client.GetNextID(ctx)
	if err != nil {
		log.Error("provision: get next id", "error", err)
		p.setError(ctx, tenantID, nodeID, ramMB, "provisioning failed: could not allocate container ID")
		return
	}
	log = log.With("lxc_id", newID)

	// Clone container
	log.Info("provision: cloning container", "template_id", templateID)
	cloneTask, err := client.CloneContainer(ctx, templateID, proxmox.CloneOptions{
		NewID:    newID,
		Hostname: subdomain,
		Full:     true,
	})
	if err != nil {
		log.Error("provision: clone container", "error", err)
		p.setError(ctx, tenantID, nodeID, ramMB, "provisioning failed: clone error")
		return
	}

	if err := cloneTask.Wait(ctx); err != nil {
		log.Error("provision: wait for clone", "error", err)
		p.setError(ctx, tenantID, nodeID, ramMB, "provisioning failed: clone did not complete")
		return
	}

	// Start container
	log.Info("provision: starting container")
	startTask, err := client.StartContainer(ctx, newID)
	if err != nil {
		log.Error("provision: start container", "error", err)
		p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: start error")
		return
	}

	if err := startTask.Wait(ctx); err != nil {
		log.Error("provision: wait for start", "error", err)
		p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: start did not complete")
		return
	}

	// Mark as active
	if err := p.tenantStore.SetActive(ctx, tenantID, newID); err != nil {
		log.Error("provision: set active", "error", err)
		// Container is running but DB update failed — clean up the container
		// to avoid orphans, and mark as error
		p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: could not update status")
		return
	}

	log.Info("provision: tenant provisioned successfully")
}

// Deprovision removes an LXC container for a tenant synchronously.
// ramMB is passed by the caller (from the project) so we don't need to re-fetch the project.
func (p *Provisioner) Deprovision(ctx context.Context, tenantID, nodeID string, lxcID, ramMB int) error {
	log := slog.With("tenant_id", tenantID, "node_id", nodeID, "lxc_id", lxcID)

	// Atomically transition to deleting — prevents concurrent delete requests
	if err := p.tenantStore.SetDeleting(ctx, tenantID); err != nil {
		if errors.Is(err, tenant.ErrStateConflict) {
			return tenant.ErrStateConflict
		}
		return fmt.Errorf("set deleting: %w", err)
	}

	// Get Proxmox client
	client, err := p.getClient(ctx, nodeID)
	if err != nil {
		_ = p.tenantStore.SetError(ctx, tenantID, "deprovision failed: node connection error")
		return fmt.Errorf("get proxmox client: %w", err)
	}

	// Stop container (ignore "already stopped" errors)
	log.Info("deprovision: stopping container")
	stopTask, err := client.StopContainer(ctx, lxcID)
	if err != nil {
		log.Warn("deprovision: stop container error (may be already stopped)", "error", err)
	} else {
		if err := stopTask.Wait(ctx); err != nil {
			log.Warn("deprovision: wait for stop (may be already stopped)", "error", err)
		}
	}

	// Delete container
	log.Info("deprovision: deleting container")
	deleteTask, err := client.DeleteContainer(ctx, lxcID, true)
	if err != nil {
		_ = p.tenantStore.SetError(ctx, tenantID, "deprovision failed: container delete error")
		return fmt.Errorf("delete container: %w", err)
	}

	if err := deleteTask.Wait(ctx); err != nil {
		_ = p.tenantStore.SetError(ctx, tenantID, "deprovision failed: container delete did not complete")
		return fmt.Errorf("delete task failed: %w", err)
	}

	// Release RAM
	if ramMB > 0 {
		if err := p.nodeStore.ReleaseRAM(ctx, nodeID, ramMB); err != nil {
			log.Error("deprovision: release ram", "error", err)
		}
	}

	// Mark as deleted
	if err := p.tenantStore.SetDeleted(ctx, tenantID); err != nil {
		return fmt.Errorf("set deleted: %w", err)
	}

	log.Info("deprovision: tenant deprovisioned successfully")
	return nil
}

// Suspend stops an LXC container for a tenant.
func (p *Provisioner) Suspend(ctx context.Context, tenantID, nodeID string, lxcID int) error {
	log := slog.With("tenant_id", tenantID, "node_id", nodeID, "lxc_id", lxcID)

	client, err := p.getClient(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get proxmox client: %w", err)
	}

	log.Info("suspend: stopping container")
	stopTask, err := client.StopContainer(ctx, lxcID)
	if err != nil {
		return fmt.Errorf("stop container: %w", err)
	}

	if err := stopTask.Wait(ctx); err != nil {
		return fmt.Errorf("stop task failed: %w", err)
	}

	log.Info("suspend: container stopped")
	return nil
}

// Resume starts an LXC container for a tenant.
func (p *Provisioner) Resume(ctx context.Context, tenantID, nodeID string, lxcID int) error {
	log := slog.With("tenant_id", tenantID, "node_id", nodeID, "lxc_id", lxcID)

	client, err := p.getClient(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get proxmox client: %w", err)
	}

	log.Info("resume: starting container")
	startTask, err := client.StartContainer(ctx, lxcID)
	if err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	if err := startTask.Wait(ctx); err != nil {
		return fmt.Errorf("start task failed: %w", err)
	}

	log.Info("resume: container started")
	return nil
}

// setError marks a tenant as errored (sanitized message) and releases RAM.
func (p *Provisioner) setError(ctx context.Context, tenantID, nodeID string, ramMB int, errMsg string) {
	if err := p.tenantStore.SetError(ctx, tenantID, errMsg); err != nil {
		slog.Error("provision: failed to set error status", "tenant_id", tenantID, "error", err)
	}
	if ramMB > 0 {
		if err := p.nodeStore.ReleaseRAM(ctx, nodeID, ramMB); err != nil {
			slog.Error("provision: failed to release ram", "tenant_id", tenantID, "error", err)
		}
	}
}

// cleanupAndError attempts to delete the created container, then marks error and releases RAM.
func (p *Provisioner) cleanupAndError(ctx context.Context, client ProxmoxClient, tenantID, nodeID string, ramMB, lxcID int, errMsg string) {
	slog.Info("provision: attempting cleanup", "tenant_id", tenantID, "lxc_id", lxcID)
	deleteTask, err := client.DeleteContainer(ctx, lxcID, true)
	if err != nil {
		slog.Error("provision: cleanup delete failed", "tenant_id", tenantID, "error", err)
	} else {
		if err := deleteTask.Wait(ctx); err != nil {
			slog.Error("provision: cleanup delete wait failed", "tenant_id", tenantID, "error", err)
		}
	}
	p.setError(ctx, tenantID, nodeID, ramMB, errMsg)
}
