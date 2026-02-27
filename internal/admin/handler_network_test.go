package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"controlplane/internal/wireguard"
)

// --- Mock WireGuard store ---

type mockWGStore struct {
	peers      map[string]*wireguard.Peer
	createErr  error
	updateErr  error
	deleteErr  error
	enabledErr error
}

func newMockWGStore() *mockWGStore {
	return &mockWGStore{peers: make(map[string]*wireguard.Peer)}
}

func (m *mockWGStore) List(_ context.Context) ([]wireguard.Peer, error) {
	var result []wireguard.Peer
	for _, p := range m.peers {
		result = append(result, *p)
	}
	return result, nil
}

func (m *mockWGStore) ListByType(_ context.Context, peerType string) ([]wireguard.Peer, error) {
	var result []wireguard.Peer
	for _, p := range m.peers {
		if p.Type == peerType {
			result = append(result, *p)
		}
	}
	return result, nil
}

func (m *mockWGStore) GetByID(_ context.Context, id string) (*wireguard.Peer, error) {
	p, ok := m.peers[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (m *mockWGStore) Update(_ context.Context, id string, req wireguard.UpdatePeerRequest) (*wireguard.Peer, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	p, ok := m.peers[id]
	if !ok {
		return nil, nil
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Endpoint != nil {
		p.Endpoint = req.Endpoint
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	return p, nil
}

func (m *mockWGStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.peers[id]; !ok {
		return pgx.ErrNoRows
	}
	delete(m.peers, id)
	return nil
}

func (m *mockWGStore) SetEnabled(_ context.Context, id string, enabled bool) error {
	if m.enabledErr != nil {
		return m.enabledErr
	}
	p, ok := m.peers[id]
	if !ok {
		return pgx.ErrNoRows
	}
	p.Enabled = enabled
	return nil
}

func (m *mockWGStore) GetNextAvailableIP(_ context.Context, _ string) (string, error) {
	// Простейшая логика: возвращаем IP на основе количества пиров
	return fmt.Sprintf("10.10.0.%d", len(m.peers)+2), nil
}

func (m *mockWGStore) ListEnabled(_ context.Context) ([]wireguard.Peer, error) {
	var result []wireguard.Peer
	for _, p := range m.peers {
		if p.Enabled {
			result = append(result, *p)
		}
	}
	return result, nil
}

// --- Mock WireGuard service ---

type mockWGService struct {
	createErr   error
	applyErr    error
	removeErr   error
	lastCreated *wireguard.Peer
}

func newMockWGService() *mockWGService {
	return &mockWGService{}
}

func (m *mockWGService) CreatePeer(_ context.Context, req wireguard.CreatePeerRequest) (*wireguard.Peer, string, error) {
	if m.createErr != nil {
		return nil, "", m.createErr
	}
	peer := &wireguard.Peer{
		ID:         "new-peer-id",
		Name:       req.Name,
		PublicKey:  "generated-pub-key",
		WgIP:       "10.10.0.2",
		AllowedIPs: "10.10.0.2/32",
		Type:       req.Type,
		Enabled:    true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if req.Endpoint != "" {
		peer.Endpoint = &req.Endpoint
	}
	if req.TenantID != "" {
		peer.TenantID = &req.TenantID
	}
	m.lastCreated = peer
	return peer, "generated-private-key", nil
}

func (m *mockWGService) BuildPeerConfig(peer *wireguard.Peer, privateKey string) string {
	return fmt.Sprintf("[Interface]\nAddress = %s/24\nPrivateKey = %s\n\n[Peer]\nPublicKey = hub-key\nEndpoint = 1.2.3.4:51820\n", peer.WgIP, privateKey)
}

func (m *mockWGService) GenerateQRCode(_ string) ([]byte, error) {
	// Возвращаем минимальный PNG
	return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, nil
}

func (m *mockWGService) RemovePeer(_ string) error {
	return m.removeErr
}

func (m *mockWGService) ApplyPeer(_ *wireguard.Peer) error {
	return m.applyErr
}

func (m *mockWGService) NetworkCIDR() string {
	return "10.10.0.0/24"
}

func (m *mockWGService) HubPublicKey() string {
	return "hub-public-key"
}

func (m *mockWGService) HubEndpoint() string {
	return "1.2.3.4:51820"
}

// --- Test helpers ---

const testPeerID = "44444444-4444-4444-4444-444444444444"

func testHandlerWithWG(t *testing.T) (*Handler, *mockWGStore, *mockWGService) {
	t.Helper()
	h, _, _, _, _ := testHandler(t)
	wgs := newMockWGStore()
	wgsvc := newMockWGService()
	h.SetWireGuard(wgsvc, wgs)
	return h, wgs, wgsvc
}

// --- Network page tests ---

func TestNetworkPage(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1", Type: "admin",
		WgIP: "10.10.0.2", AllowedIPs: "10.10.0.2/32", PublicKey: "pub-key-1",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/network", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNetworkPageWithFilter(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers["p1"] = &wireguard.Peer{
		ID: "p1", Name: "admin-peer", Type: "admin",
		WgIP: "10.10.0.2", AllowedIPs: "10.10.0.2/32", PublicKey: "key-1",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	wgs.peers["p2"] = &wireguard.Peer{
		ID: "p2", Name: "user-peer", Type: "user",
		WgIP: "10.10.0.3", AllowedIPs: "10.10.0.3/32", PublicKey: "key-2",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/network?type=admin", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNetworkPageNotConfigured(t *testing.T) {
	h, _, _, _, _ := testHandler(t)
	// Не вызываем SetWireGuard — wgStore == nil

	w := doRequest(t, h, "GET", "/network", nil)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- Create peer tests ---

func TestCreatePeer_Valid(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	form := url.Values{
		"name": {"my-laptop"},
		"type": {"user"},
	}
	w := doRequest(t, h, "POST", "/network/peers", form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

func TestCreatePeer_WithEndpoint(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	form := url.Values{
		"name":     {"server-node"},
		"type":     {"node"},
		"endpoint": {"5.6.7.8:51820"},
	}
	w := doRequest(t, h, "POST", "/network/peers", form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreatePeer_MissingFields(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	form := url.Values{"name": {"only-name"}}
	w := doRequest(t, h, "POST", "/network/peers", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for missing type")
	}
}

func TestCreatePeer_InvalidType(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	form := url.Values{
		"name": {"test"},
		"type": {"superadmin"},
	}
	w := doRequest(t, h, "POST", "/network/peers", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for invalid type")
	}
}

func TestCreatePeer_ServiceError(t *testing.T) {
	h, _, wgsvc := testHandlerWithWG(t)
	wgsvc.createErr = fmt.Errorf("some error")

	form := url.Values{
		"name": {"test"},
		"type": {"user"},
	}
	w := doRequest(t, h, "POST", "/network/peers", form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error on service error")
	}
}

// --- Peer detail tests ---

func TestPeerDetail(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1", Type: "admin",
		WgIP: "10.10.0.2", AllowedIPs: "10.10.0.2/32", PublicKey: "pub-key-1",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "GET", "/network/peers/"+testPeerID, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestPeerDetail_NotFound(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	w := doRequest(t, h, "GET", "/network/peers/"+testPeerID, nil)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPeerDetail_InvalidID(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	w := doRequest(t, h, "GET", "/network/peers/not-uuid", nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Update peer tests ---

func TestUpdatePeer(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1", Type: "admin",
		WgIP: "10.10.0.2", AllowedIPs: "10.10.0.2/32", PublicKey: "pub-key-1",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	form := url.Values{"name": {"updated-name"}}
	w := doRequest(t, h, "PUT", "/network/peers/"+testPeerID, form)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/admin/network/peers/"+testPeerID {
		t.Errorf("HX-Redirect = %q, want /admin/network/peers/%s", loc, testPeerID)
	}
}

func TestUpdatePeer_NoChanges(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	wgs.updateErr = wireguard.ErrNoUpdate

	form := url.Values{}
	w := doRequest(t, h, "PUT", "/network/peers/"+testPeerID, form)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for no changes")
	}
}

// --- Delete peer tests ---

func TestDeletePeer(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1", PublicKey: "pub-key-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "DELETE", "/network/peers/"+testPeerID, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/admin/network" {
		t.Errorf("HX-Redirect = %q, want /admin/network", loc)
	}
}

func TestDeletePeer_NotFound(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	w := doRequest(t, h, "DELETE", "/network/peers/"+testPeerID, nil)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- Enable/Disable tests ---

func TestEnablePeer(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1", PublicKey: "pub-key-1",
		Enabled: false, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "POST", "/network/peers/"+testPeerID+"/enable", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !wgs.peers[testPeerID].Enabled {
		t.Error("peer should be enabled after enable action")
	}
}

func TestEnablePeer_NotFound(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	w := doRequest(t, h, "POST", "/network/peers/"+testPeerID+"/enable", nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for not found")
	}
}

func TestDisablePeer(t *testing.T) {
	h, wgs, _ := testHandlerWithWG(t)
	wgs.peers[testPeerID] = &wireguard.Peer{
		ID: testPeerID, Name: "peer-1", PublicKey: "pub-key-1",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	w := doRequest(t, h, "POST", "/network/peers/"+testPeerID+"/disable", nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if wgs.peers[testPeerID].Enabled {
		t.Error("peer should be disabled after disable action")
	}
}

func TestDisablePeer_NotFound(t *testing.T) {
	h, _, _ := testHandlerWithWG(t)

	w := doRequest(t, h, "POST", "/network/peers/"+testPeerID+"/disable", nil)

	if w.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("expected flash error for not found")
	}
}
