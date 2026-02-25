package admin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/crypto"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
	"controlplane/internal/tenant"
)

// Store interfaces — concrete *node.Store etc. satisfy these automatically.

type NodeStore interface {
	List(ctx context.Context) ([]node.Node, error)
	GetByID(ctx context.Context, id string) (*node.Node, error)
	Create(ctx context.Context, req node.CreateNodeRequest) (*node.Node, error)
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

type ProjectStore interface {
	List(ctx context.Context) ([]project.Project, error)
	GetByID(ctx context.Context, id string) (*project.Project, error)
	Create(ctx context.Context, req project.CreateProjectRequest) (*project.Project, error)
}

type TenantStore interface {
	List(ctx context.Context) ([]tenant.Tenant, error)
	GetByID(ctx context.Context, id string) (*tenant.Tenant, error)
	Create(ctx context.Context, req tenant.CreateTenantRequest) (*tenant.Tenant, error)
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
}

type Provisioner interface {
	Provision(tenantID, nodeID, projectID, subdomain string, ramMB int)
	Deprovision(ctx context.Context, tenantID, nodeID string, lxcID, ramMB int) error
}

// Handler serves the admin UI.
type Handler struct {
	tmpl          *Templates
	nodes         NodeStore
	projects      ProjectStore
	tenants       TenantStore
	provisioner   Provisioner
	encryptionKey string
}

// NewHandler creates a new admin Handler. Returns error if templates fail to parse.
func NewHandler(nodes NodeStore, projects ProjectStore, tenants TenantStore, provisioner Provisioner, encryptionKey string) (*Handler, error) {
	tmpl, err := ParseTemplates()
	if err != nil {
		return nil, err
	}
	return &Handler{
		tmpl:          tmpl,
		nodes:         nodes,
		projects:      projects,
		tenants:       tenants,
		provisioner:   provisioner,
		encryptionKey: encryptionKey,
	}, nil
}

// Routes returns a chi.Router with all admin routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	// Static files
	r.Handle("/static/*", http.StripPrefix("/admin/static/", http.FileServer(StaticFS())))

	// Pages
	r.Get("/", h.dashboard)
	r.Get("/nodes", h.nodesList)
	r.Get("/projects", h.projectsList)
	r.Get("/tenants", h.tenantsList)

	// Actions
	r.Post("/nodes", h.createNode)
	r.Post("/projects", h.createProject)
	r.Post("/tenants", h.createTenant)
	r.Delete("/tenants/{id}", h.deleteTenant)
	r.Get("/tenants/{id}/row", h.tenantRow)

	return r
}

// --- Page data types ---

type pageData struct {
	Title string
	Nav   string
}

type enrichedTenant struct {
	tenant.Tenant
	ProjectName string
	NodeName    string
}

// --- Dashboard ---

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.nodes.List(r.Context())
	if err != nil {
		slog.Error("admin: list nodes", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	projects, err := h.projects.List(r.Context())
	if err != nil {
		slog.Error("admin: list projects", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	tenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: list tenants", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	nodeMap, projectMap := buildMaps(nodes, projects)

	var totalRAM, allocRAM int
	for _, n := range nodes {
		totalRAM += n.TotalRAMMB
		allocRAM += n.AllocatedRAMMB
	}

	// Recent tenants (up to 10, already sorted by created_at DESC)
	limit := 10
	if len(tenants) < limit {
		limit = len(tenants)
	}
	recent := make([]enrichedTenant, limit)
	for i := 0; i < limit; i++ {
		recent[i] = enrichTenant(tenants[i], nodeMap, projectMap)
	}

	data := struct {
		pageData
		Nodes         []node.Node
		Projects      []project.Project
		Tenants       []tenant.Tenant
		TotalRAM      int
		AllocatedRAM  int
		RecentTenants []enrichedTenant
	}{
		pageData:      pageData{Title: "Dashboard", Nav: "dashboard"},
		Nodes:         nodes,
		Projects:      projects,
		Tenants:       tenants,
		TotalRAM:      totalRAM,
		AllocatedRAM:  allocRAM,
		RecentTenants: recent,
	}

	if err := h.tmpl.RenderPage(w, "dashboard", data); err != nil {
		slog.Error("admin: render dashboard", "error", err)
	}
}

// --- Nodes ---

func (h *Handler) nodesList(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.nodes.List(r.Context())
	if err != nil {
		slog.Error("admin: list nodes", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	data := struct {
		pageData
		Nodes []node.Node
	}{
		pageData: pageData{Title: "Nodes", Nav: "nodes"},
		Nodes:    nodes,
	}

	if err := h.tmpl.RenderPage(w, "nodes", data); err != nil {
		slog.Error("admin: render nodes", "error", err)
	}
}

func (h *Handler) createNode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	tailscaleIP := strings.TrimSpace(r.FormValue("tailscale_ip"))
	proxmoxURL := strings.TrimSpace(r.FormValue("proxmox_url"))
	apiToken := strings.TrimSpace(r.FormValue("api_token"))
	totalRAMStr := r.FormValue("total_ram_mb")

	if name == "" || tailscaleIP == "" || proxmoxURL == "" || apiToken == "" || totalRAMStr == "" {
		h.renderFlash(w, "flash_error", "All fields are required")
		return
	}

	if !nameRegexp.MatchString(name) || len(name) > 63 {
		h.renderFlash(w, "flash_error", "Invalid name: lowercase alphanumeric with hyphens/dots/underscores, 2-63 chars")
		return
	}

	if net.ParseIP(tailscaleIP) == nil {
		h.renderFlash(w, "flash_error", "Invalid Tailscale IP address")
		return
	}

	u, parseErr := url.Parse(proxmoxURL)
	if parseErr != nil || u.Scheme != "https" || u.Host == "" {
		h.renderFlash(w, "flash_error", "Invalid Proxmox URL: must be a valid HTTPS URL")
		return
	}

	totalRAM, err := strconv.Atoi(totalRAMStr)
	if err != nil || totalRAM <= 0 {
		h.renderFlash(w, "flash_error", "Total RAM must be a positive number")
		return
	}

	encrypted, err := crypto.Encrypt(apiToken, h.encryptionKey)
	if err != nil {
		slog.Error("admin: encrypt api token", "error", err)
		h.renderFlash(w, "flash_error", "Failed to encrypt API token")
		return
	}

	n, err := h.nodes.Create(r.Context(), node.CreateNodeRequest{
		Name:        name,
		TailscaleIP: tailscaleIP,
		ProxmoxURL:  proxmoxURL,
		APIToken:    encrypted,
		TotalRAMMB:  totalRAM,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			h.renderFlash(w, "flash_error", "Node name already exists")
			return
		}
		slog.Error("admin: create node", "error", err)
		h.renderFlash(w, "flash_error", "Failed to create node")
		return
	}

	if err := h.tmpl.RenderPartial(w, "node_row", n); err != nil {
		slog.Error("admin: render node row", "error", err)
	}
}

// --- Projects ---

func (h *Handler) projectsList(w http.ResponseWriter, r *http.Request) {
	projects, err := h.projects.List(r.Context())
	if err != nil {
		slog.Error("admin: list projects", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	data := struct {
		pageData
		Projects []project.Project
	}{
		pageData: pageData{Title: "Projects", Nav: "projects"},
		Projects: projects,
	}

	if err := h.tmpl.RenderPage(w, "projects", data); err != nil {
		slog.Error("admin: render projects", "error", err)
	}
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	templateIDStr := r.FormValue("template_id")
	portsStr := strings.TrimSpace(r.FormValue("ports"))
	healthPath := strings.TrimSpace(r.FormValue("health_path"))
	ramStr := r.FormValue("ram_mb")

	if name == "" || templateIDStr == "" {
		h.renderFlash(w, "flash_error", "Name and Template ID are required")
		return
	}

	templateID, err := strconv.Atoi(templateIDStr)
	if err != nil || templateID <= 0 {
		h.renderFlash(w, "flash_error", "Template ID must be a positive number")
		return
	}

	var ports []int
	if portsStr != "" {
		for _, ps := range strings.Split(portsStr, ",") {
			p, err := strconv.Atoi(strings.TrimSpace(ps))
			if err != nil || p < 1 || p > 65535 {
				h.renderFlash(w, "flash_error", "Invalid port number: "+strings.TrimSpace(ps))
				return
			}
			ports = append(ports, p)
		}
	}

	var ramMB int
	if ramStr != "" {
		ramMB, err = strconv.Atoi(ramStr)
		if err != nil || ramMB < 0 {
			h.renderFlash(w, "flash_error", "RAM must be a non-negative number")
			return
		}
	}

	p, err := h.projects.Create(r.Context(), project.CreateProjectRequest{
		Name:       name,
		TemplateID: templateID,
		Ports:      ports,
		HealthPath: healthPath,
		RAMMB:      ramMB,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			h.renderFlash(w, "flash_error", "Project name already exists")
			return
		}
		slog.Error("admin: create project", "error", err)
		h.renderFlash(w, "flash_error", "Failed to create project")
		return
	}

	if err := h.tmpl.RenderPartial(w, "project_row", p); err != nil {
		slog.Error("admin: render project row", "error", err)
	}
}

// --- Tenants ---

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)
var subdomainRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

var reservedSubdomains = map[string]bool{
	"www": true, "api": true, "admin": true, "app": true,
	"mail": true, "smtp": true, "ftp": true, "ns1": true, "ns2": true,
	"cdn": true, "static": true, "assets": true, "media": true,
}

func (h *Handler) tenantsList(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.nodes.List(r.Context())
	if err != nil {
		slog.Error("admin: list nodes", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	projects, err := h.projects.List(r.Context())
	if err != nil {
		slog.Error("admin: list projects", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	tenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: list tenants", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	nodeMap, projectMap := buildMaps(nodes, projects)

	enriched := make([]enrichedTenant, len(tenants))
	for i, t := range tenants {
		enriched[i] = enrichTenant(t, nodeMap, projectMap)
	}

	data := struct {
		pageData
		Nodes    []node.Node
		Projects []project.Project
		Tenants  []enrichedTenant
	}{
		pageData: pageData{Title: "Tenants", Nav: "tenants"},
		Nodes:    nodes,
		Projects: projects,
		Tenants:  enriched,
	}

	if err := h.tmpl.RenderPage(w, "tenants", data); err != nil {
		slog.Error("admin: render tenants", "error", err)
	}
}

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	subdomain := strings.TrimSpace(r.FormValue("subdomain"))
	projectID := r.FormValue("project_id")
	nodeID := r.FormValue("node_id")

	if name == "" || subdomain == "" || projectID == "" || nodeID == "" {
		h.renderFlash(w, "flash_error", "All fields are required")
		return
	}

	if len(subdomain) > 63 || !subdomainRegexp.MatchString(subdomain) {
		h.renderFlash(w, "flash_error", "Invalid subdomain: lowercase alphanumeric with hyphens, 2-63 chars")
		return
	}
	if reservedSubdomains[subdomain] {
		h.renderFlash(w, "flash_error", "Subdomain is reserved")
		return
	}

	// Validate node
	n, err := h.nodes.GetByID(r.Context(), nodeID)
	if err != nil {
		slog.Error("admin: get node", "error", err)
		h.renderFlash(w, "flash_error", "Failed to validate node")
		return
	}
	if n == nil {
		h.renderFlash(w, "flash_error", "Node not found")
		return
	}
	if n.Status != "active" {
		h.renderFlash(w, "flash_error", "Node is not active")
		return
	}

	// Validate project
	proj, err := h.projects.GetByID(r.Context(), projectID)
	if err != nil {
		slog.Error("admin: get project", "error", err)
		h.renderFlash(w, "flash_error", "Failed to validate project")
		return
	}
	if proj == nil {
		h.renderFlash(w, "flash_error", "Project not found")
		return
	}

	// Reserve RAM
	if err := h.nodes.ReserveRAM(r.Context(), nodeID, proj.RAMMB); err != nil {
		if errors.Is(err, node.ErrInsufficientCapacity) {
			h.renderFlash(w, "flash_error", "Insufficient RAM capacity on node")
			return
		}
		slog.Error("admin: reserve ram", "error", err)
		h.renderFlash(w, "flash_error", "Failed to reserve resources")
		return
	}

	// Create tenant
	t, err := h.tenants.Create(r.Context(), tenant.CreateTenantRequest{
		Name:      name,
		ProjectID: projectID,
		NodeID:    nodeID,
		Subdomain: subdomain,
	})
	if err != nil {
		// Release RAM on failure
		if releaseErr := h.nodes.ReleaseRAM(r.Context(), nodeID, proj.RAMMB); releaseErr != nil {
			slog.Error("admin: release ram after failure", "error", releaseErr)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			h.renderFlash(w, "flash_error", "Name or subdomain already exists")
			return
		}
		slog.Error("admin: create tenant", "error", err)
		h.renderFlash(w, "flash_error", "Failed to create tenant")
		return
	}

	// Fire async provisioning
	h.provisioner.Provision(t.ID, nodeID, projectID, subdomain, proj.RAMMB)

	enriched := enrichedTenant{
		Tenant:      *t,
		ProjectName: proj.Name,
		NodeName:    n.Name,
	}

	if err := h.tmpl.RenderPartial(w, "tenant_row", enriched); err != nil {
		slog.Error("admin: render tenant row", "error", err)
	}
}

func (h *Handler) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		http.Error(w, "invalid ID", 400)
		return
	}

	t, err := h.tenants.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get tenant for delete", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if t == nil {
		http.Error(w, "not found", 404)
		return
	}

	if t.Status != "active" && t.Status != "error" {
		h.renderFlash(w, "flash_error", "Cannot delete tenant in status: "+t.Status)
		return
	}

	// Get project for RAM
	proj, err := h.projects.GetByID(r.Context(), t.ProjectID)
	if err != nil {
		slog.Error("admin: get project for delete", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	ramMB := 0
	if proj != nil {
		ramMB = proj.RAMMB
	}

	if t.LXCID != nil {
		if err := h.provisioner.Deprovision(r.Context(), t.ID, t.NodeID, *t.LXCID, ramMB); err != nil {
			if errors.Is(err, tenant.ErrStateConflict) {
				h.renderFlash(w, "flash_error", "Tenant is already being deleted")
				return
			}
			slog.Error("admin: deprovision", "error", err, "tenant_id", t.ID)
			h.renderFlash(w, "flash_error", "Failed to deprovision tenant")
			return
		}
	} else {
		if err := h.tenants.SetDeleting(r.Context(), t.ID); err != nil {
			if errors.Is(err, tenant.ErrStateConflict) {
				h.renderFlash(w, "flash_error", "Tenant is already being deleted")
				return
			}
			slog.Error("admin: set deleting", "error", err)
			h.renderFlash(w, "flash_error", "Failed to delete tenant")
			return
		}
		if ramMB > 0 {
			if err := h.nodes.ReleaseRAM(r.Context(), t.NodeID, ramMB); err != nil {
				slog.Error("admin: release ram", "error", err)
			}
		}
		if err := h.tenants.SetDeleted(r.Context(), t.ID); err != nil {
			slog.Error("admin: set deleted", "error", err)
			h.renderFlash(w, "flash_error", "Failed to delete tenant")
			return
		}
	}

	// Re-read and render updated row
	t, err = h.tenants.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get tenant after delete", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	enriched := h.enrichSingle(r.Context(), *t)
	if err := h.tmpl.RenderPartial(w, "tenant_row", enriched); err != nil {
		slog.Error("admin: render tenant row", "error", err)
	}
}

func (h *Handler) tenantRow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		w.WriteHeader(286)
		return
	}

	t, err := h.tenants.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get tenant for poll", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if t == nil {
		// 286 tells htmx to stop polling
		w.WriteHeader(286)
		return
	}

	enriched := h.enrichSingle(r.Context(), *t)
	if err := h.tmpl.RenderPartial(w, "tenant_row", enriched); err != nil {
		slog.Error("admin: render tenant row", "error", err)
	}
}

// enrichSingle resolves node/project names for a single tenant via GetByID (not List).
func (h *Handler) enrichSingle(ctx context.Context, t tenant.Tenant) enrichedTenant {
	var pName, nName string
	if p, err := h.projects.GetByID(ctx, t.ProjectID); err != nil {
		slog.Error("admin: get project for enrich", "error", err)
	} else if p != nil {
		pName = p.Name
	}
	if n, err := h.nodes.GetByID(ctx, t.NodeID); err != nil {
		slog.Error("admin: get node for enrich", "error", err)
	} else if n != nil {
		nName = n.Name
	}
	if pName == "" {
		pName = truncID(t.ProjectID)
	}
	if nName == "" {
		nName = truncID(t.NodeID)
	}
	return enrichedTenant{Tenant: t, ProjectName: pName, NodeName: nName}
}

// --- Helpers ---

func (h *Handler) renderFlash(w http.ResponseWriter, name string, msg string) {
	w.Header().Set("HX-Retarget", "#flash")
	w.Header().Set("HX-Reswap", "innerHTML")
	if err := h.tmpl.RenderPartial(w, name, msg); err != nil {
		slog.Error("admin: render flash", "error", err)
	}
}

func buildMaps(nodes []node.Node, projects []project.Project) (map[string]string, map[string]string) {
	nodeMap := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n.Name
	}
	projectMap := make(map[string]string, len(projects))
	for _, p := range projects {
		projectMap[p.ID] = p.Name
	}
	return nodeMap, projectMap
}

func enrichTenant(t tenant.Tenant, nodeMap, projectMap map[string]string) enrichedTenant {
	pName := projectMap[t.ProjectID]
	if pName == "" {
		pName = truncID(t.ProjectID)
	}
	nName := nodeMap[t.NodeID]
	if nName == "" {
		nName = truncID(t.NodeID)
	}
	return enrichedTenant{Tenant: t, ProjectName: pName, NodeName: nName}
}

func truncID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	if id == "" {
		return "?"
	}
	return id
}
