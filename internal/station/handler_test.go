package station

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"controlplane/internal/response"
)

// --- Mock station store ---

type mockStationStore struct {
	stations      map[string]*Station
	slugIndex     map[string]*Station
	tenantCounts  map[string]int
	createErr     error
	updateErr     error
	deleteErr     error
}

func newMockStationStore() *mockStationStore {
	return &mockStationStore{
		stations:     make(map[string]*Station),
		slugIndex:    make(map[string]*Station),
		tenantCounts: make(map[string]int),
	}
}

func (m *mockStationStore) addStation(st *Station) {
	m.stations[st.ID] = st
	m.slugIndex[st.Slug] = st
}

func (m *mockStationStore) ListPublic(_ context.Context, _ ListPublicParams) ([]Station, int, error) {
	var result []Station
	for _, st := range m.stations {
		if st.IsPublic {
			result = append(result, *st)
		}
	}
	return result, len(result), nil
}

func (m *mockStationStore) ListGenres(_ context.Context) ([]string, error) {
	genres := map[string]bool{}
	for _, st := range m.stations {
		if st.IsPublic && st.Genre != "" {
			genres[st.Genre] = true
		}
	}
	var result []string
	for g := range genres {
		result = append(result, g)
	}
	return result, nil
}

func (m *mockStationStore) GetBySlug(_ context.Context, slug string) (*Station, error) {
	st, ok := m.slugIndex[slug]
	if !ok {
		return nil, nil
	}
	return st, nil
}

func (m *mockStationStore) GetByID(_ context.Context, id string) (*Station, error) {
	st, ok := m.stations[id]
	if !ok {
		return nil, nil
	}
	return st, nil
}

func (m *mockStationStore) Create(_ context.Context, req CreateStationRequest) (*Station, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	st := &Station{
		ID:          "new-station-id",
		Name:        req.Name,
		Slug:        req.Slug,
		Genre:       req.Genre,
		Description: req.Description,
		ArtworkURL:  req.ArtworkURL,
		OwnerID:     req.OwnerID,
		TenantID:    req.TenantID,
		IsPublic:    req.IsPublic,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	m.addStation(st)
	return st, nil
}

func (m *mockStationStore) Update(_ context.Context, id string, req UpdateStationRequest) (*Station, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	st, ok := m.stations[id]
	if !ok {
		return nil, nil
	}
	if req.Name != nil {
		st.Name = *req.Name
	}
	if req.Slug != nil {
		st.Slug = *req.Slug
	}
	if req.Genre != nil {
		st.Genre = *req.Genre
	}
	return st, nil
}

func (m *mockStationStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.stations[id]; !ok {
		return pgx.ErrNoRows
	}
	delete(m.stations, id)
	return nil
}

func (m *mockStationStore) CountByTenantID(_ context.Context, tenantID string) (int, error) {
	return m.tenantCounts[tenantID], nil
}

// --- Mock tenant provider ---

type mockTenantProvider struct {
	tiers map[string]string
}

func (m *mockTenantProvider) GetTier(_ context.Context, tenantID string) (string, error) {
	tier, ok := m.tiers[tenantID]
	if !ok {
		return "free", nil
	}
	return tier, nil
}

// --- Mock status provider ---

type mockStatusProvider struct {
	statuses map[string]*StationStatus
}

func (m *mockStatusProvider) GetStatus(tenantID string) *StationStatus {
	return m.statuses[tenantID]
}

// --- Test helpers ---

const validStationID = "44444444-4444-4444-4444-444444444444"

func stationRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/stations", h.List)
	r.Get("/stations/{slug}", h.GetBySlug)
	r.Post("/stations", h.Create)
	r.Put("/stations/{stationID}", h.Update)
	r.Delete("/stations/{stationID}", h.Delete)
	return r
}

func sampleStation() *Station {
	return &Station{
		ID:        validStationID,
		Name:      "Test Radio",
		Slug:      "test-radio",
		Genre:     "techno",
		IsPublic:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// --- Tests ---

func TestList_ReturnsEmptyArray(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result response.ListResult[Station]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected empty list, got %d", len(result.Items))
	}
}

func TestList_ReturnsPublicOnly(t *testing.T) {
	store := newMockStationStore()
	store.addStation(&Station{ID: "1", Name: "Public", Slug: "public", IsPublic: true})
	store.addStation(&Station{ID: "2", Name: "Private", Slug: "private", IsPublic: false})
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result response.ListResult[Station]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 1 {
		t.Errorf("expected 1 public station, got %d", len(result.Items))
	}
}

func TestList_EnrichedWithPollerData(t *testing.T) {
	tenantID := "11111111-1111-1111-1111-111111111111"
	store := newMockStationStore()
	store.addStation(&Station{
		ID:       "1",
		Name:     "Live Radio",
		Slug:     "live-radio",
		TenantID: &tenantID,
		IsPublic: true,
	})

	poller := &mockStatusProvider{
		statuses: map[string]*StationStatus{
			tenantID: {
				IsOnline:       true,
				ListenersCount: 99,
				NowPlaying:     "Track A",
				BPM:            140.0,
			},
		},
	}

	h := NewHandler(store, nil)
	h.WithPoller(poller)
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result response.ListResult[Station]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 station, got %d", len(result.Items))
	}
	if result.Items[0].ListenersCount != 99 {
		t.Errorf("listeners_count = %d, want 99", result.Items[0].ListenersCount)
	}
	if result.Items[0].NowPlaying != "Track A" {
		t.Errorf("now_playing = %q, want 'Track A'", result.Items[0].NowPlaying)
	}
	if result.Items[0].BPM != 140.0 {
		t.Errorf("bpm = %f, want 140.0", result.Items[0].BPM)
	}
}

func TestList_NoPollerNoEnrichment(t *testing.T) {
	tenantID := "11111111-1111-1111-1111-111111111111"
	store := newMockStationStore()
	store.addStation(&Station{
		ID:       "1",
		Name:     "Radio",
		Slug:     "radio-st",
		TenantID: &tenantID,
		IsPublic: true,
	})

	h := NewHandler(store, nil) // no poller
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var result response.ListResult[Station]
	json.NewDecoder(w.Body).Decode(&result)
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 station, got %d", len(result.Items))
	}
	if result.Items[0].ListenersCount != 0 {
		t.Errorf("expected 0 listeners without poller, got %d", result.Items[0].ListenersCount)
	}
}

func TestGetBySlug_EnrichedWithPollerData(t *testing.T) {
	tenantID := "22222222-2222-2222-2222-222222222222"
	store := newMockStationStore()
	store.addStation(&Station{
		ID:       validStationID,
		Name:     "Enriched Radio",
		Slug:     "enriched-radio",
		TenantID: &tenantID,
		IsPublic: true,
	})

	poller := &mockStatusProvider{
		statuses: map[string]*StationStatus{
			tenantID: {
				IsOnline:       true,
				ListenersCount: 7,
				NowPlaying:     "Track B",
				BPM:            155.0,
			},
		},
	}

	h := NewHandler(store, nil)
	h.WithPoller(poller)
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations/enriched-radio", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var st Station
	json.NewDecoder(w.Body).Decode(&st)
	if st.ListenersCount != 7 {
		t.Errorf("listeners_count = %d, want 7", st.ListenersCount)
	}
	if st.NowPlaying != "Track B" {
		t.Errorf("now_playing = %q, want 'Track B'", st.NowPlaying)
	}
}

func TestGetBySlug_Found(t *testing.T) {
	store := newMockStationStore()
	store.addStation(sampleStation())
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations/test-radio", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var st Station
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Name != "Test Radio" {
		t.Errorf("name = %q, want Test Radio", st.Name)
	}
}

func TestGetBySlug_NotFound(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("GET", "/stations/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetBySlug_InvalidSlug(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	invalids := []string{"A", "-bad", "a"}
	for _, slug := range invalids {
		req := httptest.NewRequest("GET", "/stations/"+slug, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("slug %q: expected 400, got %d", slug, w.Code)
		}
	}
}

func TestCreate_Returns201(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{
		Name:     "New Radio",
		Slug:     "new-radio",
		Genre:    "house",
		IsPublic: true,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var st Station
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Name != "New Radio" {
		t.Errorf("name = %q", st.Name)
	}
}

func TestCreate_MissingFields(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	body, _ := json.Marshal(map[string]string{"name": "test"})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_InvalidSlug(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{Name: "Test", Slug: "-invalid"})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCreate_InvalidTenantID(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	badID := "not-a-uuid"
	body, _ := json.Marshal(CreateStationRequest{
		Name:     "Test",
		Slug:     "test-radio",
		TenantID: &badID,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCreate_InvalidOwnerID(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	badID := "not-a-uuid"
	body, _ := json.Marshal(CreateStationRequest{
		Name:    "Test",
		Slug:    "test-radio",
		OwnerID: &badID,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUpdate_Success(t *testing.T) {
	store := newMockStationStore()
	store.addStation(sampleStation())
	h := NewHandler(store, nil)
	r := stationRouter(h)

	newName := "Updated Radio"
	body, _ := json.Marshal(UpdateStationRequest{Name: &newName})
	req := httptest.NewRequest("PUT", "/stations/"+validStationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdate_NotFound(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	newName := "Updated"
	body, _ := json.Marshal(UpdateStationRequest{Name: &newName})
	req := httptest.NewRequest("PUT", "/stations/"+validStationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestUpdate_InvalidID(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	newName := "Updated"
	body, _ := json.Marshal(UpdateStationRequest{Name: &newName})
	req := httptest.NewRequest("PUT", "/stations/not-a-uuid", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUpdate_InvalidSlug(t *testing.T) {
	store := newMockStationStore()
	store.addStation(sampleStation())
	h := NewHandler(store, nil)
	r := stationRouter(h)

	badSlug := "-invalid"
	body, _ := json.Marshal(UpdateStationRequest{Slug: &badSlug})
	req := httptest.NewRequest("PUT", "/stations/"+validStationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdate_NoFields(t *testing.T) {
	store := newMockStationStore()
	store.addStation(sampleStation())
	store.updateErr = ErrNoUpdate
	h := NewHandler(store, nil)
	r := stationRouter(h)

	body, _ := json.Marshal(UpdateStationRequest{})
	req := httptest.NewRequest("PUT", "/stations/"+validStationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestDelete_Success(t *testing.T) {
	store := newMockStationStore()
	store.addStation(sampleStation())
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("DELETE", "/stations/"+validStationID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDelete_NotFound(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("DELETE", "/stations/"+validStationID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDelete_InvalidID(t *testing.T) {
	store := newMockStationStore()
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("DELETE", "/stations/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestDelete_StoreError(t *testing.T) {
	store := newMockStationStore()
	store.addStation(sampleStation())
	store.deleteErr = fmt.Errorf("database error")
	h := NewHandler(store, nil)
	r := stationRouter(h)

	req := httptest.NewRequest("DELETE", "/stations/"+validStationID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// --- Tier Enforcement Tests ---

func TestCreate_UnderTierLimit(t *testing.T) {
	tenantID := "11111111-1111-1111-1111-111111111111"
	store := newMockStationStore()
	store.tenantCounts[tenantID] = 0 // no stations yet

	tp := &mockTenantProvider{tiers: map[string]string{tenantID: "free"}}

	h := NewHandler(store, nil)
	h.WithTenantProvider(tp)
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{
		Name:     "New Radio",
		Slug:     "new-radio",
		Genre:    "house",
		TenantID: &tenantID,
		IsPublic: true,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_AtTierLimit(t *testing.T) {
	tenantID := "11111111-1111-1111-1111-111111111111"
	store := newMockStationStore()
	store.tenantCounts[tenantID] = 1 // already at free tier limit (max 1)

	tp := &mockTenantProvider{tiers: map[string]string{tenantID: "free"}}

	h := NewHandler(store, nil)
	h.WithTenantProvider(tp)
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{
		Name:     "Second Radio",
		Slug:     "second-radio",
		Genre:    "house",
		TenantID: &tenantID,
		IsPublic: true,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_AfterTierUpgrade(t *testing.T) {
	tenantID := "11111111-1111-1111-1111-111111111111"
	store := newMockStationStore()
	store.tenantCounts[tenantID] = 1 // 1 station, free limit is 1, but studio allows 3

	tp := &mockTenantProvider{tiers: map[string]string{tenantID: "studio"}}

	h := NewHandler(store, nil)
	h.WithTenantProvider(tp)
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{
		Name:     "Second Radio",
		Slug:     "second-radio",
		Genre:    "house",
		TenantID: &tenantID,
		IsPublic: true,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_NoTenantID_ReturnsBadRequest(t *testing.T) {
	store := newMockStationStore()
	tp := &mockTenantProvider{tiers: map[string]string{}}

	h := NewHandler(store, nil)
	h.WithTenantProvider(tp)
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{
		Name:     "Free Radio",
		Slug:     "free-radio",
		Genre:    "house",
		IsPublic: true,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_NoTenantID_NoProviderAllowed(t *testing.T) {
	// Когда tenantProvider не настроен, создание без tenant_id допустимо
	store := newMockStationStore()
	h := NewHandler(store, nil) // без tenantProvider
	r := stationRouter(h)

	body, _ := json.Marshal(CreateStationRequest{
		Name:     "Free Radio",
		Slug:     "free-radio",
		Genre:    "house",
		IsPublic: true,
	})
	req := httptest.NewRequest("POST", "/stations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidSlug(t *testing.T) {
	tests := []struct {
		slug string
		want bool
	}{
		{"test-radio", true},
		{"ab", true},
		{"a1", true},
		{"my-cool-station-123", true},
		{"a", false},          // too short
		{"-bad", false},       // starts with hyphen
		{"bad-", false},       // ends with hyphen
		{"Bad", false},        // uppercase
		{"has space", false},  // space
		{"", false},           // empty
	}

	for _, tt := range tests {
		got := validSlug(tt.slug)
		if got != tt.want {
			t.Errorf("validSlug(%q) = %v, want %v", tt.slug, got, tt.want)
		}
	}
}
