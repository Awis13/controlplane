package admin

import (
	"log/slog"
	"net/http"

	"controlplane/internal/audit"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/tenant"
)

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
