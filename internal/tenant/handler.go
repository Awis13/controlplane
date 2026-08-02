package tenant

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
)

// TenantStore defines the data operations for tenants.
type TenantStore interface {
	List(ctx context.Context) ([]Tenant, error)
	ListPaginated(ctx context.Context, limit, offset int, status, nodeID, projectID string) ([]Tenant, int, error)
	GetByID(ctx context.Context, id string) (*Tenant, error)
	Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error)
	Update(ctx context.Context, id string, req UpdateTenantRequest) (*Tenant, error)
	Delete(ctx context.Context, id string) error
	SetActive(ctx context.Context, id string, lxcID int) error
	SetError(ctx context.Context, id string, errMsg string) error
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
	SetSuspended(ctx context.Context, id string) error
	SetResumed(ctx context.Context, id string) error
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
	Deprovision(ctx context.Context, tenantID, nodeID, subdomain string, lxcID, ramMB int) error
	Suspend(ctx context.Context, tenantID, nodeID string, lxcID int) error
	Resume(ctx context.Context, tenantID, nodeID string, lxcID int) error
}

// Handler handles tenant HTTP requests.
type Handler struct {
	store        TenantStore
	nodeStore    NodeStore
	projectStore ProjectStore
	provisioner  Provisioner
	auditStore   *audit.Store
	lifecycle    *LifecycleService
}

func NewHandler(store TenantStore, nodeStore NodeStore, projectStore ProjectStore, provisioner Provisioner, auditStore *audit.Store) *Handler {
	return &Handler{
		store:        store,
		nodeStore:    nodeStore,
		projectStore: projectStore,
		provisioner:  provisioner,
		auditStore:   auditStore,
		lifecycle:    NewLifecycleService(store, nodeStore, projectStore, provisioner, auditStore),
	}
}

// respondLifecycle turns a lifecycle failure into an HTTP response.
func (h *Handler) respondLifecycle(w http.ResponseWriter, lerr *LifecycleError) {
	response.Error(w, lifecycleStatus(lerr), lerr.Message)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	params := response.ParseListParams(r)

	tenants, total, err := h.store.ListPaginated(r.Context(), params.Limit, params.Offset,
		params.Status, params.NodeID, params.ProjectID)
	if err != nil {
		slog.Error("list tenants", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	response.JSON(w, http.StatusOK, response.ListResult[Tenant]{
		Items:   tenants,
		Total:   total,
		Limit:   params.Limit,
		Offset:  params.Offset,
		HasMore: params.Offset+len(tenants) < total,
	})
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

	// Validate the subdomain before the node and project lookups, so a bad
	// subdomain is reported ahead of a missing node as it always has been.
	if lerr := ValidateSubdomain(req.Subdomain); lerr != nil {
		h.respondLifecycle(w, lerr)
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

	t, lerr := h.lifecycle.Create(r.Context(), CreateParams{
		Name:      req.Name,
		Subdomain: req.Subdomain,
		Project:   proj,
		Node:      n,
	}, Actor{})
	if lerr != nil {
		h.respondLifecycle(w, lerr)
		return
	}

	response.JSON(w, http.StatusAccepted, t)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	var req UpdateTenantRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	t, err := h.store.Update(r.Context(), id, req)
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
		slog.Error("update tenant", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update tenant")
		return
	}
	if t == nil {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "tenant", t.ID, nil)
	}
	response.JSON(w, http.StatusOK, t)
}

func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for suspend", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if t.Status != "active" {
		response.Error(w, http.StatusConflict, "tenant must be active to suspend")
		return
	}

	// DB first: mark as suspended
	if err := h.store.SetSuspended(r.Context(), id); err != nil {
		if errors.Is(err, ErrStateConflict) {
			response.Error(w, http.StatusConflict, "tenant is not in a suspendable state")
			return
		}
		slog.Error("set tenant suspended", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to suspend tenant")
		return
	}

	// Then stop container; rollback DB on failure
	if t.LXCID != nil {
		if err := h.provisioner.Suspend(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("suspend tenant container", "error", err)
			// Rollback: restore active state
			if rbErr := h.store.SetResumed(r.Context(), id); rbErr != nil {
				slog.Error("rollback suspend: failed to restore active state", "error", rbErr)
			}
			response.Error(w, http.StatusInternalServerError, "failed to stop container")
			return
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "suspend", "tenant", id, nil)
	}

	t, err = h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant after suspend", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	response.JSON(w, http.StatusOK, t)
}

func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for resume", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if t.Status != "suspended" {
		response.Error(w, http.StatusConflict, "tenant must be suspended to resume")
		return
	}

	// DB first: mark as active
	if err := h.store.SetResumed(r.Context(), id); err != nil {
		if errors.Is(err, ErrStateConflict) {
			response.Error(w, http.StatusConflict, "tenant is not in a resumable state")
			return
		}
		slog.Error("set tenant resumed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to resume tenant")
		return
	}

	// Then start container; rollback DB on failure
	if t.LXCID != nil {
		if err := h.provisioner.Resume(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("resume tenant container", "error", err)
			// Rollback: restore suspended state
			if rbErr := h.store.SetSuspended(r.Context(), id); rbErr != nil {
				slog.Error("rollback resume: failed to restore suspended state", "error", rbErr)
			}
			response.Error(w, http.StatusInternalServerError, "failed to start container")
			return
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "resume", "tenant", id, nil)
	}

	t, err = h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant after resume", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	response.JSON(w, http.StatusOK, t)
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

	t, lerr := h.lifecycle.Delete(r.Context(), t, Actor{})
	if lerr != nil {
		h.respondLifecycle(w, lerr)
		return
	}
	response.JSON(w, http.StatusOK, t)
}
