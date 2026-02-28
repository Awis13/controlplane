package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/response"
	"controlplane/internal/wireguard"
)

// WireGuardService определяет операции WireGuard, нужные для admin UI.
type WireGuardService interface {
	CreatePeer(ctx context.Context, req wireguard.CreatePeerRequest) (*wireguard.Peer, string, error)
	BuildPeerConfig(peer *wireguard.Peer, privateKey string) string
	GenerateQRCode(config string) ([]byte, error)
	RemovePeer(publicKey string) error
	ApplyPeer(peer *wireguard.Peer) error
	NetworkCIDR() string
	HubPublicKey() string
	HubEndpoint() string
}

// WireGuardStore определяет операции хранилища WireGuard для admin UI.
type WireGuardStore interface {
	List(ctx context.Context) ([]wireguard.Peer, error)
	ListByType(ctx context.Context, peerType string) ([]wireguard.Peer, error)
	GetByID(ctx context.Context, id string) (*wireguard.Peer, error)
	GetByTenantID(ctx context.Context, tenantID string) (*wireguard.Peer, error)
	Update(ctx context.Context, id string, req wireguard.UpdatePeerRequest) (*wireguard.Peer, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
}

// networkPage отображает страницу управления WireGuard сетью.
func (h *Handler) networkPage(w http.ResponseWriter, r *http.Request) {
	if h.wgStore == nil {
		http.Error(w, "WireGuard not configured", 500)
		return
	}

	// Получаем все пиры одним запросом
	allPeers, err := h.wgStore.List(r.Context())
	if err != nil {
		slog.Error("admin: list peers", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	// Считаем статистику по типам
	var adminCount, nodeCount, userCount, enabledCount int
	for _, p := range allPeers {
		switch p.Type {
		case "admin":
			adminCount++
		case "node":
			nodeCount++
		case "user":
			userCount++
		}
		if p.Enabled {
			enabledCount++
		}
	}

	// Фильтруем in-memory если указан тип
	filterType := r.URL.Query().Get("type")
	peers := allPeers
	if filterType != "" && wireguard.ValidPeerTypes[filterType] {
		filtered := make([]wireguard.Peer, 0, len(allPeers))
		for _, p := range allPeers {
			if p.Type == filterType {
				filtered = append(filtered, p)
			}
		}
		peers = filtered
	}

	data := struct {
		pageData
		Peers        []wireguard.Peer
		FilterType   string
		AdminCount   int
		NodeCount    int
		UserCount    int
		EnabledCount int
		TotalCount   int
	}{
		pageData:     newPage(r, "Network", "network", nil),
		Peers:        peers,
		FilterType:   filterType,
		AdminCount:   adminCount,
		NodeCount:    nodeCount,
		UserCount:    userCount,
		EnabledCount: enabledCount,
		TotalCount:   len(allPeers),
	}

	if err := h.tmpl.RenderPage(w, "network", data); err != nil {
		slog.Error("admin: render network", "error", err)
	}
}

// createPeerAdmin создаёт новый WireGuard пир через HTMX форму.
func (h *Handler) createPeerAdmin(w http.ResponseWriter, r *http.Request) {
	if h.wgService == nil {
		h.renderFlash(w, "flash_error", "WireGuard not configured")
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	peerType := strings.TrimSpace(r.FormValue("type"))
	endpoint := strings.TrimSpace(r.FormValue("endpoint"))
	tenantID := strings.TrimSpace(r.FormValue("tenant_id"))
	allowedIPs := strings.TrimSpace(r.FormValue("allowed_ips"))

	if name == "" || peerType == "" {
		h.renderFlash(w, "flash_error", "Name and type are required")
		return
	}

	if !wireguard.ValidPeerTypes[peerType] {
		h.renderFlash(w, "flash_error", "Invalid type: must be admin, node, or user")
		return
	}

	// Валидация AllowedIPs если указаны
	if allowedIPs != "" {
		if err := wireguard.ValidateAllowedIPs(allowedIPs); err != nil {
			h.renderFlash(w, "flash_error", "Invalid AllowedIPs: "+err.Error())
			return
		}
	}

	req := wireguard.CreatePeerRequest{
		Name:       name,
		Type:       peerType,
		Endpoint:   endpoint,
		TenantID:   tenantID,
		AllowedIPs: allowedIPs,
	}

	peer, privateKey, err := h.wgService.CreatePeer(r.Context(), req)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			h.renderFlash(w, "flash_error", "Peer with this public key or IP already exists")
			return
		}
		slog.Error("admin: create peer", "error", err)
		h.renderFlash(w, "flash_error", "Failed to create peer")
		return
	}

	// Пытаемся применить к wg0
	if applyErr := h.wgService.ApplyPeer(peer); applyErr != nil {
		slog.Warn("admin: не удалось применить пир к wg0", "peer", peer.Name, "error", applyErr)
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "wireguard_peer", peer.ID, map[string]string{
			"name": name,
			"type": peerType,
		})
	}

	// Рендерим страницу деталей пира с приватным ключом (показывается один раз)
	config := h.wgService.BuildPeerConfig(peer, privateKey)

	qrPNG, qrErr := h.wgService.GenerateQRCode(config)
	var qrBase64 string
	if qrErr == nil {
		qrBase64 = base64.StdEncoding.EncodeToString(qrPNG)
	} else {
		slog.Warn("admin: не удалось сгенерировать QR", "error", qrErr)
	}

	data := struct {
		pageData
		Peer       *wireguard.Peer
		Config     string
		QRBase64   string
		PrivateKey string
	}{
		pageData: newPage(r, "Peer: "+peer.Name, "network", []breadcrumb{
			{Label: "Network", URL: "/admin/network"},
			{Label: peer.Name},
		}),
		Peer:       peer,
		Config:     config,
		QRBase64:   qrBase64,
		PrivateKey: privateKey,
	}

	if err := h.tmpl.RenderPage(w, "peer_detail", data); err != nil {
		slog.Error("admin: render peer detail after create", "error", err)
	}
}

// peerDetail отображает детальную страницу пира.
func (h *Handler) peerDetail(w http.ResponseWriter, r *http.Request) {
	if h.wgStore == nil {
		http.Error(w, "WireGuard not configured", 500)
		return
	}

	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		http.Error(w, "invalid ID", 400)
		return
	}

	peer, err := h.wgStore.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get peer", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if peer == nil {
		http.Error(w, "not found", 404)
		return
	}

	// Конфиг без приватного ключа
	config := h.wgService.BuildPeerConfig(peer, "<PRIVATE_KEY>")

	// QR код
	qrPNG, err := h.wgService.GenerateQRCode(config)
	var qrBase64 string
	if err == nil {
		qrBase64 = base64.StdEncoding.EncodeToString(qrPNG)
	} else {
		slog.Warn("admin: не удалось сгенерировать QR", "error", err)
	}

	data := struct {
		pageData
		Peer       *wireguard.Peer
		Config     string
		QRBase64   string
		PrivateKey string
	}{
		pageData: newPage(r, "Peer: "+peer.Name, "network", []breadcrumb{
			{Label: "Network", URL: "/admin/network"},
			{Label: peer.Name},
		}),
		Peer:     peer,
		Config:   config,
		QRBase64: qrBase64,
	}

	if err := h.tmpl.RenderPage(w, "peer_detail", data); err != nil {
		slog.Error("admin: render peer detail", "error", err)
	}
}

// updatePeerAdmin обновляет пир через admin UI.
func (h *Handler) updatePeerAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid peer ID")
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderFlash(w, "flash_error", "Invalid form data")
		return
	}

	req := wireguard.UpdatePeerRequest{}
	if name := r.FormValue("name"); name != "" {
		req.Name = &name
	}
	if endpoint := r.FormValue("endpoint"); endpoint != "" {
		req.Endpoint = &endpoint
	}

	_, err := h.wgStore.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, wireguard.ErrNoUpdate) {
			h.renderFlash(w, "flash_error", "No changes to apply")
			return
		}
		slog.Error("admin: update peer", "error", err)
		h.renderFlash(w, "flash_error", "Failed to update peer")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "wireguard_peer", id, nil)
	}

	h.triggerToast(w, "Peer updated", "success")
	w.Header().Set("HX-Redirect", "/admin/network/peers/"+id)
	w.WriteHeader(http.StatusOK)
}

// deletePeerAdmin удаляет пир через admin UI.
func (h *Handler) deletePeerAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid peer ID")
		return
	}

	peer, err := h.wgStore.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get peer for delete", "error", err)
		http.Error(w, "internal error", 500)
		return
	}
	if peer == nil {
		http.Error(w, "not found", 404)
		return
	}

	// Удаляем из wg0
	if removeErr := h.wgService.RemovePeer(peer.PublicKey); removeErr != nil {
		slog.Warn("admin: не удалось удалить пир из wg0", "peer", peer.Name, "error", removeErr)
	}

	// Удаляем из БД
	if err := h.wgStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "not found", 404)
			return
		}
		slog.Error("admin: delete peer", "error", err)
		h.renderFlash(w, "flash_error", "Failed to delete peer")
		return
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "wireguard_peer", id, map[string]string{
			"name": peer.Name,
		})
	}

	w.Header().Set("HX-Redirect", "/admin/network")
	w.WriteHeader(http.StatusOK)
}

// enablePeerAdmin включает пир через admin UI.
func (h *Handler) enablePeerAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid peer ID")
		return
	}

	if err := h.wgStore.SetEnabled(r.Context(), id, true); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.renderFlash(w, "flash_error", "Peer not found")
			return
		}
		slog.Error("admin: enable peer", "error", err)
		h.renderFlash(w, "flash_error", "Failed to enable peer")
		return
	}

	// Применяем к wg0
	peer, err := h.wgStore.GetByID(r.Context(), id)
	if err == nil && peer != nil {
		if applyErr := h.wgService.ApplyPeer(peer); applyErr != nil {
			slog.Warn("admin: не удалось применить пир к wg0", "peer", peer.Name, "error", applyErr)
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "enable", "wireguard_peer", id, nil)
	}

	h.triggerToast(w, "Peer enabled", "success")
	w.Header().Set("HX-Redirect", "/admin/network/peers/"+id)
	w.WriteHeader(http.StatusOK)
}

// disablePeerAdmin отключает пир через admin UI.
func (h *Handler) disablePeerAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid peer ID")
		return
	}

	peer, err := h.wgStore.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("admin: get peer for disable", "error", err)
		h.renderFlash(w, "flash_error", "Failed to get peer")
		return
	}
	if peer == nil {
		h.renderFlash(w, "flash_error", "Peer not found")
		return
	}

	if err := h.wgStore.SetEnabled(r.Context(), id, false); err != nil {
		slog.Error("admin: disable peer", "error", err)
		h.renderFlash(w, "flash_error", "Failed to disable peer")
		return
	}

	// Удаляем из wg0
	if removeErr := h.wgService.RemovePeer(peer.PublicKey); removeErr != nil {
		slog.Warn("admin: не удалось удалить пир из wg0", "peer", peer.Name, "error", removeErr)
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "disable", "wireguard_peer", id, nil)
	}

	h.triggerToast(w, "Peer disabled", "success")
	w.Header().Set("HX-Redirect", "/admin/network/peers/"+id)
	w.WriteHeader(http.StatusOK)
}
