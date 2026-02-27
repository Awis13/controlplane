package wireguard

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/response"
)

// PeerStore определяет интерфейс для операций с пирами (для тестирования).
type PeerStore interface {
	List(ctx context.Context) ([]Peer, error)
	ListByType(ctx context.Context, peerType string) ([]Peer, error)
	GetByID(ctx context.Context, id string) (*Peer, error)
	Create(ctx context.Context, p *Peer) (*Peer, error)
	Update(ctx context.Context, id string, req UpdatePeerRequest) (*Peer, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	GetNextAvailableIP(ctx context.Context, subnet string) (string, error)
	ListEnabled(ctx context.Context) ([]Peer, error)
}

// Handler обрабатывает HTTP-запросы для WireGuard пиров.
type Handler struct {
	service    *Service
	auditStore *audit.Store
}

// NewHandler создаёт новый Handler для WireGuard пиров.
func NewHandler(service *Service, auditStore *audit.Store) *Handler {
	return &Handler{
		service:    service,
		auditStore: auditStore,
	}
}

// --- API handlers (JSON) ---

// ListPeers возвращает список всех пиров в JSON.
func (h *Handler) ListPeers(w http.ResponseWriter, r *http.Request) {
	filterType := r.URL.Query().Get("type")

	var peers []Peer
	var err error

	if filterType != "" && ValidPeerTypes[filterType] {
		peers, err = h.service.store.ListByType(r.Context(), filterType)
	} else {
		peers, err = h.service.store.List(r.Context())
	}

	if err != nil {
		slog.Error("wireguard: list peers", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list peers")
		return
	}
	if peers == nil {
		peers = []Peer{}
	}
	response.JSON(w, http.StatusOK, peers)
}

// GetPeer возвращает пир по ID.
func (h *Handler) GetPeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	peer, err := h.service.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("wireguard: get peer", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get peer")
		return
	}
	if peer == nil {
		response.Error(w, http.StatusNotFound, "peer not found")
		return
	}
	response.JSON(w, http.StatusOK, peer)
}

// CreatePeer создаёт нового пира (JSON API).
func (h *Handler) CreatePeer(w http.ResponseWriter, r *http.Request) {
	var req CreatePeerRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.Type == "" {
		response.Error(w, http.StatusBadRequest, "name and type are required")
		return
	}
	if !ValidPeerTypes[req.Type] {
		response.Error(w, http.StatusBadRequest, "invalid type: must be admin, node, or user")
		return
	}
	if req.AllowedIPs != "" {
		if err := ValidateAllowedIPs(req.AllowedIPs); err != nil {
			response.Error(w, http.StatusBadRequest, "invalid allowed_ips: "+err.Error())
			return
		}
	}
	if req.Endpoint != "" {
		if err := ValidateEndpoint(req.Endpoint); err != nil {
			response.Error(w, http.StatusBadRequest, "invalid endpoint: "+err.Error())
			return
		}
	}

	peer, privateKey, err := h.service.CreatePeer(r.Context(), req)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "peer with this public key or IP already exists")
			return
		}
		slog.Error("wireguard: create peer", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create peer")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "wireguard_peer", peer.ID, map[string]string{
			"name": req.Name,
			"type": req.Type,
		})
	}

	// Возвращаем пир + приватный ключ (только при создании)
	result := struct {
		Peer       *Peer  `json:"peer"`
		PrivateKey string `json:"private_key"`
		Config     string `json:"config"`
	}{
		Peer:       peer,
		PrivateKey: privateKey,
		Config:     h.service.BuildPeerConfig(peer, privateKey),
	}

	response.JSON(w, http.StatusCreated, result)
}

// UpdatePeer обновляет пир (JSON API).
func (h *Handler) UpdatePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	var req UpdatePeerRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	peer, err := h.service.store.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, ErrNoUpdate) {
			response.Error(w, http.StatusBadRequest, "no fields to update")
			return
		}
		slog.Error("wireguard: update peer", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update peer")
		return
	}
	if peer == nil {
		response.Error(w, http.StatusNotFound, "peer not found")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "wireguard_peer", id, nil)
	}

	response.JSON(w, http.StatusOK, peer)
}

// DeletePeer удаляет пир из БД и wg0.
func (h *Handler) DeletePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	// Получаем пир для public key (нужен для удаления из wg0)
	peer, err := h.service.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("wireguard: get peer for delete", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get peer")
		return
	}
	if peer == nil {
		response.Error(w, http.StatusNotFound, "peer not found")
		return
	}

	// Удаляем из wg0 (игнорируем ошибку — wg может быть недоступен)
	if err := h.service.RemovePeer(peer.PublicKey); err != nil {
		slog.Warn("wireguard: не удалось удалить пир из wg0", "peer", peer.Name, "error", err)
	}

	// Удаляем из БД
	if err := h.service.store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "peer not found")
			return
		}
		slog.Error("wireguard: delete peer", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to delete peer")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "wireguard_peer", id, map[string]string{
			"name": peer.Name,
		})
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// EnablePeer включает пир.
func (h *Handler) EnablePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	if err := h.service.store.SetEnabled(r.Context(), id, true); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "peer not found")
			return
		}
		slog.Error("wireguard: enable peer", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to enable peer")
		return
	}

	// Применяем к wg0
	peer, err := h.service.store.GetByID(r.Context(), id)
	if err == nil && peer != nil {
		if applyErr := h.service.ApplyPeer(peer); applyErr != nil {
			slog.Warn("wireguard: не удалось применить пир к wg0", "peer", peer.Name, "error", applyErr)
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "enable", "wireguard_peer", id, nil)
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "enabled"})
}

// DisablePeer отключает пир.
func (h *Handler) DisablePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	// Получаем пир до отключения (для public key)
	peer, err := h.service.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("wireguard: get peer for disable", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get peer")
		return
	}
	if peer == nil {
		response.Error(w, http.StatusNotFound, "peer not found")
		return
	}

	if err := h.service.store.SetEnabled(r.Context(), id, false); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "peer not found")
			return
		}
		slog.Error("wireguard: disable peer", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to disable peer")
		return
	}

	// Удаляем из wg0
	if removeErr := h.service.RemovePeer(peer.PublicKey); removeErr != nil {
		slog.Warn("wireguard: не удалось удалить пир из wg0", "peer", peer.Name, "error", removeErr)
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "disable", "wireguard_peer", id, nil)
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

// GetPeerConfig возвращает текстовый конфиг (без приватного ключа — только заглушка).
func (h *Handler) GetPeerConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	peer, err := h.service.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("wireguard: get peer for config", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get peer")
		return
	}
	if peer == nil {
		response.Error(w, http.StatusNotFound, "peer not found")
		return
	}

	// Конфиг без приватного ключа (он был показан только при создании)
	config := h.service.BuildPeerConfig(peer, "<PRIVATE_KEY>")

	response.JSON(w, http.StatusOK, map[string]string{
		"config": config,
	})
}

// GetPeerQR возвращает QR-код как PNG (base64 encoded в JSON).
func (h *Handler) GetPeerQR(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid peer ID format")
		return
	}

	peer, err := h.service.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("wireguard: get peer for QR", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get peer")
		return
	}
	if peer == nil {
		response.Error(w, http.StatusNotFound, "peer not found")
		return
	}

	// QR без приватного ключа (placeholder)
	config := h.service.BuildPeerConfig(peer, "<PRIVATE_KEY>")
	png, err := h.service.GenerateQRCode(config)
	if err != nil {
		slog.Error("wireguard: generate QR", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to generate QR code")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{
		"qr_base64": base64.StdEncoding.EncodeToString(png),
	})
}

