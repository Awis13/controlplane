package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/project"
	"controlplane/internal/response"
)

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
		Project:    p,
		Tenants:    projectTenants,
		HasTenants: len(projectTenants) > 0,
	}

	if err := h.tmpl.RenderPage(w, "project_detail", data); err != nil {
		slog.Error("admin: render project detail", "error", err)
	}
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
