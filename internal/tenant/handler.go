package tenant

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/response"
)

// Handler handles tenant HTTP requests.
type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("list tenants", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	response.JSON(w, http.StatusOK, tenants)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	response.JSON(w, http.StatusOK, t)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateTenantRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.ProjectID == "" || req.NodeID == "" || req.Subdomain == "" {
		response.Error(w, http.StatusBadRequest, "name, project_id, node_id, and subdomain are required")
		return
	}

	t, err := h.store.Create(r.Context(), req)
	if err != nil {
		slog.Error("create tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}
	response.JSON(w, http.StatusCreated, t)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")

	if err := h.store.Delete(r.Context(), id); err != nil {
		slog.Error("delete tenant", "error", err)
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ProjectHandler handles project HTTP requests.
type ProjectHandler struct {
	store *ProjectStore
}

func NewProjectHandler(store *ProjectStore) *ProjectHandler {
	return &ProjectHandler{store: store}
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("list projects", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	response.JSON(w, http.StatusOK, projects)
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.TemplateID <= 0 {
		response.Error(w, http.StatusBadRequest, "name and template_id are required")
		return
	}

	p, err := h.store.Create(r.Context(), req)
	if err != nil {
		slog.Error("create project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create project")
		return
	}
	response.JSON(w, http.StatusCreated, p)
}
