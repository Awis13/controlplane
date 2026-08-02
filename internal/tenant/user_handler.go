package tenant

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/audit"
	"controlplane/internal/auth"
	"controlplane/internal/billing"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
)

// UserNodeStore extends NodeStore with auto-selection capabilities.
type UserNodeStore interface {
	NodeStore
	GetLeastLoaded(ctx context.Context, requiredMB int) (*node.Node, error)
}

// UserTenantStore extends TenantStore with owner-scoped operations.
type UserTenantStore interface {
	TenantStore
	CreateWithOwner(ctx context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error)
	ListByOwnerID(ctx context.Context, ownerID string) ([]Tenant, error)
	CountByOwnerID(ctx context.Context, ownerID string) (int, error)
}

// UserProjectStore extends ProjectStore with default selection.
type UserProjectStore interface {
	ProjectStore
	GetDefault(ctx context.Context) (*project.Project, error)
}

// UserHandler handles tenant requests from authenticated users.
type UserHandler struct {
	store        UserTenantStore
	nodeStore    UserNodeStore
	projectStore UserProjectStore
	provisioner  Provisioner
	auditStore   *audit.Store
	lifecycle    *LifecycleService
	ssoDomain    string
	ssoScheme    string
}

// respondLifecycle turns a lifecycle failure into an HTTP response, logging the
// underlying cause for anything the client is not shown.
func (h *UserHandler) respondLifecycle(w http.ResponseWriter, lerr *LifecycleError) {
	response.Error(w, lifecycleStatus(lerr), lerr.Message)
}

// lifecycleStatus maps a failure kind to the status code every entry point uses.
func lifecycleStatus(lerr *LifecycleError) int {
	switch lerr.Kind {
	case FailureInvalid:
		return http.StatusBadRequest
	case FailureConflict:
		return http.StatusConflict
	default:
		slog.Error("tenant lifecycle", "error", lerr)
		return http.StatusInternalServerError
	}
}

// UserCreateRequest is the simplified tenant creation request for users.
// Project and node are auto-selected.
type UserCreateRequest struct {
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
}

func NewUserHandler(store UserTenantStore, nodeStore UserNodeStore, projectStore UserProjectStore, provisioner Provisioner, auditStore *audit.Store, ssoDomain, ssoScheme string) *UserHandler {
	if ssoDomain == "" {
		ssoDomain = "example.com"
	}
	if ssoScheme == "" {
		ssoScheme = "https"
	}
	return &UserHandler{
		store:        store,
		nodeStore:    nodeStore,
		projectStore: projectStore,
		provisioner:  provisioner,
		auditStore:   auditStore,
		lifecycle:    NewLifecycleService(store, nodeStore, projectStore, provisioner, auditStore),
		ssoDomain:    ssoDomain,
		ssoScheme:    ssoScheme,
	}
}

// List returns only the authenticated user's tenants.
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	tenants, err := h.store.ListByOwnerID(r.Context(), u.ID.String())
	if err != nil {
		slog.Error("list user tenants", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	response.JSON(w, http.StatusOK, response.ListResult[Tenant]{
		Items:   tenants,
		Total:   len(tenants),
		Limit:   len(tenants),
		Offset:  0,
		HasMore: false,
	})
}

// Get returns a single tenant owned by the authenticated user.
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get user tenant", "error", err, "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil || t.OwnerID == nil || *t.OwnerID != u.ID.String() {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	response.JSON(w, http.StatusOK, t)
}

// Create creates a tenant for the authenticated user with auto-selected project and node.
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req UserCreateRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Subdomain == "" {
		response.Error(w, http.StatusBadRequest, "name and subdomain are required")
		return
	}

	// Validate subdomain
	if lerr := ValidateSubdomain(req.Subdomain); lerr != nil {
		h.respondLifecycle(w, lerr)
		return
	}

	// Enforce tenant creation limits based on tier.
	// NOTE: there is a theoretical race (TOCTOU) between COUNT and INSERT, but for
	// single-user operations the probability is extremely low. The unique constraint on subdomain
	// (23505) is caught below as an additional safeguard.
	currentCount, err := h.store.CountByOwnerID(r.Context(), u.ID.String())
	if err != nil {
		slog.Error("count tenants for enforcement", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to check tenant limits")
		return
	}
	// Determine the max tenant count from the highest tier among existing tenants (default: free).
	maxTenants := billing.GetLimits(billing.TierFree).MaxTenants
	existingTenants, err := h.store.ListByOwnerID(r.Context(), u.ID.String())
	if err != nil {
		slog.Error("list tenants for tier check", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to check tenant limits")
		return
	}
	for _, t := range existingTenants {
		limits := billing.GetLimits(t.Tier)
		if limits.MaxTenants > maxTenants {
			maxTenants = limits.MaxTenants
		}
	}
	if currentCount >= maxTenants {
		response.Error(w, http.StatusForbidden,
			fmt.Sprintf("tenant limit reached (max %d)", maxTenants))
		return
	}

	// Auto-select default project
	proj, err := h.projectStore.GetDefault(r.Context())
	if err != nil {
		slog.Error("get default project", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to select project")
		return
	}
	if proj == nil {
		response.Error(w, http.StatusServiceUnavailable, "no projects configured")
		return
	}

	// Auto-select least loaded node with enough RAM
	n, err := h.nodeStore.GetLeastLoaded(r.Context(), proj.RAMMB)
	if err != nil {
		slog.Error("get least loaded node", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to select node")
		return
	}
	if n == nil {
		response.Error(w, http.StatusServiceUnavailable, "no nodes with sufficient capacity")
		return
	}

	t, lerr := h.lifecycle.Create(r.Context(), CreateParams{
		Name:      req.Name,
		Subdomain: req.Subdomain,
		Project:   proj,
		Node:      n,
		OwnerID:   u.ID.String(),
	}, Actor{UserID: u.ID.String()})
	if lerr != nil {
		h.respondLifecycle(w, lerr)
		return
	}

	slog.Info("user created tenant", "user_id", u.ID, "tenant_id", t.ID, "subdomain", req.Subdomain)
	response.JSON(w, http.StatusAccepted, t)
}

// SSOToken generates a short-lived SSO token for accessing the tenant dashboard.
func (h *UserHandler) SSOToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for sso token", "error", err, "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil || t.OwnerID == nil || *t.OwnerID != u.ID.String() {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if t.Status != "active" {
		response.Error(w, http.StatusConflict, "tenant must be active to generate SSO token")
		return
	}

	if t.DashboardToken == nil || *t.DashboardToken == "" {
		slog.Error("tenant has no dashboard token", "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "dashboard token not configured")
		return
	}

	// Default to "free" if tier is empty
	tier := t.Tier
	if tier == "" {
		tier = "free"
	}

	// Generate HMAC-SHA256 signed token
	timestamp := time.Now().Unix()
	payload := fmt.Sprintf("%s:%s:%s:%d", u.ID.String(), t.ID, tier, timestamp)
	payloadB64 := base64.RawURLEncoding.EncodeToString([]byte(payload))

	mac := hmac.New(sha256.New, []byte(*t.DashboardToken))
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	token := payloadB64 + ":" + sigB64

	ssoURL := fmt.Sprintf("%s://%s.%s/auth/sso?token=%s", h.ssoScheme, t.Subdomain, h.ssoDomain, url.QueryEscape(token))

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"url":        ssoURL,
		"expires_in": 60,
		"tier":       tier,
	})
}

// Delete removes a tenant owned by the authenticated user.
func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for user deletion", "error", err, "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil || t.OwnerID == nil || *t.OwnerID != u.ID.String() {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	t, lerr := h.lifecycle.Delete(r.Context(), t, Actor{UserID: u.ID.String()})
	if lerr != nil {
		h.respondLifecycle(w, lerr)
		return
	}
	response.JSON(w, http.StatusOK, t)
}

// Suspend stops a tenant owned by the authenticated user.
func (h *UserHandler) Suspend(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for user suspend", "error", err, "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil || t.OwnerID == nil || *t.OwnerID != u.ID.String() {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if t.Status != "active" {
		response.Error(w, http.StatusConflict, "tenant must be active to suspend")
		return
	}

	// DB first: mark as suspended
	if err := h.store.SetSuspended(r.Context(), id); err != nil {
		if errors.Is(err, ErrStateConflict) {
			response.Error(w, http.StatusConflict, "tenant is not in a suspendable state")
			return
		}
		slog.Error("set user tenant suspended", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to suspend tenant")
		return
	}

	// Then stop container; rollback DB on failure
	if t.LXCID != nil {
		if err := h.provisioner.Suspend(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("suspend user tenant container", "error", err)
			// Rollback: restore active state
			if rbErr := h.store.SetResumed(r.Context(), id); rbErr != nil {
				slog.Error("rollback suspend: failed to restore active state", "error", rbErr)
			}
			response.Error(w, http.StatusInternalServerError, "failed to stop container")
			return
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "suspend", "tenant", id, map[string]string{"user_id": u.ID.String()})
	}

	t, err = h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant after user suspend", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	response.JSON(w, http.StatusOK, t)
}

// Resume starts a suspended tenant owned by the authenticated user.
func (h *UserHandler) Resume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := chi.URLParam(r, "tenantID")
	if !response.ValidUUID(id) {
		response.Error(w, http.StatusBadRequest, "invalid tenant ID format")
		return
	}

	t, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant for user resume", "error", err, "tenant_id", id)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if t == nil || t.OwnerID == nil || *t.OwnerID != u.ID.String() {
		response.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if t.Status != "suspended" {
		response.Error(w, http.StatusConflict, "tenant must be suspended to resume")
		return
	}

	// DB first: mark as active
	if err := h.store.SetResumed(r.Context(), id); err != nil {
		if errors.Is(err, ErrStateConflict) {
			response.Error(w, http.StatusConflict, "tenant is not in a resumable state")
			return
		}
		slog.Error("set user tenant resumed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to resume tenant")
		return
	}

	// Then start container; rollback DB on failure
	if t.LXCID != nil {
		if err := h.provisioner.Resume(r.Context(), t.ID, t.NodeID, *t.LXCID); err != nil {
			slog.Error("resume user tenant container", "error", err)
			// Rollback: restore suspended state
			if rbErr := h.store.SetSuspended(r.Context(), id); rbErr != nil {
				slog.Error("rollback resume: failed to restore suspended state", "error", rbErr)
			}
			response.Error(w, http.StatusInternalServerError, "failed to start container")
			return
		}
	}

	if h.auditStore != nil {
		h.auditStore.Log(r.Context(), "resume", "tenant", id, map[string]string{"user_id": u.ID.String()})
	}

	t, err = h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("get tenant after user resume", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	response.JSON(w, http.StatusOK, t)
}
