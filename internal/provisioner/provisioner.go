package provisioner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
	SetLXCIP(ctx context.Context, id string, ip string) error
	GetNextAvailableIP(ctx context.Context, cidr string) (string, error)
	SetHealthStatus(ctx context.Context, id string, status string) error
	GetByID(ctx context.Context, id string) (*tenant.Tenant, error)
}

// ProjectStore defines what the provisioner needs from the project store.
type ProjectStore interface {
	GetByID(ctx context.Context, id string) (*project.Project, error)
}

// StationCreator defines what the provisioner needs to auto-create stations.
type StationCreator interface {
	AutoCreateStation(ctx context.Context, tenantID, name, subdomain, ownerID, caddyDomain string) error
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
	ConfigureNetwork(ctx context.Context, vmid int, net0Value string) error
	ConfigureMountPoints(ctx context.Context, vmid int, mounts map[string]string) error
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

func (a *proxmoxAdapter) ConfigureNetwork(ctx context.Context, vmid int, net0Value string) error {
	return a.client.ConfigureNetwork(ctx, vmid, net0Value)
}

func (a *proxmoxAdapter) ConfigureMountPoints(ctx context.Context, vmid int, mounts map[string]string) error {
	return a.client.ConfigureMountPoints(ctx, vmid, mounts)
}

// ClientFactory creates Proxmox clients. Allows injection in tests.
type ClientFactory func(baseURL, apiToken string) ProxmoxClient

// defaultClientFactory creates real Proxmox clients wrapped in an adapter.
func defaultClientFactory(baseURL, apiToken string) ProxmoxClient {
	return &proxmoxAdapter{client: proxmox.NewClient(baseURL, apiToken)}
}

// CaddyClient defines the Caddy Admin API operations needed by the provisioner.
type CaddyClient interface {
	AddRoute(ctx context.Context, subdomain, targetIP string) error
	RemoveRoute(ctx context.Context, subdomain string) error
}

// Provisioner handles async provisioning and deprovisioning of tenant LXC containers.
type Provisioner struct {
	nodeStore      NodeStore
	tenantStore    TenantStore
	projectStore   ProjectStore
	encryptionKey  string
	clientFactory  ClientFactory
	clients        map[string]ProxmoxClient // keyed by node ID, lazy-initialized
	mu             sync.RWMutex             // guards clients map
	sem            chan struct{}             // bounded concurrency for provision goroutines
	wg             sync.WaitGroup           // tracks in-flight provisions for graceful shutdown
	caddyClient    CaddyClient              // optional: Caddy route management
	stationCreator StationCreator           // optional: auto-create station on provisioning
	caddyDomain    string                   // domain for station stream URLs
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

// WithCaddyClient sets the Caddy client for dynamic route management.
func (p *Provisioner) WithCaddyClient(c CaddyClient) {
	p.caddyClient = c
}

// WithStationCreator sets the station creator for auto-creating stations on provisioning.
func (p *Provisioner) WithStationCreator(sc StationCreator, caddyDomain string) {
	p.stationCreator = sc
	p.caddyDomain = caddyDomain
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

	// Get project
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

	// Allocate IP and configure network (before start)
	var lxcIP string
	if proj.NetworkCIDR != "" {
		lxcIP, err = p.tenantStore.GetNextAvailableIP(ctx, proj.NetworkCIDR)
		if err != nil {
			log.Error("provision: allocate ip", "error", err)
			p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: IP allocation error")
			return
		}
		log = log.With("lxc_ip", lxcIP)

		// Extract prefix length from CIDR (e.g. "10.10.10.0/24" -> "24")
		prefixLen := "24"
		if parts := strings.SplitN(proj.NetworkCIDR, "/", 2); len(parts) == 2 {
			prefixLen = parts[1]
		}
		net0 := fmt.Sprintf("name=eth0,bridge=vmbr0,ip=%s/%s,gw=%s,firewall=1,type=veth", lxcIP, prefixLen, proj.Gateway)
		log.Info("provision: configuring network", "net0", net0)
		if err := client.ConfigureNetwork(ctx, newID, net0); err != nil {
			log.Error("provision: configure network", "error", err)
			p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: network config error")
			return
		}

		// Save IP in DB early (before start, so it's visible during provisioning)
		if err := p.tenantStore.SetLXCIP(ctx, tenantID, lxcIP); err != nil {
			log.Error("provision: set lxc ip", "error", err)
			// Non-fatal — continue
		}
	}

	// Configure bind mount points for shared media storage
	mounts := map[string]string{
		"mp0": fmt.Sprintf("/mnt/tenants/%d/visuals,mp=/root/freeRadio/content/visuals", newID),
		"mp1": fmt.Sprintf("/mnt/tenants/%d/music,mp=/root/freeRadio/content/music", newID),
	}
	log.Info("provision: configuring mount points", "mounts", mounts)
	if err := client.ConfigureMountPoints(ctx, newID, mounts); err != nil {
		log.Error("provision: configure mount points", "error", err)
		p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: mount point config error")
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
		p.cleanupAndError(ctx, client, tenantID, nodeID, ramMB, newID, "provisioning failed: could not update status")
		return
	}

	// Auto-create station record (best-effort, don't fail provisioning)
	if p.stationCreator != nil {
		t, err := p.tenantStore.GetByID(ctx, tenantID)
		if err != nil {
			log.Error("provision: get tenant for station creation", "error", err)
		} else if t != nil {
			ownerID := ""
			if t.OwnerID != nil {
				ownerID = *t.OwnerID
			}
			if err := p.stationCreator.AutoCreateStation(ctx, tenantID, t.Name, subdomain, ownerID, p.caddyDomain); err != nil {
				log.Error("provision: auto-create station", "error", err)
			} else {
				log.Info("provision: station auto-created", "subdomain", subdomain)
			}
		}
	}

	// Add Caddy route (best-effort, don't fail provisioning)
	if p.caddyClient != nil && lxcIP != "" {
		if err := p.caddyClient.AddRoute(ctx, subdomain, lxcIP); err != nil {
			log.Error("provision: add caddy route", "subdomain", subdomain, "error", err)
		} else {
			log.Info("provision: caddy route added", "subdomain", subdomain)
		}
	}

	// Health check (best-effort, don't fail provisioning)
	if lxcIP != "" && len(proj.Ports) > 0 && proj.HealthPath != "" {
		healthURL := fmt.Sprintf("http://%s:%d%s", lxcIP, proj.Ports[0], proj.HealthPath)
		log.Info("provision: checking health", "url", healthURL)
		healthy := p.waitForHealth(ctx, healthURL, 60*time.Second, 5*time.Second)
		status := "healthy"
		if !healthy {
			status = "unhealthy"
			log.Warn("provision: health check timeout, marking unhealthy")
		}
		if err := p.tenantStore.SetHealthStatus(ctx, tenantID, status); err != nil {
			log.Error("provision: set health status", "error", err)
		}
	}

	log.Info("provision: tenant provisioned successfully")
}

// waitForHealth polls a URL until it returns 200 or timeout.
func (p *Provisioner) waitForHealth(ctx context.Context, url string, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
	return false
}

// Deprovision removes an LXC container for a tenant synchronously.
// ramMB is passed by the caller (from the project) so we don't need to re-fetch the project.
// subdomain is used to remove the Caddy route if a CaddyClient is configured.
func (p *Provisioner) Deprovision(ctx context.Context, tenantID, nodeID, subdomain string, lxcID, ramMB int) error {
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

	// Remove Caddy route (best-effort, don't fail deprovisioning)
	if p.caddyClient != nil && subdomain != "" {
		if err := p.caddyClient.RemoveRoute(ctx, subdomain); err != nil {
			log.Error("deprovision: remove caddy route", "subdomain", subdomain, "error", err)
		} else {
			log.Info("deprovision: caddy route removed", "subdomain", subdomain)
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
