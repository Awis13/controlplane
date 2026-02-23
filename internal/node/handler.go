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

	"controlplane/internal/crypto"
	"controlplane/internal/response"
)

// NodeStore defines the data operations for nodes.
type NodeStore interface {
	List(ctx context.Context) ([]Node, error)
	GetByID(ctx context.Context, id string) (*Node, error)
	Create(ctx context.Context, req CreateNodeRequest) (*Node, error)
	Delete(ctx context.Context, id string) error
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)

type Handler struct {
	store         NodeStore
	encryptionKey string
}

func NewHandler(store NodeStore, encryptionKey string) *Handler {
	return &Handler{store: store, encryptionKey: encryptionKey}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("list nodes", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list nodes")
		return
	}
	if nodes == nil {
		nodes = []Node{}
	}
	response.JSON(w, http.StatusOK, nodes)
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
	response.JSON(w, http.StatusCreated, n)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "nodeID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid node ID format")
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
	w.WriteHeader(http.StatusNoContent)
}
