package project

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/response"
)

// ProjectStore defines the data operations for projects.
type ProjectStore interface {
	List(ctx context.Context) ([]Project, error)
	Create(ctx context.Context, req CreateProjectRequest) (*Project, error)
}

// Handler handles project HTTP requests.
type Handler struct {
	store ProjectStore
}

func NewHandler(store ProjectStore) *Handler {
	return &Handler{store: store}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
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
	response.JSON(w, http.StatusCreated, p)
}
