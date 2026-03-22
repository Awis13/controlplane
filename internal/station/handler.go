package station

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/billing"
	"controlplane/internal/response"
)

// StationStore defines the data operations for stations.
type StationStore interface {
	ListPublic(ctx context.Context, params ListPublicParams) ([]Station, int, error)
	ListGenres(ctx context.Context) ([]string, error)
	GetBySlug(ctx context.Context, slug string) (*Station, error)
	GetByID(ctx context.Context, id string) (*Station, error)
	Create(ctx context.Context, req CreateStationRequest) (*Station, error)
	Update(ctx context.Context, id string, req UpdateStationRequest) (*Station, error)
	Delete(ctx context.Context, id string) error
	CountByTenantID(ctx context.Context, tenantID string) (int, error)
}

// TenantProvider provides tenant lookup for tier enforcement.
type TenantProvider interface {
	GetTier(ctx context.Context, tenantID string) (string, error)
}

// StatusProvider provides live station status (implemented by Poller).
type StatusProvider interface {
	GetStatus(tenantID string) *StationStatus
}

var slugRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// Handler handles station HTTP requests.
type Handler struct {
	store          StationStore
	auditStore     *audit.Store
	poller         StatusProvider // optional: enriches List/Get with live data
	tenantProvider TenantProvider // optional: tier enforcement on create
}

func NewHandler(store StationStore, auditStore *audit.Store) *Handler {
	return &Handler{store: store, auditStore: auditStore}
}

// WithTenantProvider sets the tenant provider for tier enforcement.
func (h *Handler) WithTenantProvider(tp TenantProvider) {
	h.tenantProvider = tp
}

// WithPoller sets the status provider for live enrichment.
func (h *Handler) WithPoller(p StatusProvider) {
	h.poller = p
}

// validSlug checks that a slug is lowercase alphanumeric with hyphens, 2-63 chars.
func validSlug(s string) bool {
	return len(s) >= 2 && len(s) <= 63 && slugRegexp.MatchString(s)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	lp := response.ParseListParams(r)
	q := r.URL.Query()

	params := ListPublicParams{
		Query:  q.Get("q"),
		Genre:  q.Get("genre"),
		Sort:   q.Get("sort"),
		Limit:  lp.Limit,
		Offset: lp.Offset,
	}

	stations, total, err := h.store.ListPublic(r.Context(), params)
	if err != nil {
		slog.Error("list stations", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list stations")
		return
	}
	if stations == nil {
		stations = []Station{}
	}

	// Enrich with live poller data
	if h.poller != nil {
		for i := range stations {
			if stations[i].TenantID == nil {
				continue
			}
			status := h.poller.GetStatus(*stations[i].TenantID)
			if status != nil {
				stations[i].ListenersCount = status.ListenersCount
				stations[i].NowPlaying = status.NowPlaying
				stations[i].BPM = status.BPM
			}
		}
	}

	// In-memory sort by listeners (requires poller data)
	if params.Sort == "listeners" {
		sort.Slice(stations, func(i, j int) bool {
			return stations[i].ListenersCount > stations[j].ListenersCount
		})
	}

	response.JSON(w, http.StatusOK, response.ListResult[Station]{
		Items:   stations,
		Total:   total,
		Limit:   params.Limit,
		Offset:  params.Offset,
		HasMore: params.Offset+params.Limit < total,
	})
}

func (h *Handler) Genres(w http.ResponseWriter, r *http.Request) {
	genres, err := h.store.ListGenres(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to list genres")
		return
	}
	if genres == nil {
		genres = []string{}
	}
	response.JSON(w, http.StatusOK, genres)
}

func (h *Handler) GetBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if !validSlug(slug) {
		response.Error(w, http.StatusBadRequest, "invalid slug format")
		return
	}

	st, err := h.store.GetBySlug(r.Context(), slug)
	if err != nil {
		slog.Error("get station by slug", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get station")
		return
	}
	if st == nil {
		response.Error(w, http.StatusNotFound, "station not found")
		return
	}

	// Enrich with live poller data
	if h.poller != nil && st.TenantID != nil {
		status := h.poller.GetStatus(*st.TenantID)
		if status != nil {
			st.ListenersCount = status.ListenersCount
			st.NowPlaying = status.NowPlaying
			st.BPM = status.BPM
		}
	}

	response.JSON(w, http.StatusOK, st)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateStationRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Slug == "" {
		response.Error(w, http.StatusBadRequest, "name and slug are required")
		return
	}

	// Validate slug format
	if !validSlug(req.Slug) {
		response.Error(w, http.StatusBadRequest, "invalid slug: must be lowercase alphanumeric with hyphens, 2-63 chars")
		return
	}

	// Validate tenant_id if provided
	if req.TenantID != nil && !response.ValidUUID(*req.TenantID) {
		response.Error(w, http.StatusBadRequest, "invalid tenant_id format")
		return
	}

	// Validate owner_id if provided
	if req.OwnerID != nil && !response.ValidUUID(*req.OwnerID) {
		response.Error(w, http.StatusBadRequest, "invalid owner_id format")
		return
	}

	// Enforce tier station limits.
	// NOTE: между COUNT и INSERT есть теоретическая гонка (TOCTOU), но для
	// single-user tenants вероятность крайне мала. Уникальный constraint на slug
	// (23505) ловится ниже как дополнительная защита.
	if h.tenantProvider != nil && req.TenantID == nil {
		response.Error(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	if req.TenantID != nil && h.tenantProvider != nil {
		tier, err := h.tenantProvider.GetTier(r.Context(), *req.TenantID)
		if err != nil {
			slog.Error("get tenant tier for enforcement", "error", err, "tenant_id", *req.TenantID)
			response.Error(w, http.StatusInternalServerError, "failed to check tier limits")
			return
		}
		limits := billing.GetLimits(tier)
		count, err := h.store.CountByTenantID(r.Context(), *req.TenantID)
		if err != nil {
			slog.Error("count stations for enforcement", "error", err, "tenant_id", *req.TenantID)
			response.Error(w, http.StatusInternalServerError, "failed to check station count")
			return
		}
		if count >= limits.MaxStations {
			response.Error(w, http.StatusForbidden,
				fmt.Sprintf("station limit reached for tier %s (max %d)", tier, limits.MaxStations))
			return
		}
	}

	st, err := h.store.Create(r.Context(), req)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "slug already exists")
			return
		}
		slog.Error("create station", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create station")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "create", "station", st.ID, map[string]string{"name": st.Name, "slug": st.Slug})
	}
	response.JSON(w, http.StatusCreated, st)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stationID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid station ID format")
		return
	}

	var req UpdateStationRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate slug if provided
	if req.Slug != nil && !validSlug(*req.Slug) {
		response.Error(w, http.StatusBadRequest, "invalid slug: must be lowercase alphanumeric with hyphens, 2-63 chars")
		return
	}

	st, err := h.store.Update(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, ErrNoUpdate) {
			response.Error(w, http.StatusBadRequest, "no fields to update")
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "slug already exists")
			return
		}
		slog.Error("update station", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update station")
		return
	}
	if st == nil {
		response.Error(w, http.StatusNotFound, "station not found")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "update", "station", st.ID, nil)
	}
	response.JSON(w, http.StatusOK, st)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stationID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid station ID format")
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, http.StatusNotFound, "station not found")
			return
		}
		slog.Error("delete station", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to delete station")
		return
	}
	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "delete", "station", id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
