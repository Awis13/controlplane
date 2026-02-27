package project

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/response"
)

// ProjectStore defines the data operations for projects.
type ProjectStore interface {
	List(ctx context.Context) ([]Project, error)
	ListPaginated(ctx context.Context, limit, offset int) ([]Project, int, error)
	GetByID(ctx context.Context, id string) (*Project, error)
	Create(ctx context.Context, req CreateProjectRequest) (*Project, error)
	Update(ctx context.Context, id string, req UpdateProjectRequest) (*Project, error)
	Delete(ctx context.Context, id string) error
	CountTenants(ctx context.Context, projectID string) (int, error)
}

// Handler handles project HTTP requests.
type Handler struct {
	store      ProjectStore
	auditStore *audit.Store
}

func NewHandler(store ProjectStore, auditStore *audit.Store) *Handler {
	return &Handler{store: store, auditStore: auditStore}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	params := response.ParseListParams(r)

	projects, total, err := h.store.ListPaginated(r.Context(), params.Limit, params.Offset)
	if err != nil {
		slog.Error("list projects", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	response.JSON(w, http.StatusOK, response.ListResult[Project]{
		Items:   projects,
		Total:   total,
		Limit:   params.Limit,
		Offset:  params.Offset,
		HasMore: params.Offset+len(projects) < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid project ID format")
		return
	}

	p, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get project")
		return
	}
	if p == nil {
		response.Error(w, http.StatusNotFound, "project not found")
		return
	}
	response.JSON(w, http.StatusOK, p)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
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
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "name already exists")
			return
		}
		slog.Error("create project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create project")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "project", p.ID, map[string]string{"name": p.Name})
	}
	response.JSON(w, http.StatusCreated, p)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid project ID format")
		return
	}

	var req UpdateProjectRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p, err := h.store.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, ErrNoUpdate) {
			response.Error(w, http.StatusBadRequest, "no fields to update")
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "name already exists")
			return
		}
		slog.Error("update project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update project")
		return
	}
	if p == nil {
		response.Error(w, http.StatusNotFound, "project not found")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "project", p.ID, nil)
	}
	response.JSON(w, http.StatusOK, p)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid project ID format")
		return
	}

	// Guard: check for non-deleted tenants
	count, err := h.store.CountTenants(r.Context(), id)
	if err != nil {
		slog.Error("count tenants for project deletion", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to check project dependencies")
		return
	}
	if count > 0 {
		response.Error(w, http.StatusConflict, "cannot delete project with active tenants")
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "project not found")
			return
		}
		slog.Error("delete project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "project", id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
