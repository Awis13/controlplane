package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/justinas/nosurf"

	"controlplane/internal/audit"
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
	Update(ctx context.Context, id string, req node.UpdateNodeRequest) (*node.Node, error)
	Delete(ctx context.Context, id string) error
	CountTenants(ctx context.Context, nodeID string) (int, error)
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

type ProjectStore interface {
	List(ctx context.Context) ([]project.Project, error)
	GetByID(ctx context.Context, id string) (*project.Project, error)
	Create(ctx context.Context, req project.CreateProjectRequest) (*project.Project, error)
	Update(ctx context.Context, id string, req project.UpdateProjectRequest) (*project.Project, error)
	Delete(ctx context.Context, id string) error
	CountTenants(ctx context.Context, projectID string) (int, error)
}

type TenantStore interface {
	List(ctx context.Context) ([]tenant.Tenant, error)
	GetByID(ctx context.Context, id string) (*tenant.Tenant, error)
	Create(ctx context.Context, req tenant.CreateTenantRequest) (*tenant.Tenant, error)
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
	SetSuspended(ctx context.Context, id string) error
	SetResumed(ctx context.Context, id string) error
}

type Provisioner interface {
	Provision(tenantID, nodeID, projectID, subdomain string, ramMB int)
	Deprovision(ctx context.Context, tenantID, nodeID string, lxcID, ramMB int) error
	Suspend(ctx context.Context, tenantID, nodeID string, lxcID int) error
	Resume(ctx context.Context, tenantID, nodeID string, lxcID int) error
}

// Handler serves the admin UI.
type Handler struct {
	tmpl             *Templates
	nodes            NodeStore
	projects         ProjectStore
	tenants          TenantStore
	auditStore       *audit.Store
	provisioner      Provisioner
	encryptionKey    string
	webauthn         *webauthn.WebAuthn
	webauthnStore    *WebAuthnStore
	webauthnSessions *webauthnSessions
}

// NewHandler creates a new admin Handler. Returns error if templates fail to parse.
func NewHandler(nodes NodeStore, projects ProjectStore, tenants TenantStore, auditStore *audit.Store, provisioner Provisioner, encryptionKey string, wa *webauthn.WebAuthn, waStore *WebAuthnStore) (*Handler, error) {
	tmpl, err := ParseTemplates()
	if err != nil {
		return nil, err
	}
	return &Handler{
		tmpl:             tmpl,
		nodes:            nodes,
		projects:         projects,
		tenants:          tenants,
		auditStore:       auditStore,
		provisioner:      provisioner,
		encryptionKey:    encryptionKey,
		webauthn:         wa,
		webauthnStore:    waStore,
		webauthnSessions: newWebAuthnSessions(),
	}, nil
}

// Routes returns a chi.Router with all admin routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	// CSRF protection for all non-GET/HEAD/OPTIONS routes
	r.Use(func(next http.Handler) http.Handler {
		csrf := nosurf.New(next)
		csrf.SetBaseCookie(http.Cookie{
			Path:     "/admin",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		// Exempt WebAuthn JSON endpoints (they have their own challenge-response)
		csrf.ExemptPaths(
			"/admin/webauthn/register/begin",
			"/admin/webauthn/register/finish",
			"/admin/webauthn/login/begin",
			"/admin/webauthn/login/finish",
		)
		return csrf
	})

	// Public routes (no auth)
	r.Handle("/static/*", http.StripPrefix("/admin/static/", http.FileServer(StaticFS())))
	r.Get("/login", h.loginPage)
	r.Post("/logout", h.logout)
	r.With(httprate.LimitByIP(5, time.Minute)).Post("/webauthn/register/begin", h.registerBegin)
	r.With(httprate.LimitByIP(5, time.Minute)).Post("/webauthn/register/finish", h.registerFinish)
	r.With(httprate.LimitByIP(10, time.Minute)).Post("/webauthn/login/begin", h.loginBegin)
	r.With(httprate.LimitByIP(10, time.Minute)).Post("/webauthn/login/finish", h.loginFinish)

	// Protected routes (require auth)
	r.Group(func(r chi.Router) {
		r.Use(requireAuth(h.encryptionKey))
		r.Use(maxBodySize(1 << 20)) // 1MB limit on all admin forms

		// Pages
		r.Get("/", h.dashboard)
		r.Get("/nodes", h.nodesList)
		r.Get("/nodes/{id}", h.nodeDetail)
		r.Get("/projects", h.projectsList)
		r.Get("/projects/{id}", h.projectDetail)
		r.Get("/tenants", h.tenantsList)
		r.Get("/tenants/{id}", h.tenantDetail)
		r.Get("/audit", h.auditPage)
		r.Get("/settings", h.settingsPage)

		// Node actions
		r.Post("/nodes", h.createNode)
		r.Put("/nodes/{id}", h.updateNodeAdmin)
		r.Delete("/nodes/{id}", h.deleteNodeAdmin)

		// Project actions
		r.Post("/projects", h.createProject)
		r.Put("/projects/{id}", h.updateProjectAdmin)
		r.Delete("/projects/{id}", h.deleteProjectAdmin)

		// Tenant actions
		r.Post("/tenants", h.createTenant)
		r.Delete("/tenants/{id}", h.deleteTenant)
		r.Post("/tenants/{id}/suspend", h.suspendTenant)
		r.Post("/tenants/{id}/resume", h.resumeTenant)
		r.Get("/tenants/{id}/row", h.tenantRow)

		// WebAuthn credential management
		r.Delete("/webauthn/credentials/{id}", h.deleteCredential)
	})

	return r
}

// --- Page data types ---

type breadcrumb struct {
	Label string
	URL   string
}

type pageData struct {
	Title       string
	Nav         string
	Breadcrumbs []breadcrumb
	CSRFToken   string
}

// newPage creates pageData with CSRF token from the request.
func newPage(r *http.Request, title, nav string, crumbs []breadcrumb) pageData {
	return pageData{
		Title:       title,
		Nav:         nav,
		Breadcrumbs: crumbs,
		CSRFToken:   nosurf.Token(r),
	}
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

	// Tenant status counts
	var activeTenants, suspendedTenants, errorTenants int
	for _, t := range tenants {
		switch t.Status {
		case "active":
			activeTenants++
		case "suspended":
			suspendedTenants++
		case "error":
			errorTenants++
		}
	}

	// Health counts
	var healthyCount, unhealthyCount, unknownHealthCount int
	for _, t := range tenants {
		if t.Status != "active" {
			continue
		}
		switch t.HealthStatus {
		case "healthy":
			healthyCount++
		case "unhealthy":
			unhealthyCount++
		default:
			unknownHealthCount++
		}
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

	// Recent audit entries
	var recentAudit []audit.Entry
	if h.auditStore != nil {
		recentAudit, _, _ = h.auditStore.List(r.Context(), 10, 0, "", "")
	}
	if recentAudit == nil {
		recentAudit = []audit.Entry{}
	}

	data := struct {
		pageData
		Nodes              []node.Node
		Projects           []project.Project
		Tenants            []tenant.Tenant
		TotalRAM           int
		AllocatedRAM       int
		ActiveTenants      int
		SuspendedTenants   int
		ErrorTenants       int
		HealthyCount       int
		UnhealthyCount     int
		UnknownHealthCount int
		RecentTenants      []enrichedTenant
		RecentAudit        []audit.Entry
	}{
		pageData:           newPage(r, "Dashboard", "dashboard", nil),
		Nodes:              nodes,
		Projects:           projects,
		Tenants:            tenants,
		TotalRAM:           totalRAM,
		AllocatedRAM:       allocRAM,
		ActiveTenants:      activeTenants,
		SuspendedTenants:   suspendedTenants,
		ErrorTenants:       errorTenants,
		HealthyCount:       healthyCount,
		UnhealthyCount:     unhealthyCount,
		UnknownHealthCount: unknownHealthCount,
		RecentTenants:      recent,
		RecentAudit:        recentAudit,
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
		pageData: newPage(r, "Nodes", "nodes", nil),
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

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "node", n.ID, map[string]string{"name": n.Name})
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
		pageData: newPage(r, "Projects", "projects", nil),
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

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "project", p.ID, map[string]string{"name": p.Name})
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
	allTenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: list tenants", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	// Apply filters
	filterStatus := r.URL.Query().Get("status")
	filterProject := r.URL.Query().Get("project_id")
	filterNode := r.URL.Query().Get("node_id")

	var filtered []tenant.Tenant
	for _, t := range allTenants {
		if filterStatus != "" && t.Status != filterStatus {
			continue
		}
		if filterProject != "" && t.ProjectID != filterProject {
			continue
		}
		if filterNode != "" && t.NodeID != filterNode {
			continue
		}
		filtered = append(filtered, t)
	}

	nodeMap, projectMap := buildMaps(nodes, projects)

	enriched := make([]enrichedTenant, len(filtered))
	for i, t := range filtered {
		enriched[i] = enrichTenant(t, nodeMap, projectMap)
	}

	data := struct {
		pageData
		Nodes         []node.Node
		Projects      []project.Project
		Tenants       []enrichedTenant
		FilterStatus  string
		FilterProject string
		FilterNode    string
		TotalCount    int
	}{
		pageData:      newPage(r, "Tenants", "tenants", nil),
		Nodes:         nodes,
		Projects:      projects,
		Tenants:       enriched,
		FilterStatus:  filterStatus,
		FilterProject: filterProject,
		FilterNode:    filterNode,
		TotalCount:    len(allTenants),
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

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "tenant", t.ID, map[string]string{"name": name, "subdomain": subdomain})
	}

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

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "tenant", id, nil)
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

// triggerToast sets an HX-Trigger header to display a toast notification via Alpine.js.
func (h *Handler) triggerToast(w http.ResponseWriter, msg, toastType string) {
	trigger := map[string]any{
		"showToast": map[string]string{"message": msg, "type": toastType},
	}
	b, _ := json.Marshal(trigger)
	w.Header().Set("HX-Trigger", string(b))
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

// maxBodySize limits request body size for all routes in the group.
func maxBodySize(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// --- Detail pages ---

func (h *Handler) nodeDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		http.Error(w, "invalid ID", 400)
		return
	}

	n, err := h.nodes.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get node", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if n == nil {
		http.Error(w, "not found", 404)
		return
	}

	// Get tenants on this node
	allTenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: list tenants for node detail", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	projects, err := h.projects.List(r.Context())
	if err != nil {
		slog.Error("admin: list projects", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	_, projectMap := buildMaps(nil, projects)

	var nodeTenants []enrichedTenant
	for _, t := range allTenants {
		if t.NodeID == id && t.Status != "deleted" {
			pName := projectMap[t.ProjectID]
			if pName == "" {
				pName = truncID(t.ProjectID)
			}
			nodeTenants = append(nodeTenants, enrichedTenant{Tenant: t, ProjectName: pName, NodeName: n.Name})
		}
	}

	data := struct {
		pageData
		Node       *node.Node
		Tenants    []enrichedTenant
		HasTenants bool
	}{
		pageData: newPage(r, "Node: "+n.Name, "nodes", []breadcrumb{
			{Label: "Nodes", URL: "/admin/nodes"},
			{Label: n.Name},
		}),
		Node:       n,
		Tenants:    nodeTenants,
		HasTenants: len(nodeTenants) > 0,
	}

	if err := h.tmpl.RenderPage(w, "node_detail", data); err != nil {
		slog.Error("admin: render node detail", "error", err)
	}
}

func (h *Handler) projectDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		http.Error(w, "invalid ID", 400)
		return
	}

	p, err := h.projects.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get project", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if p == nil {
		http.Error(w, "not found", 404)
		return
	}

	// Get tenants using this project
	allTenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: list tenants for project detail", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	nodes, err := h.nodes.List(r.Context())
	if err != nil {
		slog.Error("admin: list nodes", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	nodeMap, _ := buildMaps(nodes, nil)

	var projectTenants []enrichedTenant
	for _, t := range allTenants {
		if t.ProjectID == id && t.Status != "deleted" {
			nName := nodeMap[t.NodeID]
			if nName == "" {
				nName = truncID(t.NodeID)
			}
			projectTenants = append(projectTenants, enrichedTenant{Tenant: t, ProjectName: p.Name, NodeName: nName})
		}
	}

	data := struct {
		pageData
		Project    *project.Project
		Tenants    []enrichedTenant
		HasTenants bool
	}{
		pageData: newPage(r, "Project: "+p.Name, "projects", []breadcrumb{
			{Label: "Projects", URL: "/admin/projects"},
			{Label: p.Name},
		}),
		Project: p,
		Tenants:    projectTenants,
		HasTenants: len(projectTenants) > 0,
	}

	if err := h.tmpl.RenderPage(w, "project_detail", data); err != nil {
		slog.Error("admin: render project detail", "error", err)
	}
}

func (h *Handler) tenantDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		http.Error(w, "invalid ID", 400)
		return
	}

	t, err := h.tenants.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get tenant", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if t == nil {
		http.Error(w, "not found", 404)
		return
	}

	enriched := h.enrichSingle(r.Context(), *t)

	// Get audit entries
	var auditEntries []audit.Entry
	if h.auditStore != nil {
		auditEntries, err = h.auditStore.ListByEntity(r.Context(), "tenant", id, 20)
		if err != nil {
			slog.Error("admin: get audit entries for tenant", "error", err)
		}
	}

	data := struct {
		pageData
		Tenant       tenant.Tenant
		ProjectName  string
		NodeName     string
		AuditEntries []audit.Entry
	}{
		pageData: newPage(r, "Tenant: "+t.Name, "tenants", []breadcrumb{
			{Label: "Tenants", URL: "/admin/tenants"},
			{Label: t.Name},
		}),
		Tenant:       *t,
		ProjectName:  enriched.ProjectName,
		NodeName:     enriched.NodeName,
		AuditEntries: auditEntries,
	}

	if err := h.tmpl.RenderPage(w, "tenant_detail", data); err != nil {
		slog.Error("admin: render tenant detail", "error", err)
	}
}

// --- Audit page ---

func (h *Handler) auditPage(w http.ResponseWriter, r *http.Request) {
	params := response.ParseListParams(r)
	entityType := r.URL.Query().Get("entity_type")
	action := r.URL.Query().Get("action")

	var entries []audit.Entry
	var total int
	var err error

	if h.auditStore != nil {
		entries, total, err = h.auditStore.List(r.Context(), params.Limit, params.Offset, entityType, action)
		if err != nil {
			slog.Error("admin: list audit", "error", err)
			http.Error(w, "internal error", 500)
			return
		}
	}

	if entries == nil {
		entries = []audit.Entry{}
	}

	data := struct {
		pageData
		Entries          []audit.Entry
		Total            int
		Limit            int
		Offset           int
		HasMore          bool
		FilterEntityType string
		FilterAction     string
	}{
		pageData: newPage(r, "Audit Log", "audit", []breadcrumb{
			{Label: "Audit Log"},
		}),
		Entries:          entries,
		Total:            total,
		Limit:            params.Limit,
		Offset:           params.Offset,
		HasMore:          params.Offset+len(entries) < total,
		FilterEntityType: entityType,
		FilterAction:     action,
	}

	if err := h.tmpl.RenderPage(w, "audit", data); err != nil {
		slog.Error("admin: render audit", "error", err)
	}
}

// --- Settings page ---

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	dbStatus := "healthy"

	nodes, err := h.nodes.List(r.Context())
	if err != nil {
		slog.Error("admin: settings list nodes", "error", err)
		dbStatus = "error"
	}

	allTenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: settings list tenants", "error", err)
		dbStatus = "error"
	}

	activeTenants := 0
	for _, t := range allTenants {
		if t.Status == "active" {
			activeTenants++
		}
	}

	var credentials []CredentialInfo
	if h.webauthnStore != nil {
		var credErr error
		credentials, credErr = h.webauthnStore.ListCredentialInfos(r.Context())
		if credErr != nil {
			slog.Error("admin: settings list credentials", "error", credErr)
		}
	}

	data := struct {
		pageData
		DBStatus      string
		NodeCount     int
		ActiveTenants int
		Credentials   []CredentialInfo
	}{
		pageData: newPage(r, "Settings", "settings", []breadcrumb{
			{Label: "Settings"},
		}),
		DBStatus:      dbStatus,
		NodeCount:     len(nodes),
		ActiveTenants: activeTenants,
		Credentials:   credentials,
	}

	if err := h.tmpl.RenderPage(w, "settings", data); err != nil {
		slog.Error("admin: render settings", "error", err)
	}
}

func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid credential ID")
		return
	}

	if err := h.webauthnStore.DeleteCredential(r.Context(), id); err != nil {
		slog.Error("admin: delete credential", "error", err)
		h.renderFlash(w, "flash_error", "Failed to delete credential")
		return
	}

	w.Header().Set("HX-Redirect", "/admin/settings")
	w.WriteHeader(http.StatusOK)
}

// --- Admin CRUD actions ---

func (h *Handler) updateNodeAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid node ID")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	req := node.UpdateNodeRequest{}
	if s := r.FormValue("status"); s != "" {
		validStatuses := map[string]bool{"active": true, "maintenance": true, "offline": true}
		if !validStatuses[s] {
			h.renderFlash(w, "flash_error", "Invalid status")
			return
		}
		req.Status = &s
	}
	if ramStr := r.FormValue("total_ram_mb"); ramStr != "" {
		ram, err := strconv.Atoi(ramStr)
		if err != nil || ram <= 0 {
			h.renderFlash(w, "flash_error", "Invalid RAM value")
			return
		}
		req.TotalRAMMB = &ram
	}

	_, err := h.nodes.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, node.ErrNoUpdate) {
			h.renderFlash(w, "flash_error", "No changes to apply")
			return
		}
		if errors.Is(err, node.ErrRAMBelowAllocated) {
			h.renderFlash(w, "flash_error", "Total RAM cannot be less than allocated RAM")
			return
		}
		slog.Error("admin: update node", "error", err)
		h.renderFlash(w, "flash_error", "Failed to update node")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "node", id, nil)
	}

	h.triggerToast(w, "Node updated", "success")
	w.Header().Set("HX-Redirect", "/admin/nodes/"+id)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteNodeAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid node ID")
		return
	}

	count, err := h.nodes.CountTenants(r.Context(), id)
	if err != nil {
		slog.Error("admin: count tenants for node", "error", err)
		h.renderFlash(w, "flash_error", "Failed to check node dependencies")
		return
	}
	if count > 0 {
		h.renderFlash(w, "flash_error", fmt.Sprintf("Cannot delete node: %d active tenant(s)", count))
		return
	}

	if err := h.nodes.Delete(r.Context(), id); err != nil {
		slog.Error("admin: delete node", "error", err)
		h.renderFlash(w, "flash_error", "Failed to delete node")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "node", id, nil)
	}

	w.Header().Set("HX-Redirect", "/admin/nodes")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) updateProjectAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid project ID")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	req := project.UpdateProjectRequest{}
	if s := strings.TrimSpace(r.FormValue("name")); s != "" {
		req.Name = &s
	}
	if s := r.FormValue("template_id"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			h.renderFlash(w, "flash_error", "Invalid template ID")
			return
		}
		req.TemplateID = &v
	}
	if s := r.FormValue("ram_mb"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			h.renderFlash(w, "flash_error", "Invalid RAM value")
			return
		}
		req.RAMMB = &v
	}
	if s := strings.TrimSpace(r.FormValue("health_path")); s != "" {
		req.HealthPath = &s
	}

	_, err := h.projects.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, project.ErrNoUpdate) {
			h.renderFlash(w, "flash_error", "No changes to apply")
			return
		}
		slog.Error("admin: update project", "error", err)
		h.renderFlash(w, "flash_error", "Failed to update project")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "project", id, nil)
	}

	h.triggerToast(w, "Project updated", "success")
	w.Header().Set("HX-Redirect", "/admin/projects/"+id)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteProjectAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid project ID")
		return
	}

	count, err := h.projects.CountTenants(r.Context(), id)
	if err != nil {
		slog.Error("admin: count tenants for project", "error", err)
		h.renderFlash(w, "flash_error", "Failed to check project dependencies")
		return
	}
	if count > 0 {
		h.renderFlash(w, "flash_error", fmt.Sprintf("Cannot delete project: %d active tenant(s)", count))
		return
	}

	if err := h.projects.Delete(r.Context(), id); err != nil {
		slog.Error("admin: delete project", "error", err)
		h.renderFlash(w, "flash_error", "Failed to delete project")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "project", id, nil)
	}

	w.Header().Set("HX-Redirect", "/admin/projects")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) suspendTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid tenant ID")
		return
	}

	t, err := h.tenants.GetByID(r.Context(), id)
	if err != nil || t == nil {
		h.renderFlash(w, "flash_error", "Tenant not found")
		return
	}

	if t.Status != "active" {
		h.renderFlash(w, "flash_error", "Tenant must be active to suspend")
		return
	}

	// Stop container
	if t.LXCID != nil {
		if err := h.provisioner.Suspend(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("admin: suspend tenant", "error", err)
			h.renderFlash(w, "flash_error", "Failed to stop container")
			return
		}
	}

	if err := h.tenants.SetSuspended(r.Context(), id); err != nil {
		if errors.Is(err, tenant.ErrStateConflict) {
			h.renderFlash(w, "flash_error", "Tenant is not in a suspendable state")
			return
		}
		slog.Error("admin: set suspended", "error", err)
		h.renderFlash(w, "flash_error", "Failed to suspend tenant")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "suspend", "tenant", id, nil)
	}

	w.Header().Set("HX-Redirect", "/admin/tenants/"+id)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) resumeTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid tenant ID")
		return
	}

	t, err := h.tenants.GetByID(r.Context(), id)
	if err != nil || t == nil {
		h.renderFlash(w, "flash_error", "Tenant not found")
		return
	}

	if t.Status != "suspended" {
		h.renderFlash(w, "flash_error", "Tenant must be suspended to resume")
		return
	}

	// Start container
	if t.LXCID != nil {
		if err := h.provisioner.Resume(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("admin: resume tenant", "error", err)
			h.renderFlash(w, "flash_error", "Failed to start container")
			return
		}
	}

	if err := h.tenants.SetResumed(r.Context(), id); err != nil {
		if errors.Is(err, tenant.ErrStateConflict) {
			h.renderFlash(w, "flash_error", "Tenant is not in a resumable state")
			return
		}
		slog.Error("admin: set resumed", "error", err)
		h.renderFlash(w, "flash_error", "Failed to resume tenant")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "resume", "tenant", id, nil)
	}

	w.Header().Set("HX-Redirect", "/admin/tenants/"+id)
	w.WriteHeader(http.StatusOK)
}
