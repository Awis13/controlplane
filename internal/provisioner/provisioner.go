package provisioner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"controlplane/internal/crypto"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/proxmox"
)

const provisionTimeout = 10 * time.Minute

// NodeStore defines what the provisioner needs from the node store.
type NodeStore interface {
	GetByID(ctx context.Context, id string) (*node.Node, error)
	GetEncryptedTokenByID(ctx context.Context, id string) (string, error)
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
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
	}
}

// WithClientFactory sets a custom client factory (used for testing).
func (p *Provisioner) WithClientFactory(f ClientFactory) {
	p.clientFactory = f
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
// This method is designed to be called as a goroutine.
// It uses context.Background() with a timeout, NOT the HTTP request context.
func (p *Provisioner) Provision(ctx context.Context, tenantID, nodeID, projectID, subdomain string) {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()

	log := slog.With("tenant_id", tenantID, "node_id", nodeID, "project_id", projectID)

	// Get project for template_id and ram_mb
	proj, err := p.projectStore.GetByID(ctx, projectID)
	if err != nil {
		log.Error("provision: get project", "error", err)
		p.setError(ctx, tenantID, nodeID, proj, fmt.Sprintf("get project: %v", err))
		return
	}
	if proj == nil {
		log.Error("provision: project not found")
		p.setError(ctx, tenantID, nodeID, nil, "project not found")
		return
	}

	// Get Proxmox client
	client, err := p.getClient(ctx, nodeID)
	if err != nil {
		log.Error("provision: get client", "error", err)
		p.setError(ctx, tenantID, nodeID, proj, fmt.Sprintf("get proxmox client: %v", err))
		return
	}

	// Get next VMID
	newID, err := client.GetNextID(ctx)
	if err != nil {
		log.Error("provision: get next id", "error", err)
		p.setError(ctx, tenantID, nodeID, proj, fmt.Sprintf("get next id: %v", err))
		return
	}
	log = log.With("lxc_id", newID)

	// Clone container
	log.Info("provision: cloning container", "template_id", proj.TemplateID)
	cloneTask, err := client.CloneContainer(ctx, proj.TemplateID, proxmox.CloneOptions{
		NewID:    newID,
		Hostname: subdomain,
		Full:     true,
	})
	if err != nil {
		log.Error("provision: clone container", "error", err)
		p.setError(ctx, tenantID, nodeID, proj, fmt.Sprintf("clone container: %v", err))
		return
	}

	if err := cloneTask.Wait(ctx); err != nil {
		log.Error("provision: wait for clone", "error", err)
		p.setError(ctx, tenantID, nodeID, proj, fmt.Sprintf("clone task failed: %v", err))
		return
	}

	// Start container
	log.Info("provision: starting container")
	startTask, err := client.StartContainer(ctx, newID)
	if err != nil {
		log.Error("provision: start container", "error", err)
		p.cleanupAndError(ctx, client, tenantID, nodeID, proj, newID, fmt.Sprintf("start container: %v", err))
		return
	}

	if err := startTask.Wait(ctx); err != nil {
		log.Error("provision: wait for start", "error", err)
		p.cleanupAndError(ctx, client, tenantID, nodeID, proj, newID, fmt.Sprintf("start task failed: %v", err))
		return
	}

	// Mark as active
	if err := p.tenantStore.SetActive(ctx, tenantID, newID); err != nil {
		log.Error("provision: set active", "error", err)
		return
	}

	log.Info("provision: tenant provisioned successfully")
}

// Deprovision removes an LXC container for a tenant synchronously.
func (p *Provisioner) Deprovision(ctx context.Context, tenantID, nodeID, projectID string, lxcID int) error {
	log := slog.With("tenant_id", tenantID, "node_id", nodeID, "lxc_id", lxcID)

	// Mark as deleting
	if err := p.tenantStore.SetDeleting(ctx, tenantID); err != nil {
		return fmt.Errorf("set deleting: %w", err)
	}

	// Get project for RAM amount
	proj, err := p.projectStore.GetByID(ctx, projectID)
	if err != nil {
		p.tenantStore.SetError(ctx, tenantID, fmt.Sprintf("get project: %v", err)) //nolint:errcheck
		return fmt.Errorf("get project: %w", err)
	}

	// Get Proxmox client
	client, err := p.getClient(ctx, nodeID)
	if err != nil {
		p.tenantStore.SetError(ctx, tenantID, fmt.Sprintf("get proxmox client: %v", err)) //nolint:errcheck
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
		p.tenantStore.SetError(ctx, tenantID, fmt.Sprintf("delete container: %v", err)) //nolint:errcheck
		return fmt.Errorf("delete container: %w", err)
	}

	if err := deleteTask.Wait(ctx); err != nil {
		p.tenantStore.SetError(ctx, tenantID, fmt.Sprintf("delete task failed: %v", err)) //nolint:errcheck
		return fmt.Errorf("delete task failed: %w", err)
	}

	// Release RAM
	if proj != nil {
		if err := p.nodeStore.ReleaseRAM(ctx, nodeID, proj.RAMMB); err != nil {
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

// setError marks a tenant as errored and releases RAM.
func (p *Provisioner) setError(ctx context.Context, tenantID, nodeID string, proj *project.Project, errMsg string) {
	if err := p.tenantStore.SetError(ctx, tenantID, errMsg); err != nil {
		slog.Error("provision: failed to set error status", "tenant_id", tenantID, "error", err)
	}
	if proj != nil {
		if err := p.nodeStore.ReleaseRAM(ctx, nodeID, proj.RAMMB); err != nil {
			slog.Error("provision: failed to release ram", "tenant_id", tenantID, "error", err)
		}
	}
}

// cleanupAndError attempts to delete the created container, then marks error and releases RAM.
func (p *Provisioner) cleanupAndError(ctx context.Context, client ProxmoxClient, tenantID, nodeID string, proj *project.Project, lxcID int, errMsg string) {
	slog.Info("provision: attempting cleanup", "tenant_id", tenantID, "lxc_id", lxcID)
	deleteTask, err := client.DeleteContainer(ctx, lxcID, true)
	if err != nil {
		slog.Error("provision: cleanup delete failed", "tenant_id", tenantID, "error", err)
	} else {
		if err := deleteTask.Wait(ctx); err != nil {
			slog.Error("provision: cleanup delete wait failed", "tenant_id", tenantID, "error", err)
		}
	}
	p.setError(ctx, tenantID, nodeID, proj, errMsg)
}
