package node

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/response"
)

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
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
	if req.Name == "" || req.TailscaleIP == "" || req.ProxmoxURL == "" || req.APITokenEncrypted == "" || req.TotalRAMMB <= 0 {
		response.Error(w, http.StatusBadRequest, "name, tailscale_ip, proxmox_url, api_token_encrypted, and total_ram_mb are required")
		return
	}

	n, err := h.store.Create(r.Context(), req)
	if err != nil {
		slog.Error("create node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create node")
		return
	}
	response.JSON(w, http.StatusCreated, n)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "nodeID")

	if err := h.store.Delete(r.Context(), id); err != nil {
		slog.Error("delete node", "error", err)
		response.Error(w, http.StatusNotFound, "node not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
