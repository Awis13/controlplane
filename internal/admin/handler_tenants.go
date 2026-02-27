package admin

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
	"controlplane/internal/tenant"
)

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

	name := r.FormValue("name")
	subdomain := r.FormValue("subdomain")
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

	// Update DB first (CAS guard ensures atomicity), then stop container
	if err := h.tenants.SetSuspended(r.Context(), id); err != nil {
		if errors.Is(err, tenant.ErrStateConflict) {
			h.renderFlash(w, "flash_error", "Tenant is not in a suspendable state")
			return
		}
		slog.Error("admin: set suspended", "error", err)
		h.renderFlash(w, "flash_error", "Failed to suspend tenant")
		return
	}

	if t.LXCID != nil {
		if err := h.provisioner.Suspend(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("admin: suspend container failed, rolling back", "error", err)
			// Rollback: restore active state
			if rbErr := h.tenants.SetResumed(r.Context(), id); rbErr != nil {
				slog.Error("admin: rollback suspend failed", "error", rbErr)
			}
			h.renderFlash(w, "flash_error", "Failed to stop container")
			return
		}
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

	// Update DB first (CAS guard ensures atomicity), then start container
	if err := h.tenants.SetResumed(r.Context(), id); err != nil {
		if errors.Is(err, tenant.ErrStateConflict) {
			h.renderFlash(w, "flash_error", "Tenant is not in a resumable state")
			return
		}
		slog.Error("admin: set resumed", "error", err)
		h.renderFlash(w, "flash_error", "Failed to resume tenant")
		return
	}

	if t.LXCID != nil {
		if err := h.provisioner.Resume(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("admin: resume container failed, rolling back", "error", err)
			// Rollback: restore suspended state
			if rbErr := h.tenants.SetSuspended(r.Context(), id); rbErr != nil {
				slog.Error("admin: rollback resume failed", "error", rbErr)
			}
			h.renderFlash(w, "flash_error", "Failed to start container")
			return
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "resume", "tenant", id, nil)
	}

	w.Header().Set("HX-Redirect", "/admin/tenants/"+id)
	w.WriteHeader(http.StatusOK)
}
