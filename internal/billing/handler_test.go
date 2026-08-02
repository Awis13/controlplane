package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v86"

	"controlplane/internal/auth"
	"controlplane/internal/user"
)

// --- Helpers ---

func testUser() *user.User {
	return &user.User{
		ID:    uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Email: "user@example.com",
	}
}

// authedRequest builds a JSON request carrying an authenticated user.
func authedRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(auth.SetUserForTest(req.Context(), testUser()))
}

// mockStripeBackend points the Stripe SDK at a local server for the duration of
// the test, so the service methods can be exercised without network access.
// The previous backend is restored on cleanup.
func mockStripeBackend(t *testing.T, status int, body string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))

	previous := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL:               stripe.String(srv.URL),
		MaxNetworkRetries: stripe.Int64(0),
	}))

	t.Cleanup(func() {
		stripe.SetBackend(stripe.APIBackend, previous)
		srv.Close()
	})
}

func decodeURLResponse(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return body["url"]
}

// --- CreateCheckout ---

func TestCreateCheckout_Validation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "free tier is not purchasable",
			body:       `{"tier":"free","success_url":"https://ok","cancel_url":"https://no"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown tier",
			body:       `{"tier":"enterprise","success_url":"https://ok","cancel_url":"https://no"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty tier",
			body:       `{"tier":"","success_url":"https://ok","cancel_url":"https://no"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing success_url",
			body:       `{"tier":"pro","cancel_url":"https://no"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing cancel_url",
			body:       `{"tier":"pro","success_url":"https://ok"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed JSON",
			body:       `{"tier":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown field is rejected by the decoder",
			body:       `{"tier":"pro","success_url":"https://ok","cancel_url":"https://no","extra":1}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler()
			rec := httptest.NewRecorder()
			h.CreateCheckout(rec, authedRequest(http.MethodPost, "/api/v1/billing/checkout", tt.body))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestCreateCheckout_TierWithoutConfiguredPrice pins the case where a tier is
// valid but has no Stripe price configured: it is refused rather than sent to
// Stripe with an empty price.
func TestCreateCheckout_TierWithoutConfiguredPrice(t *testing.T) {
	store := &mockTenantStore{}
	svc := NewService("sk_test_unused", testWebhookSecret, map[string]string{TierStarter: "price_starter"})
	h := NewHandler(svc, store)

	rec := httptest.NewRecorder()
	h.CreateCheckout(rec, authedRequest(http.MethodPost, "/api/v1/billing/checkout",
		`{"tier":"studio","success_url":"https://ok","cancel_url":"https://no"}`))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestCreateCheckout_Unauthorized(t *testing.T) {
	h, _ := newTestHandler()

	// No user in context.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/checkout",
		strings.NewReader(`{"tier":"pro","success_url":"https://ok","cancel_url":"https://no"}`))
	rec := httptest.NewRecorder()
	h.CreateCheckout(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCreateCheckout_NotConfigured_Returns501(t *testing.T) {
	h := NewHandler(nil, &mockTenantStore{})

	rec := httptest.NewRecorder()
	h.CreateCheckout(rec, authedRequest(http.MethodPost, "/api/v1/billing/checkout",
		`{"tier":"pro","success_url":"https://ok","cancel_url":"https://no"}`))

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

func TestCreateCheckout_Success(t *testing.T) {
	mockStripeBackend(t, http.StatusOK, `{"id":"cs_test_1","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test_1"}`)

	h, _ := newTestHandler()
	rec := httptest.NewRecorder()
	h.CreateCheckout(rec, authedRequest(http.MethodPost, "/api/v1/billing/checkout",
		`{"tier":"pro","success_url":"https://ok","cancel_url":"https://no"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := decodeURLResponse(t, rec); got != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Errorf("url = %q, want the session URL from Stripe", got)
	}
}

func TestCreateCheckout_StripeError_Returns500(t *testing.T) {
	mockStripeBackend(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"no such price"}}`)

	h, _ := newTestHandler()
	rec := httptest.NewRecorder()
	h.CreateCheckout(rec, authedRequest(http.MethodPost, "/api/v1/billing/checkout",
		`{"tier":"pro","success_url":"https://ok","cancel_url":"https://no"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

// --- CreatePortal ---

func TestCreatePortal_Validation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "missing return_url", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "empty return_url", body: `{"return_url":""}`, wantStatus: http.StatusBadRequest},
		{name: "malformed JSON", body: `{"return_url":`, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"return_url":"https://back","extra":1}`, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			customerID := "cus_123"
			store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", StripeCustomerID: &customerID}}

			rec := httptest.NewRecorder()
			h.CreatePortal(rec, authedRequest(http.MethodPost, "/api/v1/billing/portal", tt.body))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if store.callCount() != 0 {
				t.Errorf("expected validation to fail before the store lookup, got %d calls", store.callCount())
			}
		})
	}
}

func TestCreatePortal_Unauthorized(t *testing.T) {
	h, _ := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/portal", strings.NewReader(`{"return_url":"https://back"}`))
	rec := httptest.NewRecorder()
	h.CreatePortal(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCreatePortal_NotConfigured_Returns501(t *testing.T) {
	h := NewHandler(nil, &mockTenantStore{})

	rec := httptest.NewRecorder()
	h.CreatePortal(rec, authedRequest(http.MethodPost, "/api/v1/billing/portal", `{"return_url":"https://back"}`))

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

// TestCreatePortal_NoStripeCustomer covers every shape of "this user has no
// Stripe customer to manage".
func TestCreatePortal_NoStripeCustomer(t *testing.T) {
	empty := ""
	tests := []struct {
		name    string
		tenants []TenantBilling
	}{
		{name: "no tenants", tenants: nil},
		{name: "tenant without a customer ID", tenants: []TenantBilling{{ID: "tenant-1"}}},
		{name: "tenant with an empty customer ID", tenants: []TenantBilling{{ID: "tenant-1", StripeCustomerID: &empty}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantsByOwner = tt.tenants

			rec := httptest.NewRecorder()
			h.CreatePortal(rec, authedRequest(http.MethodPost, "/api/v1/billing/portal", `{"return_url":"https://back"}`))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestCreatePortal_StoreError_Returns500(t *testing.T) {
	h, store := newTestHandler()
	store.byOwnerErr = errStore

	rec := httptest.NewRecorder()
	h.CreatePortal(rec, authedRequest(http.MethodPost, "/api/v1/billing/portal", `{"return_url":"https://back"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestCreatePortal_PicksFirstTenantWithCustomerID pins that the first tenant
// carrying a non-empty customer ID wins, even if earlier tenants have none.
func TestCreatePortal_PicksFirstTenantWithCustomerID(t *testing.T) {
	mockStripeBackend(t, http.StatusOK, `{"id":"bps_test_1","object":"billing_portal.session","url":"https://billing.stripe.com/session/bps_test_1"}`)

	h, store := newTestHandler()
	empty := ""
	second := "cus_second"
	third := "cus_third"
	store.tenantsByOwner = []TenantBilling{
		{ID: "tenant-1", StripeCustomerID: &empty},
		{ID: "tenant-2", StripeCustomerID: &second},
		{ID: "tenant-3", StripeCustomerID: &third},
	}

	rec := httptest.NewRecorder()
	h.CreatePortal(rec, authedRequest(http.MethodPost, "/api/v1/billing/portal", `{"return_url":"https://back"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := decodeURLResponse(t, rec); got != "https://billing.stripe.com/session/bps_test_1" {
		t.Errorf("url = %q, want the portal URL from Stripe", got)
	}
}

func TestCreatePortal_StripeError_Returns500(t *testing.T) {
	mockStripeBackend(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"no such customer"}}`)

	h, store := newTestHandler()
	customerID := "cus_123"
	store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", StripeCustomerID: &customerID}}

	rec := httptest.NewRecorder()
	h.CreatePortal(rec, authedRequest(http.MethodPost, "/api/v1/billing/portal", `{"return_url":"https://back"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// --- Service surface reachable through the handlers ---

func TestCreateCheckoutSession_SendsExpectedParameters(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cs_test_1","url":"https://checkout.stripe.com/c/pay/cs_test_1"}`)
	}))
	defer srv.Close()

	previous := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL:               stripe.String(srv.URL),
		MaxNetworkRetries: stripe.Int64(0),
	}))
	defer stripe.SetBackend(stripe.APIBackend, previous)

	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())
	url, err := svc.CreateCheckoutSession("user@example.com", "price_pro", "https://ok", "https://no",
		map[string]string{"user_id": "owner-1", "tier": TierPro})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if url != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Errorf("url = %q", url)
	}

	for _, want := range []string{
		"mode=subscription",
		"line_items[0][price]=price_pro",
		"line_items[0][quantity]=1",
		"customer_email=user%40example.com",
		"metadata[tier]=pro",
		"metadata[user_id]=owner-1",
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %q\nbody: %s", want, gotBody)
		}
	}
}

func TestCreateCustomerPortalSession_ReturnsURL(t *testing.T) {
	mockStripeBackend(t, http.StatusOK, `{"id":"bps_test_1","url":"https://billing.stripe.com/session/bps_test_1"}`)

	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())
	url, err := svc.CreateCustomerPortalSession("cus_123", "https://back")
	if err != nil {
		t.Fatalf("CreateCustomerPortalSession: %v", err)
	}
	if url != "https://billing.stripe.com/session/bps_test_1" {
		t.Errorf("url = %q", url)
	}
}

func TestServiceSessionErrorsAreWrapped(t *testing.T) {
	mockStripeBackend(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"boom"}}`)

	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())

	if _, err := svc.CreateCheckoutSession("user@example.com", "price_pro", "https://ok", "https://no", nil); err == nil {
		t.Error("expected an error from CreateCheckoutSession")
	} else if !strings.Contains(err.Error(), "create checkout session") {
		t.Errorf("error = %v, want it wrapped with context", err)
	}

	if _, err := svc.CreateCustomerPortalSession("cus_123", "https://back"); err == nil {
		t.Error("expected an error from CreateCustomerPortalSession")
	} else if !strings.Contains(err.Error(), "create portal session") {
		t.Errorf("error = %v, want it wrapped with context", err)
	}
}

// --- Status ---

func TestStatus_ReportsTierLimits(t *testing.T) {
	h, store := newTestHandler()
	customerID := "cus_123"
	store.tenantsByOwner = []TenantBilling{
		{ID: "tenant-1", Name: "one", Tier: TierPro, StripeCustomerID: &customerID},
		{ID: "tenant-2", Name: "two", Tier: TierFree},
	}

	rec := httptest.NewRecorder()
	h.Status(rec, authedRequest(http.MethodGet, "/api/v1/billing/status", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Tenants []struct {
			TenantID  string     `json:"tenant_id"`
			Tier      string     `json:"tier"`
			Limits    TierLimits `json:"limits"`
			HasStripe bool       `json:"has_stripe"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tenants) != 2 {
		t.Fatalf("tenants = %d, want 2", len(body.Tenants))
	}
	if !body.Tenants[0].HasStripe || body.Tenants[1].HasStripe {
		t.Errorf("has_stripe = %v/%v, want true/false", body.Tenants[0].HasStripe, body.Tenants[1].HasStripe)
	}
	if body.Tenants[0].Limits != GetLimits(TierPro) {
		t.Errorf("limits for pro = %+v, want %+v", body.Tenants[0].Limits, GetLimits(TierPro))
	}
}

func TestStatus_StoreError_Returns500(t *testing.T) {
	h, store := newTestHandler()
	store.byOwnerErr = errStore

	rec := httptest.NewRecorder()
	h.Status(rec, authedRequest(http.MethodGet, "/api/v1/billing/status", ""))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestStatus_NoTenants_ReturnsEmptyList(t *testing.T) {
	h, store := newTestHandler()
	store.tenantsByOwner = nil

	rec := httptest.NewRecorder()
	h.Status(rec, authedRequest(http.MethodGet, "/api/v1/billing/status", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `"tenants":[]`) {
		t.Errorf("body = %s, want an empty tenants array rather than null", rec.Body.String())
	}
}

func TestStatus_NotConfigured_Returns501(t *testing.T) {
	h := NewHandler(nil, &mockTenantStore{})

	rec := httptest.NewRecorder()
	h.Status(rec, authedRequest(http.MethodGet, "/api/v1/billing/status", ""))

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

func TestStatus_Unauthorized(t *testing.T) {
	h, _ := newTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/status", nil)
	rec := httptest.NewRecorder()
	h.Status(rec, req.WithContext(context.Background()))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
