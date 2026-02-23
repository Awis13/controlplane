package tenant

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/response"
)

// TenantStore defines the data operations for tenants.
type TenantStore interface {
	List(ctx context.Context) ([]Tenant, error)
	GetByID(ctx context.Context, id string) (*Tenant, error)
	Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error)
	Delete(ctx context.Context, id string) error
}

var subdomainRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// Handler handles tenant HTTP requests.
type Handler struct {
	store TenantStore
}

func NewHandler(store TenantStore) *Handler {
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
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

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

	// Validate project_id is a valid UUID
	if !response.ValidUUID(req.ProjectID) {
		response.Error(w, http.StatusBadRequest, "invalid project_id format")
		return
	}

	// Validate node_id is a valid UUID
	if !response.ValidUUID(req.NodeID) {
		response.Error(w, http.StatusBadRequest, "invalid node_id format")
		return
	}

	// Validate subdomain
	if len(req.Subdomain) > 63 || !subdomainRegexp.MatchString(req.Subdomain) {
		response.Error(w, http.StatusBadRequest, "invalid subdomain: must be lowercase alphanumeric with hyphens, 2-63 chars")
		return
	}

	t, err := h.store.Create(r.Context(), req)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "name or subdomain already exists")
			return
		}
		slog.Error("create tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}
	response.JSON(w, http.StatusCreated, t)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "tenant not found")
			return
		}
		slog.Error("delete tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to delete tenant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
