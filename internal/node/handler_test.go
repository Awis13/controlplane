package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/crypto"
)

const (
	validNodeID = "11111111-1111-1111-1111-111111111111"
	// A 32-byte key, hex encoded, as crypto.Encrypt expects.
	testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var errStore = errors.New("database is down")

// --- Mock store ---

type mockStore struct {
	nodes map[string]*Node

	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
	countErr  error
	tenants   int
	total     int

	createdToken string // what Create was handed, to check it was encrypted
	updatedToken string
}

func newMockStore() *mockStore {
	return &mockStore{nodes: make(map[string]*Node)}
}

func (m *mockStore) List(context.Context) ([]Node, error) {
	var out []Node
	for _, n := range m.nodes {
		out = append(out, *n)
	}
	return out, m.listErr
}

func (m *mockStore) ListPaginated(_ context.Context, limit, offset int) ([]Node, int, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	var out []Node
	for _, n := range m.nodes {
		out = append(out, *n)
	}
	total := m.total
	if total == 0 {
		total = len(out)
	}
	return out, total, nil
}

func (m *mockStore) GetByID(_ context.Context, id string) (*Node, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.nodes[id], nil
}

func (m *mockStore) GetEncryptedTokenByID(context.Context, string) (string, error) {
	return "", nil
}

func (m *mockStore) Create(_ context.Context, req CreateNodeRequest) (*Node, error) {
	m.createdToken = req.APIToken
	if m.createErr != nil {
		return nil, m.createErr
	}
	n := &Node{
		ID: "new-node-id", Name: req.Name, TailscaleIP: req.TailscaleIP,
		ProxmoxURL: req.ProxmoxURL, TotalRAMMB: req.TotalRAMMB, Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.nodes[n.ID] = n
	return n, nil
}

func (m *mockStore) Update(_ context.Context, id string, req UpdateNodeRequest) (*Node, error) {
	if req.APIToken != nil {
		m.updatedToken = *req.APIToken
	}
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	n, ok := m.nodes[id]
	if !ok {
		return nil, nil
	}
	if req.Status != nil {
		n.Status = *req.Status
	}
	if req.TotalRAMMB != nil {
		n.TotalRAMMB = *req.TotalRAMMB
	}
	return n, nil
}

func (m *mockStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.nodes, id)
	return nil
}

func (m *mockStore) CountTenants(context.Context, string) (int, error) {
	return m.tenants, m.countErr
}

func (m *mockStore) ReserveRAM(context.Context, string, int) error { return nil }
func (m *mockStore) ReleaseRAM(context.Context, string, int) error { return nil }

// mockInvalidator records the cache invalidations the handler requests.
type mockInvalidator struct {
	invalidated []string
}

func (m *mockInvalidator) InvalidateClient(nodeID string) {
	m.invalidated = append(m.invalidated, nodeID)
}

// --- Helpers ---

func testRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/nodes", h.List)
	r.Post("/nodes", h.Create)
	r.Get("/nodes/{nodeID}", h.Get)
	r.Put("/nodes/{nodeID}", h.Update)
	r.Delete("/nodes/{nodeID}", h.Delete)
	return r
}

func do(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	testRouter(h).ServeHTTP(rec, req)
	return rec
}

func seed(m *mockStore) *Node {
	n := &Node{
		ID: validNodeID, Name: "node-1", TailscaleIP: "100.64.0.1",
		ProxmoxURL: "https://10.0.0.1:8006", TotalRAMMB: 65536, Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.nodes[n.ID] = n
	return n
}

func newTestHandler(store *mockStore) *Handler {
	return NewHandler(store, nil, testKey, nil)
}

const validCreateBody = `{"name":"node-1","tailscale_ip":"100.64.0.1","proxmox_url":"https://10.0.0.1:8006","api_token":"secret-token","total_ram_mb":65536}`

// --- List ---

func TestList_Empty(t *testing.T) {
	rec := do(t, newTestHandler(newMockStore()), http.MethodGet, "/nodes", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want an empty items array rather than null", rec.Body.String())
	}
}

func TestList_ReturnsNodes(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, newTestHandler(store), http.MethodGet, "/nodes", "")

	var body struct {
		Items []Node `json:"items"`
		Total int    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Name != "node-1" {
		t.Errorf("items = %+v", body.Items)
	}
}

func TestList_StoreError(t *testing.T) {
	store := newMockStore()
	store.listErr = errStore

	rec := do(t, newTestHandler(store), http.MethodGet, "/nodes", "")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Get ---

func TestGet_Found(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, newTestHandler(store), http.MethodGet, "/nodes/"+validNodeID, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGet_NotFound(t *testing.T) {
	rec := do(t, newTestHandler(newMockStore()), http.MethodGet, "/nodes/"+validNodeID, "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGet_InvalidID(t *testing.T) {
	rec := do(t, newTestHandler(newMockStore()), http.MethodGet, "/nodes/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGet_StoreError(t *testing.T) {
	store := newMockStore()
	store.getErr = errStore

	rec := do(t, newTestHandler(store), http.MethodGet, "/nodes/"+validNodeID, "")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Create ---

func TestCreate_Success(t *testing.T) {
	store := newMockStore()

	rec := do(t, newTestHandler(store), http.MethodPost, "/nodes", validCreateBody)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var got Node
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "node-1" || got.TotalRAMMB != 65536 {
		t.Errorf("created = %+v", got)
	}
}

// TestCreate_TokenIsEncryptedBeforeStorage is the one that matters most here:
// the Proxmox API token is a credential, and the handler must not hand it to
// the store in the clear.
func TestCreate_TokenIsEncryptedBeforeStorage(t *testing.T) {
	store := newMockStore()

	do(t, newTestHandler(store), http.MethodPost, "/nodes", validCreateBody)

	if store.createdToken == "" {
		t.Fatal("the store was never given a token")
	}
	if store.createdToken == "secret-token" {
		t.Fatal("the token reached the store in plaintext")
	}
	decrypted, err := crypto.Decrypt(store.createdToken, testKey)
	if err != nil {
		t.Fatalf("stored token does not decrypt: %v", err)
	}
	if decrypted != "secret-token" {
		t.Errorf("decrypted = %q, want the original token", decrypted)
	}
}

func TestCreate_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing name", body: `{"tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "missing tailscale_ip", body: `{"name":"n1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "missing proxmox_url", body: `{"name":"n1","tailscale_ip":"100.64.0.1","api_token":"t","total_ram_mb":1}`},
		{name: "missing api_token", body: `{"name":"n1","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","total_ram_mb":1}`},
		{name: "zero ram", body: `{"name":"n1","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":0}`},
		{name: "negative ram", body: `{"name":"n1","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":-1}`},
		{name: "uppercase name", body: `{"name":"Node1","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "name with space", body: `{"name":"node 1","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "name starting with a hyphen", body: `{"name":"-node","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "not an ip", body: `{"name":"n1","tailscale_ip":"not-an-ip","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "http url", body: `{"name":"n1","tailscale_ip":"100.64.0.1","proxmox_url":"http://h:8006","api_token":"t","total_ram_mb":1}`},
		{name: "url without host", body: `{"name":"n1","tailscale_ip":"100.64.0.1","proxmox_url":"https://","api_token":"t","total_ram_mb":1}`},
		{name: "malformed json", body: `{"name":`},
		{name: "unknown field", body: `{"name":"n1","tailscale_ip":"100.64.0.1","proxmox_url":"https://h:8006","api_token":"t","total_ram_mb":1,"nope":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			rec := do(t, newTestHandler(store), http.MethodPost, "/nodes", tt.body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if len(store.nodes) != 0 {
				t.Error("a rejected request must not create a node")
			}
		})
	}
}

// TestCreate_AcceptsIPv6TailscaleAddress pins that the IP check is a real
// parse rather than a v4-shaped pattern.
func TestCreate_AcceptsIPv6TailscaleAddress(t *testing.T) {
	store := newMockStore()

	rec := do(t, newTestHandler(store), http.MethodPost, "/nodes",
		`{"name":"node-1","tailscale_ip":"fd7a:115c:a1e0::1","proxmox_url":"https://10.0.0.1:8006","api_token":"t","total_ram_mb":1024}`)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	store := newMockStore()
	store.createErr = &pgconn.PgError{Code: "23505"}

	rec := do(t, newTestHandler(store), http.MethodPost, "/nodes", validCreateBody)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestCreate_StoreError(t *testing.T) {
	store := newMockStore()
	store.createErr = errStore

	rec := do(t, newTestHandler(store), http.MethodPost, "/nodes", validCreateBody)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestCreate_EncryptionFailure pins that a bad encryption key fails the
// request rather than storing something unusable.
func TestCreate_EncryptionFailure(t *testing.T) {
	store := newMockStore()
	h := NewHandler(store, nil, "not-a-valid-key", nil)

	rec := do(t, h, http.MethodPost, "/nodes", validCreateBody)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if len(store.nodes) != 0 {
		t.Error("nothing should be stored when the token could not be encrypted")
	}
}

// --- Update ---

func TestUpdate_Success(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, newTestHandler(store), http.MethodPut, "/nodes/"+validNodeID, `{"status":"maintenance"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if store.nodes[validNodeID].Status != "maintenance" {
		t.Errorf("status = %q, want maintenance", store.nodes[validNodeID].Status)
	}
}

func TestUpdate_StatusValues(t *testing.T) {
	tests := []struct {
		status     string
		wantStatus int
	}{
		{status: "active", wantStatus: http.StatusOK},
		{status: "maintenance", wantStatus: http.StatusOK},
		{status: "offline", wantStatus: http.StatusOK},
		{status: "deleted", wantStatus: http.StatusBadRequest},
		{status: "ACTIVE", wantStatus: http.StatusBadRequest},
		{status: "", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run("status="+tt.status, func(t *testing.T) {
			store := newMockStore()
			seed(store)

			rec := do(t, newTestHandler(store), http.MethodPut, "/nodes/"+validNodeID,
				`{"status":"`+tt.status+`"}`)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestUpdate_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "zero ram", body: `{"total_ram_mb":0}`},
		{name: "negative ram", body: `{"total_ram_mb":-100}`},
		{name: "http url", body: `{"proxmox_url":"http://h:8006"}`},
		{name: "url without host", body: `{"proxmox_url":"https://"}`},
		{name: "malformed json", body: `{"status":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			seed(store)

			rec := do(t, newTestHandler(store), http.MethodPut, "/nodes/"+validNodeID, tt.body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestUpdate_TokenIsEncryptedAndInvalidatesTheClient pins both halves of a
// token rotation: the new token is encrypted before storage, and the cached
// Proxmox client is dropped so the next call uses the new credential.
func TestUpdate_TokenIsEncryptedAndInvalidatesTheClient(t *testing.T) {
	store := newMockStore()
	seed(store)
	inv := &mockInvalidator{}
	h := NewHandler(store, nil, testKey, inv)

	rec := do(t, h, http.MethodPut, "/nodes/"+validNodeID, `{"api_token":"rotated-token"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if store.updatedToken == "rotated-token" {
		t.Fatal("the token reached the store in plaintext")
	}
	decrypted, err := crypto.Decrypt(store.updatedToken, testKey)
	if err != nil {
		t.Fatalf("stored token does not decrypt: %v", err)
	}
	if decrypted != "rotated-token" {
		t.Errorf("decrypted = %q", decrypted)
	}
	if len(inv.invalidated) != 1 || inv.invalidated[0] != validNodeID {
		t.Errorf("invalidated = %v, want the cached client for this node to be dropped", inv.invalidated)
	}
}

// TestUpdate_WithoutTokenLeavesTheClientCached pins the other side, so the
// invalidation is tied to a token change rather than to any update.
func TestUpdate_WithoutTokenLeavesTheClientCached(t *testing.T) {
	store := newMockStore()
	seed(store)
	inv := &mockInvalidator{}
	h := NewHandler(store, nil, testKey, inv)

	do(t, h, http.MethodPut, "/nodes/"+validNodeID, `{"status":"offline"}`)

	if len(inv.invalidated) != 0 {
		t.Errorf("invalidated = %v, want none: the credential did not change", inv.invalidated)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	rec := do(t, newTestHandler(newMockStore()), http.MethodPut, "/nodes/"+validNodeID, `{"status":"active"}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestUpdate_InvalidID(t *testing.T) {
	rec := do(t, newTestHandler(newMockStore()), http.MethodPut, "/nodes/not-a-uuid", `{"status":"active"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestUpdate_StoreErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "no fields to update", err: ErrNoUpdate, wantStatus: http.StatusBadRequest},
		{name: "ram below allocated", err: ErrRAMBelowAllocated, wantStatus: http.StatusConflict},
		{name: "database down", err: errStore, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			seed(store)
			store.updateErr = tt.err

			rec := do(t, newTestHandler(store), http.MethodPut, "/nodes/"+validNodeID, `{"status":"active"}`)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestUpdate_EncryptionFailure(t *testing.T) {
	store := newMockStore()
	seed(store)
	h := NewHandler(store, nil, "not-a-valid-key", nil)

	rec := do(t, h, http.MethodPut, "/nodes/"+validNodeID, `{"api_token":"rotated"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Delete ---

func TestDelete_Success(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, newTestHandler(store), http.MethodDelete, "/nodes/"+validNodeID, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, still := store.nodes[validNodeID]; still {
		t.Error("node was not deleted")
	}
}

// TestDelete_RefusedWhileTenantsExist pins the dependency guard: a node still
// hosting tenants cannot be removed out from under them.
func TestDelete_RefusedWhileTenantsExist(t *testing.T) {
	store := newMockStore()
	seed(store)
	store.tenants = 2

	rec := do(t, newTestHandler(store), http.MethodDelete, "/nodes/"+validNodeID, "")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if _, gone := store.nodes[validNodeID]; !gone {
		t.Error("the node must survive a refused delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	store := newMockStore()
	store.deleteErr = pgx.ErrNoRows

	rec := do(t, newTestHandler(store), http.MethodDelete, "/nodes/"+validNodeID, "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDelete_InvalidID(t *testing.T) {
	rec := do(t, newTestHandler(newMockStore()), http.MethodDelete, "/nodes/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDelete_StoreErrors(t *testing.T) {
	t.Run("tenant count fails", func(t *testing.T) {
		store := newMockStore()
		seed(store)
		store.countErr = errStore

		rec := do(t, newTestHandler(store), http.MethodDelete, "/nodes/"+validNodeID, "")

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
		if _, gone := store.nodes[validNodeID]; !gone {
			t.Error("nothing should be deleted when the dependency check failed")
		}
	})

	t.Run("delete fails", func(t *testing.T) {
		store := newMockStore()
		seed(store)
		store.deleteErr = errStore

		rec := do(t, newTestHandler(store), http.MethodDelete, "/nodes/"+validNodeID, "")

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}
