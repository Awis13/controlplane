package tenant

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
)

// TenantStore defines the data operations for tenants.
type TenantStore interface {
	List(ctx context.Context) ([]Tenant, error)
	GetByID(ctx context.Context, id string) (*Tenant, error)
	Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error)
	Delete(ctx context.Context, id string) error
	SetActive(ctx context.Context, id string, lxcID int) error
	SetError(ctx context.Context, id string, errMsg string) error
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
}

// NodeStore defines node operations needed by the tenant handler.
type NodeStore interface {
	GetByID(ctx context.Context, id string) (*node.Node, error)
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

// ProjectStore defines project operations needed by the tenant handler.
type ProjectStore interface {
	GetByID(ctx context.Context, id string) (*project.Project, error)
}

// Provisioner defines the provisioning operations.
type Provisioner interface {
	Provision(tenantID, nodeID, projectID, subdomain string, ramMB int)
	Deprovision(ctx context.Context, tenantID, nodeID string, lxcID, ramMB int) error
}

var subdomainRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// reservedSubdomains are subdomains that cannot be used by tenants.
var reservedSubdomains = map[string]bool{
	"www": true, "api": true, "admin": true, "app": true,
	"mail": true, "smtp": true, "ftp": true, "ns1": true, "ns2": true,
	"cdn": true, "static": true, "assets": true, "media": true,
}

// Handler handles tenant HTTP requests.
type Handler struct {
	store        TenantStore
	nodeStore    NodeStore
	projectStore ProjectStore
	provisioner  Provisioner
}

func NewHandler(store TenantStore, nodeStore NodeStore, projectStore ProjectStore, provisioner Provisioner) *Handler {
	return &Handler{
		store:        store,
		nodeStore:    nodeStore,
		projectStore: projectStore,
		provisioner:  provisioner,
	}
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

	// Check reserved subdomains
	if reservedSubdomains[req.Subdomain] {
		response.Error(w, http.StatusBadRequest, "subdomain is reserved")
		return
	}

	// Validate node exists and is active
	n, err := h.nodeStore.GetByID(r.Context(), req.NodeID)
	if err != nil {
		slog.Error("get node for tenant creation", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to validate node")
		return
	}
	if n == nil {
		response.Error(w, http.StatusNotFound, "node not found")
		return
	}
	if n.Status != "active" {
		response.Error(w, http.StatusBadRequest, "node is not active")
		return
	}

	// Validate project exists
	proj, err := h.projectStore.GetByID(r.Context(), req.ProjectID)
	if err != nil {
		slog.Error("get project for tenant creation", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to validate project")
		return
	}
	if proj == nil {
		response.Error(w, http.StatusNotFound, "project not found")
		return
	}

	// Reserve RAM atomically
	if err := h.nodeStore.ReserveRAM(r.Context(), req.NodeID, proj.RAMMB); err != nil {
		if errors.Is(err, node.ErrInsufficientCapacity) {
			response.Error(w, http.StatusConflict, "insufficient capacity on node")
			return
		}
		slog.Error("reserve ram for tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to reserve resources")
		return
	}

	// Create tenant record (status=provisioning)
	t, err := h.store.Create(r.Context(), req)
	if err != nil {
		// Release RAM on failure
		if releaseErr := h.nodeStore.ReleaseRAM(r.Context(), req.NodeID, proj.RAMMB); releaseErr != nil {
			slog.Error("release ram after tenant creation failure", "error", releaseErr)
		}

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "name or subdomain already exists")
			return
		}
		slog.Error("create tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	// Launch async provisioning (fire-and-forget goroutine managed by Provisioner)
	h.provisioner.Provision(t.ID, req.NodeID, req.ProjectID, req.Subdomain, proj.RAMMB)

	response.JSON(w, http.StatusAccepted, t)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for deletion", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Only active or error tenants can be deleted
	if t.Status != "active" && t.Status != "error" {
		response.Error(w, http.StatusBadRequest, "tenant cannot be deleted in current status: "+t.Status)
		return
	}

	// Get project for RAM amount
	proj, err := h.projectStore.GetByID(r.Context(), t.ProjectID)
	if err != nil {
		slog.Error("get project for tenant deletion", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get project")
		return
	}
	ramMB := 0
	if proj != nil {
		ramMB = proj.RAMMB
	}

	// If tenant has an LXC ID, deprovision the container
	if t.LXCID != nil {
		if err := h.provisioner.Deprovision(r.Context(), t.ID, t.NodeID, *t.LXCID, ramMB); err != nil {
			if errors.Is(err, ErrStateConflict) {
				response.Error(w, http.StatusConflict, "tenant is already being deleted")
				return
			}
			slog.Error("deprovision tenant", "error", err, "tenant_id", t.ID)
			response.Error(w, http.StatusInternalServerError, "failed to deprovision tenant")
			return
		}
	} else {
		// No LXC container — atomically transition to deleting, then deleted + release RAM
		if err := h.store.SetDeleting(r.Context(), t.ID); err != nil {
			if errors.Is(err, ErrStateConflict) {
				response.Error(w, http.StatusConflict, "tenant is already being deleted")
				return
			}
			slog.Error("set tenant deleting", "error", err)
			response.Error(w, http.StatusInternalServerError, "failed to delete tenant")
			return
		}
		if ramMB > 0 {
			if err := h.nodeStore.ReleaseRAM(r.Context(), t.NodeID, ramMB); err != nil {
				slog.Error("release ram on tenant deletion", "error", err)
			}
		}
		if err := h.store.SetDeleted(r.Context(), t.ID); err != nil {
			slog.Error("set tenant deleted", "error", err)
			response.Error(w, http.StatusInternalServerError, "failed to delete tenant")
			return
		}
	}

	// Re-read tenant to return updated state
	t, err = h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant after deletion", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	response.JSON(w, http.StatusOK, t)
}
