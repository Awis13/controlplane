package project

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
)

const (
	validProjectID = "11111111-1111-1111-1111-111111111111"
)

var errStore = errors.New("database is down")

// --- Mock store ---

type mockStore struct {
	projects map[string]*Project

	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
	countErr  error
	tenants   int

	listLimit  int
	listOffset int
	total      int
}

func newMockStore() *mockStore {
	return &mockStore{projects: make(map[string]*Project)}
}

func (m *mockStore) List(context.Context) ([]Project, error) {
	var out []Project
	for _, p := range m.projects {
		out = append(out, *p)
	}
	return out, m.listErr
}

func (m *mockStore) ListPaginated(_ context.Context, limit, offset int) ([]Project, int, error) {
	m.listLimit, m.listOffset = limit, offset
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	var out []Project
	for _, p := range m.projects {
		out = append(out, *p)
	}
	total := m.total
	if total == 0 {
		total = len(out)
	}
	return out, total, nil
}

func (m *mockStore) GetByID(_ context.Context, id string) (*Project, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.projects[id], nil
}

func (m *mockStore) Create(_ context.Context, req CreateProjectRequest) (*Project, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	p := &Project{
		ID: "new-project-id", Name: req.Name, TemplateID: req.TemplateID,
		RAMMB: req.RAMMB, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.projects[p.ID] = p
	return p, nil
}

func (m *mockStore) Update(_ context.Context, id string, req UpdateProjectRequest) (*Project, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	p, ok := m.projects[id]
	if !ok {
		return nil, nil
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	return p, nil
}

func (m *mockStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.projects, id)
	return nil
}

func (m *mockStore) CountTenants(context.Context, string) (int, error) {
	return m.tenants, m.countErr
}

// --- Helpers ---

func testRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/projects", h.List)
	r.Post("/projects", h.Create)
	r.Get("/projects/{projectID}", h.Get)
	r.Put("/projects/{projectID}", h.Update)
	r.Delete("/projects/{projectID}", h.Delete)
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

func seed(m *mockStore) *Project {
	p := &Project{
		ID: validProjectID, Name: "test-project", TemplateID: 100, RAMMB: 1536,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.projects[p.ID] = p
	return p
}

// --- List ---

func TestList_Empty(t *testing.T) {
	store := newMockStore()
	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// An empty list must serialize as [], not null, so clients can iterate it.
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want an empty items array", rec.Body.String())
	}
}

func TestList_ReturnsProjects(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Items []Project `json:"items"`
		Total int       `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Name != "test-project" {
		t.Errorf("items = %+v", body.Items)
	}
	if body.Total != 1 {
		t.Errorf("total = %d, want 1", body.Total)
	}
}

func TestList_PaginationParamsReachTheStore(t *testing.T) {
	store := newMockStore()
	do(t, NewHandler(store, nil), http.MethodGet, "/projects?limit=5&offset=10", "")

	if store.listLimit != 5 || store.listOffset != 10 {
		t.Errorf("store saw limit=%d offset=%d, want 5 and 10", store.listLimit, store.listOffset)
	}
}

func TestList_HasMore(t *testing.T) {
	store := newMockStore()
	seed(store)
	store.total = 50

	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects", "")

	var body struct {
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.HasMore {
		t.Error("has_more = false, want true when the total exceeds what was returned")
	}
}

func TestList_StoreError(t *testing.T) {
	store := newMockStore()
	store.listErr = errStore

	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects", "")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Get ---

func TestGet_Found(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects/"+validProjectID, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got Project
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != validProjectID {
		t.Errorf("id = %q", got.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	store := newMockStore()

	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects/"+validProjectID, "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGet_InvalidID(t *testing.T) {
	for _, id := range []string{"not-a-uuid", "123", "11111111-1111-1111-1111"} {
		t.Run(id, func(t *testing.T) {
			rec := do(t, NewHandler(newMockStore(), nil), http.MethodGet, "/projects/"+id, "")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestGet_StoreError(t *testing.T) {
	store := newMockStore()
	store.getErr = errStore

	rec := do(t, NewHandler(store, nil), http.MethodGet, "/projects/"+validProjectID, "")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Create ---

func TestCreate_Success(t *testing.T) {
	store := newMockStore()

	rec := do(t, NewHandler(store, nil), http.MethodPost, "/projects",
		`{"name":"radio","template_id":100,"ram_mb":1536}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var got Project
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "radio" || got.TemplateID != 100 {
		t.Errorf("created = %+v", got)
	}
}

func TestCreate_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing name", body: `{"template_id":100}`},
		{name: "empty name", body: `{"name":"","template_id":100}`},
		{name: "missing template_id", body: `{"name":"radio"}`},
		{name: "zero template_id", body: `{"name":"radio","template_id":0}`},
		{name: "negative template_id", body: `{"name":"radio","template_id":-1}`},
		{name: "malformed json", body: `{"name":`},
		{name: "unknown field", body: `{"name":"radio","template_id":100,"nope":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, NewHandler(newMockStore(), nil), http.MethodPost, "/projects", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	store := newMockStore()
	store.createErr = &pgconn.PgError{Code: "23505"}

	rec := do(t, NewHandler(store, nil), http.MethodPost, "/projects",
		`{"name":"radio","template_id":100}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestCreate_StoreError(t *testing.T) {
	store := newMockStore()
	store.createErr = errStore

	rec := do(t, NewHandler(store, nil), http.MethodPost, "/projects",
		`{"name":"radio","template_id":100}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Update ---

func TestUpdate_Success(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, NewHandler(store, nil), http.MethodPut, "/projects/"+validProjectID, `{"name":"renamed"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if store.projects[validProjectID].Name != "renamed" {
		t.Errorf("name = %q, want renamed", store.projects[validProjectID].Name)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	store := newMockStore()

	rec := do(t, NewHandler(store, nil), http.MethodPut, "/projects/"+validProjectID, `{"name":"renamed"}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestUpdate_InvalidID(t *testing.T) {
	rec := do(t, NewHandler(newMockStore(), nil), http.MethodPut, "/projects/not-a-uuid", `{"name":"x"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestUpdate_MalformedBody(t *testing.T) {
	rec := do(t, NewHandler(newMockStore(), nil), http.MethodPut, "/projects/"+validProjectID, `{"name":`)

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
		{name: "duplicate name", err: &pgconn.PgError{Code: "23505"}, wantStatus: http.StatusConflict},
		{name: "database down", err: errStore, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			seed(store)
			store.updateErr = tt.err

			rec := do(t, NewHandler(store, nil), http.MethodPut, "/projects/"+validProjectID, `{"name":"x"}`)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// --- Delete ---

func TestDelete_Success(t *testing.T) {
	store := newMockStore()
	seed(store)

	rec := do(t, NewHandler(store, nil), http.MethodDelete, "/projects/"+validProjectID, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, still := store.projects[validProjectID]; still {
		t.Error("project was not deleted")
	}
}

// TestDelete_RefusedWhileTenantsExist pins the dependency guard: a project
// still backing tenants cannot be removed out from under them.
func TestDelete_RefusedWhileTenantsExist(t *testing.T) {
	store := newMockStore()
	seed(store)
	store.tenants = 3

	rec := do(t, NewHandler(store, nil), http.MethodDelete, "/projects/"+validProjectID, "")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if _, gone := store.projects[validProjectID]; !gone {
		t.Error("the project must survive a refused delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	store := newMockStore()
	store.deleteErr = pgx.ErrNoRows

	rec := do(t, NewHandler(store, nil), http.MethodDelete, "/projects/"+validProjectID, "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDelete_InvalidID(t *testing.T) {
	rec := do(t, NewHandler(newMockStore(), nil), http.MethodDelete, "/projects/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDelete_StoreErrors(t *testing.T) {
	t.Run("tenant count fails", func(t *testing.T) {
		store := newMockStore()
		seed(store)
		store.countErr = errStore

		rec := do(t, NewHandler(store, nil), http.MethodDelete, "/projects/"+validProjectID, "")

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
		if _, gone := store.projects[validProjectID]; !gone {
			t.Error("nothing should be deleted when the dependency check failed")
		}
	})

	t.Run("delete fails", func(t *testing.T) {
		store := newMockStore()
		seed(store)
		store.deleteErr = errStore

		rec := do(t, NewHandler(store, nil), http.MethodDelete, "/projects/"+validProjectID, "")

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

// TestCreate_NameIsNotFormatValidated pins current behaviour, which differs
// from the node handler next door. A node name must match a lowercase
// alphanumeric pattern of 2 to 63 characters; a project name only has to be
// non-empty, so spaces, capitals, path separators and punctuation all pass.
//
// Nothing here is exploitable today: queries are parameterized, and the admin
// templates escape what they render. It is an inconsistency between two
// handlers that create sibling resources, recorded rather than changed because
// tightening it would reject names that existing deployments may already hold.
func TestCreate_NameIsNotFormatValidated(t *testing.T) {
	accepted := []string{
		"Has Spaces And Caps",
		"../../etc/passwd",
		"a",
		"x'; DROP TABLE projects;--",
		"café-münchen",
	}

	for _, name := range accepted {
		t.Run(name, func(t *testing.T) {
			store := newMockStore()
			body, err := json.Marshal(map[string]any{"name": name, "template_id": 100})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			rec := do(t, NewHandler(store, nil), http.MethodPost, "/projects", string(body))

			if rec.Code != http.StatusCreated {
				t.Errorf("status = %d, want 201: this pins that the name is not validated", rec.Code)
			}
		})
	}
}
