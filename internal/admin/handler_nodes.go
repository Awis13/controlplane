package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/crypto"
	"controlplane/internal/node"
	"controlplane/internal/response"
)

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

	if req.APIToken != nil && h.provisioner != nil {
		h.provisioner.InvalidateClient(id)
		slog.Info("admin: invalidated cached Proxmox client", "node_id", id)
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
