package tenant

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/auth"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
)

// UserNodeStore extends NodeStore with auto-selection capabilities.
type UserNodeStore interface {
	NodeStore
	GetLeastLoaded(ctx context.Context, requiredMB int) (*node.Node, error)
}

// UserTenantStore extends TenantStore with owner-scoped operations.
type UserTenantStore interface {
	TenantStore
	CreateWithOwner(ctx context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error)
	ListByOwnerID(ctx context.Context, ownerID string) ([]Tenant, error)
}

// UserProjectStore extends ProjectStore with default selection.
type UserProjectStore interface {
	ProjectStore
	GetDefault(ctx context.Context) (*project.Project, error)
}

// UserHandler handles tenant requests from authenticated users.
type UserHandler struct {
	store        UserTenantStore
	nodeStore    UserNodeStore
	projectStore UserProjectStore
	provisioner  Provisioner
}

// UserCreateRequest is the simplified tenant creation request for users.
// Project and node are auto-selected.
type UserCreateRequest struct {
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
}

func NewUserHandler(store UserTenantStore, nodeStore UserNodeStore, projectStore UserProjectStore, provisioner Provisioner) *UserHandler {
	return &UserHandler{
		store:        store,
		nodeStore:    nodeStore,
		projectStore: projectStore,
		provisioner:  provisioner,
	}
}

// List returns only the authenticated user's tenants.
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	tenants, err := h.store.ListByOwnerID(r.Context(), u.ID.String())
	if err != nil {
		slog.Error("list user tenants", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	response.JSON(w, http.StatusOK, response.ListResult[Tenant]{
		Items:   tenants,
		Total:   len(tenants),
		Limit:   len(tenants),
		Offset:  0,
		HasMore: false,
	})
}

// Get returns a single tenant owned by the authenticated user.
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get user tenant", "error", err, "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil || t.OwnerID == nil || *t.OwnerID != u.ID.String() {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	response.JSON(w, http.StatusOK, t)
}

// Create creates a tenant for the authenticated user with auto-selected project and node.
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req UserCreateRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Subdomain == "" {
		response.Error(w, http.StatusBadRequest, "name and subdomain are required")
		return
	}

	// Validate subdomain
	if len(req.Subdomain) > 63 || !subdomainRegexp.MatchString(req.Subdomain) {
		response.Error(w, http.StatusBadRequest, "invalid subdomain: must be lowercase alphanumeric with hyphens, 2-63 chars")
		return
	}
	if reservedSubdomains[req.Subdomain] {
		response.Error(w, http.StatusBadRequest, "subdomain is reserved")
		return
	}

	// Auto-select default project
	proj, err := h.projectStore.GetDefault(r.Context())
	if err != nil {
		slog.Error("get default project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to select project")
		return
	}
	if proj == nil {
		response.Error(w, http.StatusServiceUnavailable, "no projects configured")
		return
	}

	// Auto-select least loaded node with enough RAM
	n, err := h.nodeStore.GetLeastLoaded(r.Context(), proj.RAMMB)
	if err != nil {
		slog.Error("get least loaded node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to select node")
		return
	}
	if n == nil {
		response.Error(w, http.StatusServiceUnavailable, "no nodes with sufficient capacity")
		return
	}

	// Reserve RAM atomically
	if err := h.nodeStore.ReserveRAM(r.Context(), n.ID, proj.RAMMB); err != nil {
		if errors.Is(err, node.ErrInsufficientCapacity) {
			response.Error(w, http.StatusConflict, "insufficient capacity on node")
			return
		}
		slog.Error("reserve ram for user tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to reserve resources")
		return
	}

	// Create tenant with owner
	createReq := CreateTenantRequest{
		Name:      req.Name,
		ProjectID: proj.ID,
		NodeID:    n.ID,
		Subdomain: req.Subdomain,
	}
	t, err := h.store.CreateWithOwner(r.Context(), createReq, u.ID.String())
	if err != nil {
		// Release RAM on failure
		if releaseErr := h.nodeStore.ReleaseRAM(r.Context(), n.ID, proj.RAMMB); releaseErr != nil {
			slog.Error("release ram after tenant creation failure", "error", releaseErr)
		}

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "name or subdomain already exists")
			return
		}
		slog.Error("create user tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	// Launch async provisioning
	h.provisioner.Provision(t.ID, n.ID, proj.ID, req.Subdomain, proj.RAMMB)

	slog.Info("user created tenant", "user_id", u.ID, "tenant_id", t.ID, "subdomain", req.Subdomain)
	response.JSON(w, http.StatusAccepted, t)
}
