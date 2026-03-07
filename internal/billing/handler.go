package billing

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"controlplane/internal/auth"
	"controlplane/internal/response"
)

// TenantStore defines the tenant store methods needed by billing handlers.
type TenantStore interface {
	UpdateBilling(ctx context.Context, tenantID, stripeCustomerID, stripeSubscriptionID, tier string) error
	GetByStripeCustomerID(ctx context.Context, customerID string) (*TenantBilling, error)
	GetByOwnerID(ctx context.Context, ownerID string) ([]TenantBilling, error)
}

// TenantBilling is a lightweight struct for billing operations.
type TenantBilling struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	Tier                 string  `json:"tier"`
	StripeCustomerID     *string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID *string `json:"stripe_subscription_id,omitempty"`
	OwnerID              *string `json:"owner_id,omitempty"`
}

// Handler provides HTTP handlers for billing endpoints.
type Handler struct {
	service     *Service
	tenantStore TenantStore
	enabled     bool
}

// NewHandler creates a new billing handler.
// If service is nil (Stripe not configured), all endpoints return 501.
func NewHandler(service *Service, tenantStore TenantStore) *Handler {
	return &Handler{
		service:     service,
		tenantStore: tenantStore,
		enabled:     service != nil,
	}
}

// checkEnabled returns true if billing is enabled.
// Writes a 501 response and returns false if not.
func (h *Handler) checkEnabled(w http.ResponseWriter) bool {
	if !h.enabled {
		response.Error(w, http.StatusNotImplemented, "billing is not configured")
		return false
	}
	return true
}

// CreateCheckout handles POST /api/v1/billing/checkout
// Creates a Stripe Checkout session for subscription upgrade.
func (h *Handler) CreateCheckout(w http.ResponseWriter, r *http.Request) {
	if !h.checkEnabled(w) {
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		Tier       string `json:"tier"`
		SuccessURL string `json:"success_url"`
		CancelURL  string `json:"cancel_url"`
	}
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !IsPaidTier(req.Tier) {
		response.Error(w, http.StatusBadRequest, "invalid tier: must be starter, pro, or studio")
		return
	}

	priceID := h.service.GetPriceID(req.Tier)
	if priceID == "" {
		response.Error(w, http.StatusBadRequest, "tier not available for purchase")
		return
	}

	if req.SuccessURL == "" || req.CancelURL == "" {
		response.Error(w, http.StatusBadRequest, "success_url and cancel_url are required")
		return
	}

	metadata := map[string]string{
		"user_id":    u.ID.String(),
		"user_email": u.Email,
		"tier":       req.Tier,
	}

	sessionURL, err := h.service.CreateCheckoutSession(u.Email, priceID, req.SuccessURL, req.CancelURL, metadata)
	if err != nil {
		slog.Error("billing: create checkout session", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to create checkout session")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"url": sessionURL})
}

// CreatePortal handles POST /api/v1/billing/portal
// Creates a Stripe Billing Portal session for subscription management.
func (h *Handler) CreatePortal(w http.ResponseWriter, r *http.Request) {
	if !h.checkEnabled(w) {
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		ReturnURL string `json:"return_url"`
	}
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ReturnURL == "" {
		response.Error(w, http.StatusBadRequest, "return_url is required")
		return
	}

	// Find user's tenant to get stripe_customer_id
	tenants, err := h.tenantStore.GetByOwnerID(r.Context(), u.ID.String())
	if err != nil {
		slog.Error("billing: get tenants by owner", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to look up billing info")
		return
	}

	// Find a tenant with a Stripe customer ID
	var customerID string
	for _, t := range tenants {
		if t.StripeCustomerID != nil && *t.StripeCustomerID != "" {
			customerID = *t.StripeCustomerID
			break
		}
	}

	if customerID == "" {
		response.Error(w, http.StatusBadRequest, "no active subscription found")
		return
	}

	sessionURL, err := h.service.CreateCustomerPortalSession(customerID, req.ReturnURL)
	if err != nil {
		slog.Error("billing: create portal session", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to create portal session")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"url": sessionURL})
}

// Status handles GET /api/v1/billing/status
// Returns the current user's billing status for all their tenants.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if !h.checkEnabled(w) {
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tenants, err := h.tenantStore.GetByOwnerID(r.Context(), u.ID.String())
	if err != nil {
		slog.Error("billing: get tenants by owner", "error", err, "user_id", u.ID)
		response.Error(w, http.StatusInternalServerError, "failed to look up billing info")
		return
	}

	type tenantStatus struct {
		TenantID   string     `json:"tenant_id"`
		TenantName string     `json:"tenant_name"`
		Tier       string     `json:"tier"`
		Limits     TierLimits `json:"limits"`
		HasStripe  bool       `json:"has_stripe"`
	}

	statuses := make([]tenantStatus, 0, len(tenants))
	for _, t := range tenants {
		statuses = append(statuses, tenantStatus{
			TenantID:   t.ID,
			TenantName: t.Name,
			Tier:       t.Tier,
			Limits:     GetLimits(t.Tier),
			HasStripe:  t.StripeCustomerID != nil && *t.StripeCustomerID != "",
		})
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"tenants": statuses,
	})
}

// Webhook handles POST /api/v1/stripe/webhook
// Verifies Stripe webhook signature and processes events.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	if !h.checkEnabled(w) {
		return
	}

	// Read raw body for signature verification (must not be JSON-parsed first)
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		slog.Error("billing: read webhook body", "error", err)
		response.Error(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	signature := r.Header.Get("Stripe-Signature")
	if signature == "" {
		response.Error(w, http.StatusBadRequest, "missing Stripe-Signature header")
		return
	}

	event, err := h.service.VerifyWebhook(payload, signature)
	if err != nil {
		slog.Warn("billing: webhook signature verification failed", "error", err)
		response.Error(w, http.StatusBadRequest, "invalid webhook signature")
		return
	}

	slog.Info("billing: webhook received", "type", event.Type, "id", event.ID)

	switch event.Type {
	case EventCheckoutCompleted:
		h.handleCheckoutCompleted(r.Context(), event.Data.Raw)
	case EventSubscriptionUpdated:
		h.handleSubscriptionUpdated(r.Context(), event.Data.Raw)
	case EventSubscriptionDeleted:
		h.handleSubscriptionDeleted(r.Context(), event.Data.Raw)
	case EventInvoicePaymentFailed:
		h.handleInvoicePaymentFailed(event.Data.Raw)
	default:
		slog.Debug("billing: unhandled webhook event", "type", event.Type)
	}

	// Always return 200 to acknowledge receipt
	w.WriteHeader(http.StatusOK)
}

// handleCheckoutCompleted processes checkout.session.completed events.
// Updates the tenant with Stripe customer ID, subscription ID, and tier.
func (h *Handler) handleCheckoutCompleted(ctx context.Context, data []byte) {
	session, err := ParseCheckoutSession(data)
	if err != nil {
		slog.Error("billing: parse checkout session", "error", err)
		return
	}

	userID := session.Metadata["user_id"]
	tier := session.Metadata["tier"]
	if userID == "" || tier == "" {
		slog.Error("billing: checkout session missing metadata", "customer", session.CustomerID)
		return
	}

	// Find the user's tenant to update
	tenants, err := h.tenantStore.GetByOwnerID(ctx, userID)
	if err != nil {
		slog.Error("billing: get tenants for checkout", "error", err, "user_id", userID)
		return
	}

	if len(tenants) == 0 {
		slog.Warn("billing: no tenants found for user after checkout", "user_id", userID)
		return
	}

	// Update the first tenant (in multi-tenant future, metadata could specify which)
	t := tenants[0]
	if err := h.tenantStore.UpdateBilling(ctx, t.ID, session.CustomerID, session.SubscriptionID, tier); err != nil {
		slog.Error("billing: update tenant after checkout",
			"error", err, "tenant_id", t.ID, "tier", tier)
		return
	}

	slog.Info("billing: checkout completed",
		"tenant_id", t.ID, "tier", tier, "customer", session.CustomerID)
}

// handleSubscriptionUpdated processes customer.subscription.updated events.
// Updates the tenant tier based on the new price ID.
func (h *Handler) handleSubscriptionUpdated(ctx context.Context, data []byte) {
	sub, err := ParseSubscription(data)
	if err != nil {
		slog.Error("billing: parse subscription updated", "error", err)
		return
	}

	tier := h.service.GetTierFromPriceID(sub.PriceID)

	tenant, err := h.tenantStore.GetByStripeCustomerID(ctx, sub.CustomerID)
	if err != nil {
		slog.Error("billing: get tenant by customer ID", "error", err, "customer", sub.CustomerID)
		return
	}
	if tenant == nil {
		slog.Warn("billing: no tenant found for subscription update", "customer", sub.CustomerID)
		return
	}

	if err := h.tenantStore.UpdateBilling(ctx, tenant.ID, sub.CustomerID, sub.ID, tier); err != nil {
		slog.Error("billing: update tenant tier",
			"error", err, "tenant_id", tenant.ID, "tier", tier)
		return
	}

	slog.Info("billing: subscription updated",
		"tenant_id", tenant.ID, "tier", tier, "subscription", sub.ID)
}

// handleSubscriptionDeleted processes customer.subscription.deleted events.
// Downgrades the tenant to the free tier.
func (h *Handler) handleSubscriptionDeleted(ctx context.Context, data []byte) {
	sub, err := ParseSubscription(data)
	if err != nil {
		slog.Error("billing: parse subscription deleted", "error", err)
		return
	}

	tenant, err := h.tenantStore.GetByStripeCustomerID(ctx, sub.CustomerID)
	if err != nil {
		slog.Error("billing: get tenant by customer ID", "error", err, "customer", sub.CustomerID)
		return
	}
	if tenant == nil {
		slog.Warn("billing: no tenant found for subscription deletion", "customer", sub.CustomerID)
		return
	}

	if err := h.tenantStore.UpdateBilling(ctx, tenant.ID, sub.CustomerID, "", TierFree); err != nil {
		slog.Error("billing: downgrade tenant to free",
			"error", err, "tenant_id", tenant.ID)
		return
	}

	slog.Info("billing: subscription deleted, downgraded to free",
		"tenant_id", tenant.ID, "customer", sub.CustomerID)
}

// handleInvoicePaymentFailed logs a warning but does not suspend the tenant.
func (h *Handler) handleInvoicePaymentFailed(data []byte) {
	inv, err := ParseInvoice(data)
	if err != nil {
		slog.Error("billing: parse invoice payment failed", "error", err)
		return
	}

	slog.Warn("billing: invoice payment failed",
		"customer", inv.CustomerID, "email", inv.CustomerEmail,
		"amount", inv.AmountDue, "currency", inv.Currency)
}
