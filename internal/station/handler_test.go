package station

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"controlplane/internal/response"
)

// --- Mock station store ---

type mockStationStore struct {
	stations     map[string]*Station
	slugIndex    map[string]*Station
	tenantCounts map[string]int
	createErr    error
	updateErr    error
	deleteErr    error

	// lastListParams records what the handler asked the store for, so tests can
	// pin which window the handler requested and not just what came back.
	lastListParams ListPublicParams
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

// ListPublic mirrors the real store closely enough for pagination to mean
// something: it orders by name, applies Limit/Offset to the returned page, and
// reports the total before paging.
func (m *mockStationStore) ListPublic(_ context.Context, p ListPublicParams) ([]Station, int, error) {
	m.lastListParams = p

	var result []Station
	for _, st := range m.stations {
		if st.IsPublic {
			result = append(result, *st)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })

	total := len(result)
	if p.Offset >= len(result) {
		return nil, total, nil
	}
	end := p.Offset + p.Limit
	if p.Limit <= 0 || end > len(result) {
		end = len(result)
	}
	return result[p.Offset:end], total, nil
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

// --- sort=listeners ---

// listenerFixture builds public stations whose name order and listener order
// disagree, so a page taken before the sort is visibly not the top N.
// By name: A, B, C, D, E. By listeners: D(9), E(7), B(5), C(3), A(1).
func listenerFixture() (*mockStationStore, *mockStatusProvider) {
	counts := map[string]int{"A": 1, "B": 5, "C": 3, "D": 9, "E": 7}

	store := newMockStationStore()
	poller := &mockStatusProvider{statuses: map[string]*StationStatus{}}
	for _, name := range []string{"A", "B", "C", "D", "E"} {
		tenantID := "tenant-" + name
		store.addStation(&Station{
			ID:       "id-" + name,
			Name:     name,
			Slug:     "slug-" + name,
			TenantID: &tenantID,
			IsPublic: true,
		})
		poller.statuses[tenantID] = &StationStatus{ListenersCount: counts[name]}
	}
	return store, poller
}

func listNames(t *testing.T, h *Handler, target string) response.ListResult[Station] {
	t.Helper()
	r := stationRouter(h)
	req := httptest.NewRequest("GET", target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", target, w.Code, w.Body.String())
	}
	var result response.ListResult[Station]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

func namesOf(items []Station) []string {
	names := make([]string, len(items))
	for i, st := range items {
		names[i] = st.Name
	}
	return names
}

func assertNames(t *testing.T, got []Station, want ...string) {
	t.Helper()
	names := namesOf(got)
	if len(names) != len(want) {
		t.Fatalf("items = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("items = %v, want %v", names, want)
		}
	}
}

// TestList_SortByListeners_PageOneIsTopN is the regression: page 1 used to be
// the first page by name re-sorted within itself, so the busiest stations never
// surfaced. Here A and B are the first page by name but D and E are the top two.
func TestList_SortByListeners_PageOneIsTopN(t *testing.T) {
	store, poller := listenerFixture()
	h := NewHandler(store, nil)
	h.WithPoller(poller)

	result := listNames(t, h, "/stations?sort=listeners&limit=2&offset=0")

	assertNames(t, result.Items, "D", "E")
	if result.Total != 5 {
		t.Errorf("total = %d, want 5", result.Total)
	}
	if !result.HasMore {
		t.Error("has_more = false, want true")
	}
}

func TestList_SortByListeners_LaterPagesContinueTheRanking(t *testing.T) {
	store, poller := listenerFixture()
	h := NewHandler(store, nil)
	h.WithPoller(poller)

	assertNames(t, listNames(t, h, "/stations?sort=listeners&limit=2&offset=2").Items, "B", "C")
	assertNames(t, listNames(t, h, "/stations?sort=listeners&limit=2&offset=4").Items, "A")
}

func TestList_SortByListeners_OffsetPastEndReturnsEmptyArray(t *testing.T) {
	store, poller := listenerFixture()
	h := NewHandler(store, nil)
	h.WithPoller(poller)

	result := listNames(t, h, "/stations?sort=listeners&limit=2&offset=99")

	if len(result.Items) != 0 {
		t.Errorf("items = %v, want empty", namesOf(result.Items))
	}
	if result.Total != 5 {
		t.Errorf("total = %d, want 5", result.Total)
	}
}

// TestList_SortByListeners_ScansFromTheStart pins the mechanism: the handler
// must ask the store for the whole window from offset 0, not the caller's page.
func TestList_SortByListeners_ScansFromTheStart(t *testing.T) {
	store, poller := listenerFixture()
	h := NewHandler(store, nil)
	h.WithPoller(poller)

	listNames(t, h, "/stations?sort=listeners&limit=2&offset=2")

	if store.lastListParams.Offset != 0 {
		t.Errorf("store offset = %d, want 0", store.lastListParams.Offset)
	}
	if store.lastListParams.Limit != listenersScanLimit {
		t.Errorf("store limit = %d, want %d", store.lastListParams.Limit, listenersScanLimit)
	}
}

// TestList_OtherSortsPageInTheStore pins that only sort=listeners changed: every
// other sort still hands the caller's window straight to the store.
func TestList_OtherSortsPageInTheStore(t *testing.T) {
	for _, sortKey := range []string{"", "name", "newest", "online_first"} {
		store, poller := listenerFixture()
		h := NewHandler(store, nil)
		h.WithPoller(poller)

		listNames(t, h, "/stations?sort="+sortKey+"&limit=2&offset=1")

		if store.lastListParams.Offset != 1 || store.lastListParams.Limit != 2 {
			t.Errorf("sort=%q: store got limit=%d offset=%d, want limit=2 offset=1",
				sortKey, store.lastListParams.Limit, store.lastListParams.Offset)
		}
	}
}

// TestList_SortByListeners_MissingStatsSortLast answers where stations the
// poller knows nothing about land: they keep the zero count and sit at the
// bottom, and equal counts hold the store's name order.
func TestList_SortByListeners_MissingStatsSortLast(t *testing.T) {
	store, poller := listenerFixture()
	// Two stations the poller has no entry for, plus one with no tenant at all.
	for _, name := range []string{"X", "Y"} {
		tenantID := "tenant-unpolled-" + name
		store.addStation(&Station{ID: "id-" + name, Name: name, Slug: "slug-" + name, TenantID: &tenantID, IsPublic: true})
	}
	store.addStation(&Station{ID: "id-Z", Name: "Z", Slug: "slug-z", IsPublic: true})

	h := NewHandler(store, nil)
	h.WithPoller(poller)

	result := listNames(t, h, "/stations?sort=listeners&limit=10&offset=0")

	assertNames(t, result.Items, "D", "E", "B", "C", "A", "X", "Y", "Z")
}

// TestList_SortByListeners_NoPollerKeepsStoreOrder covers the degenerate case:
// with no poller every count is zero, so the sort must leave the store's name
// order untouched rather than shuffling it. The fixture is deliberately larger
// than Go's insertion-sort threshold (12) — below it an unstable sort happens to
// preserve order anyway and the assertion would prove nothing.
func TestList_SortByListeners_NoPollerKeepsStoreOrder(t *testing.T) {
	const n = 30
	store := newMockStationStore()
	want := make([]string, n)
	for i := range n {
		name := fmt.Sprintf("station-%02d", i)
		want[i] = name
		store.addStation(&Station{ID: name, Name: name, Slug: name, IsPublic: true})
	}

	h := NewHandler(store, nil) // no poller, so every count is zero

	result := listNames(t, h, "/stations?sort=listeners&limit=50&offset=0")

	assertNames(t, result.Items, want...)
}

// TestList_SortByListeners_TiesKeepStoreOrder pins that equal counts hold the
// store's name order instead of being shuffled, which is what makes paging
// through a tied ranking repeatable rather than dropping and duplicating rows
// between pages. The fixture is two large interleaved tie groups: a single
// uniform group is left untouched even by an unstable sort, so it would prove
// nothing.
func TestList_SortByListeners_TiesKeepStoreOrder(t *testing.T) {
	const n = 30
	store := newMockStationStore()
	poller := &mockStatusProvider{statuses: map[string]*StationStatus{}}
	var busy, quiet []string
	for i := range n {
		name := fmt.Sprintf("station-%02d", i)
		tenantID := "tenant-" + name
		store.addStation(&Station{ID: name, Name: name, Slug: name, TenantID: &tenantID, IsPublic: true})
		poller.statuses[tenantID] = &StationStatus{ListenersCount: i % 2}
		if i%2 == 1 {
			busy = append(busy, name)
		} else {
			quiet = append(quiet, name)
		}
	}

	h := NewHandler(store, nil)
	h.WithPoller(poller)

	result := listNames(t, h, "/stations?sort=listeners&limit=50&offset=0")

	assertNames(t, result.Items, append(busy, quiet...)...)
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
	// When tenantProvider is not configured, creation without tenant_id is allowed
	store := newMockStationStore()
	h := NewHandler(store, nil) // no tenantProvider
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
		{"a", false},         // too short
		{"-bad", false},      // starts with hyphen
		{"bad-", false},      // ends with hyphen
		{"Bad", false},       // uppercase
		{"has space", false}, // space
		{"", false},          // empty
	}

	for _, tt := range tests {
		got := validSlug(tt.slug)
		if got != tt.want {
			t.Errorf("validSlug(%q) = %v, want %v", tt.slug, got, tt.want)
		}
	}
}
