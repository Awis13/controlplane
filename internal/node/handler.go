package node

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/crypto"
	"controlplane/internal/response"
)

// NodeStore defines the data operations for nodes.
type NodeStore interface {
	List(ctx context.Context) ([]Node, error)
	ListPaginated(ctx context.Context, limit, offset int) ([]Node, int, error)
	GetByID(ctx context.Context, id string) (*Node, error)
	GetEncryptedTokenByID(ctx context.Context, id string) (string, error)
	Create(ctx context.Context, req CreateNodeRequest) (*Node, error)
	Update(ctx context.Context, id string, req UpdateNodeRequest) (*Node, error)
	Delete(ctx context.Context, id string) error
	CountTenants(ctx context.Context, nodeID string) (int, error)
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

// ProvisionerClientInvalidator can invalidate cached Proxmox clients.
type ProvisionerClientInvalidator interface {
	InvalidateClient(nodeID string)
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)

type Handler struct {
	store         NodeStore
	auditStore    *audit.Store
	encryptionKey string
	provisioner   ProvisionerClientInvalidator
}

func NewHandler(store NodeStore, auditStore *audit.Store, encryptionKey string, provisioner ProvisionerClientInvalidator) *Handler {
	return &Handler{store: store, auditStore: auditStore, encryptionKey: encryptionKey, provisioner: provisioner}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	params := response.ParseListParams(r)

	nodes, total, err := h.store.ListPaginated(r.Context(), params.Limit, params.Offset)
	if err != nil {
		slog.Error("list nodes", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list nodes")
		return
	}
	if nodes == nil {
		nodes = []Node{}
	}
	response.JSON(w, http.StatusOK, response.ListResult[Node]{
		Items:   nodes,
		Total:   total,
		Limit:   params.Limit,
		Offset:  params.Offset,
		HasMore: params.Offset+len(nodes) < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "nodeID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid node ID format")
		return
	}

	n, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get node")
		return
	}
	if n == nil {
		response.Error(w, http.StatusNotFound, "node not found")
		return
	}
	response.JSON(w, http.StatusOK, n)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateNodeRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.TailscaleIP == "" || req.ProxmoxURL == "" || req.APIToken == "" || req.TotalRAMMB <= 0 {
		response.Error(w, http.StatusBadRequest, "name, tailscale_ip, proxmox_url, api_token, and total_ram_mb are required")
		return
	}

	// Validate name
	if !nameRegexp.MatchString(req.Name) || len(req.Name) > 63 {
		response.Error(w, http.StatusBadRequest, "invalid name: must be lowercase alphanumeric with hyphens/dots/underscores, 2-63 chars")
		return
	}

	// Validate tailscale_ip
	if net.ParseIP(req.TailscaleIP) == nil {
		response.Error(w, http.StatusBadRequest, "invalid tailscale_ip: must be a valid IP address")
		return
	}

	// Validate proxmox_url
	u, err := url.Parse(req.ProxmoxURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		response.Error(w, http.StatusBadRequest, "invalid proxmox_url: must be a valid HTTPS URL")
		return
	}

	// Encrypt the API token before storing
	encrypted, err := crypto.Encrypt(req.APIToken, h.encryptionKey)
	if err != nil {
		slog.Error("encrypt api token", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to encrypt api token")
		return
	}
	req.APIToken = encrypted

	n, err := h.store.Create(r.Context(), req)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "name already exists")
			return
		}
		slog.Error("create node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create node")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "node", n.ID, map[string]string{"name": n.Name})
	}
	response.JSON(w, http.StatusCreated, n)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "nodeID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid node ID format")
		return
	}

	var req UpdateNodeRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate status if provided
	if req.Status != nil {
		switch *req.Status {
		case "active", "maintenance", "offline":
		default:
			response.Error(w, http.StatusBadRequest, "invalid status: must be active, maintenance, or offline")
			return
		}
	}

	// Validate total_ram_mb if provided
	if req.TotalRAMMB != nil && *req.TotalRAMMB <= 0 {
		response.Error(w, http.StatusBadRequest, "total_ram_mb must be positive")
		return
	}

	// Validate proxmox_url if provided
	if req.ProxmoxURL != nil {
		u, err := url.Parse(*req.ProxmoxURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			response.Error(w, http.StatusBadRequest, "invalid proxmox_url: must be a valid HTTPS URL")
			return
		}
	}

	// Encrypt API token if provided
	if req.APIToken != nil {
		encrypted, err := crypto.Encrypt(*req.APIToken, h.encryptionKey)
		if err != nil {
			slog.Error("encrypt api token", "error", err)
			response.Error(w, http.StatusInternalServerError, "failed to encrypt api token")
			return
		}
		req.APIToken = &encrypted
	}

	n, err := h.store.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, ErrNoUpdate) {
			response.Error(w, http.StatusBadRequest, "no fields to update")
			return
		}
		if errors.Is(err, ErrRAMBelowAllocated) {
			response.Error(w, http.StatusConflict, "total_ram_mb cannot be less than allocated_ram_mb")
			return
		}
		slog.Error("update node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update node")
		return
	}
	if n == nil {
		response.Error(w, http.StatusNotFound, "node not found")
		return
	}
	if req.APIToken != nil && h.provisioner != nil {
		h.provisioner.InvalidateClient(id)
		slog.Info("node: invalidated cached Proxmox client", "node_id", id)
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "node", n.ID, nil)
	}
	response.JSON(w, http.StatusOK, n)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "nodeID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid node ID format")
		return
	}

	// Guard: check for non-deleted tenants
	count, err := h.store.CountTenants(r.Context(), id)
	if err != nil {
		slog.Error("count tenants for node deletion", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to check node dependencies")
		return
	}
	if count > 0 {
		response.Error(w, http.StatusConflict, "cannot delete node with active tenants")
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "node not found")
			return
		}
		slog.Error("delete node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to delete node")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "node", id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
