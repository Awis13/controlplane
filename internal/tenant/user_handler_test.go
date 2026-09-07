package tenant

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"controlplane/internal/auth"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/sso"
	"controlplane/internal/user"
)

// --- Mock stores for user handler ---

type mockUserTenantStore struct {
	mockTenantStore
	ownerTenants map[string][]Tenant
	ownerCounts  map[string]int
	createErr    error
}

func newMockUserTenantStore() *mockUserTenantStore {
	return &mockUserTenantStore{
		mockTenantStore: mockTenantStore{tenants: make(map[string]*Tenant)},
		ownerTenants:    make(map[string][]Tenant),
		ownerCounts:     make(map[string]int),
	}
}

func (m *mockUserTenantStore) CreateWithOwner(_ context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	t := &Tenant{
		ID:           "user-tenant-id",
		Name:         req.Name,
		ProjectID:    req.ProjectID,
		NodeID:       req.NodeID,
		Subdomain:    req.Subdomain,
		Status:       "provisioning",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	m.tenants[t.ID] = t
	m.ownerTenants[ownerID] = append(m.ownerTenants[ownerID], *t)
	return t, nil
}

// CreateWithOwnerWithReservation is the owner-scoped reservation path the
// lifecycle drives. It records the owner and the reservation together.
func (m *mockUserTenantStore) CreateWithOwnerWithReservation(_ context.Context, req CreateTenantRequest, ownerID string, ramMB int) (*Tenant, error) {
	if m.createWithReservationErr != nil {
		return nil, m.createWithReservationErr
	}
	if m.createErr != nil {
		return nil, m.createErr
	}
	t := &Tenant{
		ID:            "user-tenant-id",
		Name:          req.Name,
		ProjectID:     req.ProjectID,
		NodeID:        req.NodeID,
		Subdomain:     req.Subdomain,
		Status:        "provisioning",
		OwnerID:       &ownerID,
		HealthStatus:  "unknown",
		ReservedRAMMB: &ramMB,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	m.tenants[t.ID] = t
	m.ownerTenants[ownerID] = append(m.ownerTenants[ownerID], *t)
	return t, nil
}

func (m *mockUserTenantStore) ListByOwnerID(_ context.Context, ownerID string) ([]Tenant, error) {
	return m.ownerTenants[ownerID], nil
}

func (m *mockUserTenantStore) CountByOwnerID(_ context.Context, ownerID string) (int, error) {
	if count, ok := m.ownerCounts[ownerID]; ok {
		return count, nil
	}
	return len(m.ownerTenants[ownerID]), nil
}

type mockUserNodeStore struct {
	mockNodeStore
	leastLoaded *node.Node
}

func newMockUserNodeStore() *mockUserNodeStore {
	return &mockUserNodeStore{
		mockNodeStore: mockNodeStore{nodes: make(map[string]*node.Node)},
	}
}

func (m *mockUserNodeStore) GetLeastLoaded(_ context.Context, _ int) (*node.Node, error) {
	return m.leastLoaded, nil
}

type mockUserProjectStore struct {
	mockProjectStore
	defaultProject *project.Project
}

func newMockUserProjectStore() *mockUserProjectStore {
	return &mockUserProjectStore{
		mockProjectStore: mockProjectStore{projects: make(map[string]*project.Project)},
	}
}

func (m *mockUserProjectStore) GetDefault(_ context.Context) (*project.Project, error) {
	return m.defaultProject, nil
}

// --- Test helpers ---

var testUserID = uuid.MustParse("55555555-5555-5555-5555-555555555555")

func testUser() *user.User {
	return &user.User{
		ID:          testUserID,
		Email:       "test@example.com",
		DisplayName: "Test User",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func userTenantRouter(h *UserHandler) *chi.Mux {
	r := chi.NewRouter()
	// Simulate JWT middleware by injecting user into context
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := testUser()
			ctx := auth.SetUserForTest(r.Context(), u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Get("/tenants", h.List)
	r.Post("/tenants", h.Create)
	r.Get("/tenants/{tenantID}", h.Get)
	r.Delete("/tenants/{tenantID}", h.Delete)
	r.Post("/tenants/{tenantID}/suspend", h.Suspend)
	r.Post("/tenants/{tenantID}/resume", h.Resume)
	r.Post("/tenants/{tenantID}/sso-token", h.SSOToken)
	return r
}

func userTenantRouterNoAuth(h *UserHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/tenants", h.List)
	r.Post("/tenants", h.Create)
	r.Get("/tenants/{tenantID}", h.Get)
	r.Delete("/tenants/{tenantID}", h.Delete)
	r.Post("/tenants/{tenantID}/suspend", h.Suspend)
	r.Post("/tenants/{tenantID}/resume", h.Resume)
	r.Post("/tenants/{tenantID}/sso-token", h.SSOToken)
	return r
}

func newTestUserHandler() (*UserHandler, *mockUserTenantStore, *mockUserNodeStore, *mockUserProjectStore, *mockProvisioner) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()
	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	return h, ts, ns, ps, prov
}

// newTestSigner returns an SSO signer backed by a freshly generated key.
func newTestSigner() *sso.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := sso.NewSigner(hex.EncodeToString(priv))
	if err != nil {
		panic(err)
	}
	return signer
}

// newTestUserHandlerWithSigner is newTestUserHandler with an SSO signer wired
// in, so SSOToken tests can exercise the signed path.
func newTestUserHandlerWithSigner() (*UserHandler, *mockUserTenantStore, *mockUserNodeStore, *mockUserProjectStore, *mockProvisioner, *sso.Signer) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()
	signer := newTestSigner()
	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", signer)
	return h, ts, ns, ps, prov, signer
}

// --- Tests ---

func TestUserList_Empty(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	req := httptest.NewRequest("GET", "/tenants", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		Items []Tenant `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(result.Items))
	}
}

func TestUserList_ReturnOwnTenants(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ownerID := testUserID.String()
	ts.ownerTenants[ownerID] = []Tenant{
		{ID: "t1", Name: "My Radio", OwnerID: &ownerID, Status: "active"},
		{ID: "t2", Name: "My Other Radio", OwnerID: &ownerID, Status: "provisioning"},
	}

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	req := httptest.NewRequest("GET", "/tenants", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result struct {
		Items []Tenant `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Items))
	}
}

func TestUserList_Unauthenticated(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouterNoAuth(h)

	req := httptest.NewRequest("GET", "/tenants", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestUserCreate_Success(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ns.leastLoaded = activeNode()
	ps.defaultProject = testProjectObj()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp Tenant
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "provisioning" {
		t.Errorf("status = %q, want provisioning", resp.Status)
	}
	if resp.OwnerID == nil || *resp.OwnerID != testUserID.String() {
		t.Errorf("owner_id = %v, want %s", resp.OwnerID, testUserID)
	}

	<-prov.provisionDone
	if !prov.wasProvisionCalled() {
		t.Error("expected provisioner.Provision to be called")
	}
}

func TestUserCreate_MissingName(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUserCreate_MissingSubdomain(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUserCreate_InvalidSubdomain(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	invalids := []string{"-bad", "bad-", "A", "a", "has space"}
	for _, sub := range invalids {
		body, _ := json.Marshal(UserCreateRequest{Name: "Test", Subdomain: sub})
		req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("subdomain %q: expected 400, got %d", sub, w.Code)
		}
	}
}

func TestUserCreate_ReservedSubdomain(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "Admin Radio", Subdomain: "admin"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUserCreate_NoProject(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ps.defaultProject = nil // no projects configured

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_NoAvailableNode(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ps.defaultProject = testProjectObj()
	ns.leastLoaded = nil // no nodes with capacity

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_InsufficientCapacity(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	ps.defaultProject = testProjectObj()
	ns.leastLoaded = activeNode()
	ts.createWithReservationErr = node.ErrInsufficientCapacity

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_Unauthenticated(t *testing.T) {
	ts := newMockUserTenantStore()
	ns := newMockUserNodeStore()
	ps := newMockUserProjectStore()
	prov := newMockProvisioner()

	h := NewUserHandler(ts, ns, ps, prov, nil, "example.com", "", nil)
	r := userTenantRouterNoAuth(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "My Radio", Subdomain: "my-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// --- SSO Token Tests ---

func TestUserSSOToken_Success(t *testing.T) {
	h, ts, _, _, _, signer := newTestUserHandlerWithSigner()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/sso-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	ssoURL, ok := resp["url"].(string)
	if !ok || ssoURL == "" {
		t.Fatal("expected non-empty url in response")
	}
	if !contains(ssoURL, "my-radio.example.com") {
		t.Errorf("url should contain subdomain.domain, got: %s", ssoURL)
	}
	if !contains(ssoURL, "/auth/sso?token=") {
		t.Errorf("url should contain /auth/sso?token=, got: %s", ssoURL)
	}

	expiresIn, ok := resp["expires_in"].(float64)
	if !ok || expiresIn != 60 {
		t.Errorf("expected expires_in=60, got %v", resp["expires_in"])
	}

	// The token must be a valid signed assertion: parse it, check the payload
	// carries the right tier and tenant, and verify the signature with the
	// signer's public key.
	token := tokenFromURL(t, ssoURL)
	payload, sig := decodeToken(t, token)
	if !ed25519.Verify(signer.PublicKey(), payload, sig) {
		t.Error("token signature did not verify with the signer's public key")
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 8 {
		t.Fatalf("payload = %q, want 8 pipe-separated fields", payload)
	}
	if fields[0] != "v1" {
		t.Errorf("payload version = %q, want v1", fields[0])
	}
	if fields[1] != testUserID.String() {
		t.Errorf("payload userID = %q, want %s", fields[1], testUserID)
	}
	if fields[2] != validTenantID {
		t.Errorf("payload tenantID = %q, want %s", fields[2], validTenantID)
	}
	if fields[3] != "free" {
		t.Errorf("payload tier = %q, want free", fields[3])
	}
	if fields[6] != "controlplane" {
		t.Errorf("payload issuer = %q, want controlplane", fields[6])
	}
	if fields[7] != validTenantID {
		t.Errorf("payload audience = %q, want %s", fields[7], validTenantID)
	}
}

func TestUserSSOToken_NoSigner(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/sso-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserSSOToken_NonOwner(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	otherOwner := "99999999-9999-9999-9999-999999999999"
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "other-radio",
		Status:       "active",
		OwnerID:      &otherOwner,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/sso-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserSSOToken_InactiveTenant(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "suspended",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/sso-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserSSOToken_Unauthenticated(t *testing.T) {
	h, _, _, _, _ := newTestUserHandler()
	r := userTenantRouterNoAuth(h)

	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/sso-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// tokenFromURL extracts the token query parameter from an SSO URL.
func tokenFromURL(t *testing.T, ssoURL string) string {
	t.Helper()
	u, err := url.Parse(ssoURL)
	if err != nil {
		t.Fatalf("parse sso url: %v", err)
	}
	token := u.Query().Get("token")
	if token == "" {
		t.Fatalf("no token in url: %s", ssoURL)
	}
	return token
}

// decodeToken splits a base64url(payload):base64url(sig) token and decodes both.
func decodeToken(t *testing.T, token string) (payload, sig []byte) {
	t.Helper()
	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		t.Fatalf("token = %q, want one ':' separator", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	return payload, sig
}

// --- Delete Tests ---

func TestUserDelete_Success(t *testing.T) {
	h, ts, _, ps, _ := newTestUserHandler()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		LXCID:        nil,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	ps.projects[validProjectID] = testProjectObj()

	r := userTenantRouter(h)
	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if ts.tenants[validTenantID].Status != "deleted" {
		t.Errorf("expected status 'deleted', got %q", ts.tenants[validTenantID].Status)
	}
}

func TestUserDelete_NonOwner(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	otherOwner := "99999999-9999-9999-9999-999999999999"
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &otherOwner,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserDelete_AlreadyDeleting(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "deleting",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserDelete_Unauthenticated(t *testing.T) {
	h, _, _, _, _ := newTestUserHandler()
	r := userTenantRouterNoAuth(h)

	req := httptest.NewRequest("DELETE", "/tenants/"+validTenantID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Suspend Tests ---

func TestUserSuspend_Success(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	lxcID := 105
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		LXCID:        &lxcID,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/suspend", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if ts.tenants[validTenantID].Status != "suspended" {
		t.Errorf("expected status 'suspended', got %q", ts.tenants[validTenantID].Status)
	}
}

func TestUserSuspend_NonOwner(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	otherOwner := "99999999-9999-9999-9999-999999999999"
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &otherOwner,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/suspend", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserSuspend_NotActive(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "suspended",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/suspend", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Resume Tests ---

func TestUserResume_Success(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	lxcID := 105
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		LXCID:        &lxcID,
		Subdomain:    "my-radio",
		Status:       "suspended",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/resume", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if ts.tenants[validTenantID].Status != "active" {
		t.Errorf("expected status 'active', got %q", ts.tenants[validTenantID].Status)
	}
}

func TestUserResume_NonOwner(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	otherOwner := "99999999-9999-9999-9999-999999999999"
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "suspended",
		OwnerID:      &otherOwner,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/resume", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserResume_NotSuspended(t *testing.T) {
	h, ts, _, _, _ := newTestUserHandler()

	ownerID := testUserID.String()
	ts.tenants[validTenantID] = &Tenant{
		ID:           validTenantID,
		Name:         "test",
		ProjectID:    validProjectID,
		NodeID:       validNodeID,
		Subdomain:    "my-radio",
		Status:       "active",
		OwnerID:      &ownerID,
		HealthStatus: "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	r := userTenantRouter(h)
	req := httptest.NewRequest("POST", "/tenants/"+validTenantID+"/resume", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Tenant Creation Limit Tests ---

func TestUserCreate_FirstTenantSucceeds(t *testing.T) {
	h, ts, ns, ps, prov := newTestUserHandler()
	_ = prov

	ns.leastLoaded = activeNode()
	ps.defaultProject = testProjectObj()
	// ownerCounts defaults to 0 (no tenants yet)

	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "First Radio", Subdomain: "first-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	_ = ts
}

func TestUserCreate_SecondTenantFreeTierBlocked(t *testing.T) {
	h, ts, ns, ps, _ := newTestUserHandler()

	ns.leastLoaded = activeNode()
	ps.defaultProject = testProjectObj()

	ownerID := testUserID.String()
	// User already has 1 tenant on free tier
	ts.ownerCounts[ownerID] = 1
	ts.ownerTenants[ownerID] = []Tenant{
		{ID: "t1", Name: "My Radio", OwnerID: &ownerID, Tier: "free", Status: "active"},
	}

	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "Second Radio", Subdomain: "second-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserCreate_SecondTenantStudioTierAllowed(t *testing.T) {
	h, ts, ns, ps, prov := newTestUserHandler()

	ns.leastLoaded = activeNode()
	ps.defaultProject = testProjectObj()

	ownerID := testUserID.String()
	// User has 1 tenant on studio tier (MaxTenants: 3)
	ts.ownerCounts[ownerID] = 1
	ts.ownerTenants[ownerID] = []Tenant{
		{ID: "t1", Name: "Studio Radio", OwnerID: &ownerID, Tier: "studio", Status: "active"},
	}

	r := userTenantRouter(h)

	body, _ := json.Marshal(UserCreateRequest{Name: "Second Radio", Subdomain: "second-radio"})
	req := httptest.NewRequest("POST", "/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	<-prov.provisionDone
}

// contains is a simple string containment check for test assertions.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
