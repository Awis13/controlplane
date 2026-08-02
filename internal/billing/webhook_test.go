package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

// --- Mock tenant store ---

type updateBillingCall struct {
	TenantID       string
	CustomerID     string
	SubscriptionID string
	Tier           string
}

type mockTenantStore struct {
	mu sync.Mutex

	updateBillingCalls []updateBillingCall
	byCustomerCalls    []string
	byOwnerCalls       []string

	// Configurable responses.
	tenantByCustomer *TenantBilling
	tenantsByOwner   []TenantBilling
	updateBillingErr error
	byCustomerErr    error
	byOwnerErr       error
}

func (m *mockTenantStore) UpdateBilling(_ context.Context, tenantID, stripeCustomerID, stripeSubscriptionID, tier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateBillingCalls = append(m.updateBillingCalls, updateBillingCall{
		TenantID:       tenantID,
		CustomerID:     stripeCustomerID,
		SubscriptionID: stripeSubscriptionID,
		Tier:           tier,
	})
	return m.updateBillingErr
}

func (m *mockTenantStore) GetByStripeCustomerID(_ context.Context, customerID string) (*TenantBilling, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byCustomerCalls = append(m.byCustomerCalls, customerID)
	if m.byCustomerErr != nil {
		return nil, m.byCustomerErr
	}
	return m.tenantByCustomer, nil
}

func (m *mockTenantStore) GetByOwnerID(_ context.Context, ownerID string) ([]TenantBilling, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byOwnerCalls = append(m.byOwnerCalls, ownerID)
	if m.byOwnerErr != nil {
		return nil, m.byOwnerErr
	}
	return m.tenantsByOwner, nil
}

// callCount returns the total number of store calls, used to assert that a
// request produced no side effects at all.
func (m *mockTenantStore) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.updateBillingCalls) + len(m.byCustomerCalls) + len(m.byOwnerCalls)
}

// --- Helpers ---

const testWebhookSecret = "whsec_test_secret"

func testPrices() map[string]string {
	return map[string]string{
		TierStarter: "price_starter",
		TierPro:     "price_pro",
		TierStudio:  "price_studio",
	}
}

func newTestHandler() (*Handler, *mockTenantStore) {
	store := &mockTenantStore{}
	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())
	return NewHandler(svc, store), store
}

// eventPayload builds a Stripe event envelope around the given object.
// api_version is taken from the SDK because ConstructEvent rejects events from
// a different API release train.
func eventPayload(t *testing.T, eventType string, object any) []byte {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshal event object: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"id":          "evt_test_1",
		"object":      "event",
		"api_version": stripe.APIVersion,
		"type":        eventType,
		"data":        map[string]json.RawMessage{"object": raw},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return payload
}

// signedRequest builds a webhook request signed with the given secret and timestamp.
func signedRequest(payload []byte, secret string, ts time.Time) *http.Request {
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   payload,
		Secret:    secret,
		Timestamp: ts,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	return req
}

func checkoutSessionObject() map[string]any {
	return map[string]any{
		"customer":     "cus_123",
		"subscription": "sub_123",
		"customer_details": map[string]any{
			"email": "user@example.com",
		},
		"metadata": map[string]string{
			"user_id": "owner-1",
			"tier":    TierPro,
		},
	}
}

func subscriptionObject(priceID string) map[string]any {
	return subscriptionObjectWithStatus(priceID, "active")
}

func subscriptionObjectWithStatus(priceID, status string) map[string]any {
	return map[string]any{
		"id":       "sub_123",
		"customer": "cus_123",
		"status":   status,
		"items": map[string]any{
			"data": []map[string]any{
				{"price": map[string]any{"id": priceID}},
			},
		},
	}
}

// --- Signature verification ---

func TestWebhook_ValidSignature_IsDispatched(t *testing.T) {
	h, store := newTestHandler()
	store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}

	payload := eventPayload(t, EventCheckoutCompleted, checkoutSessionObject())
	rec := httptest.NewRecorder()
	h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(store.byOwnerCalls) != 1 {
		t.Fatalf("expected the event to be dispatched to a handler, got %d GetByOwnerID calls", len(store.byOwnerCalls))
	}
	if len(store.updateBillingCalls) != 1 {
		t.Fatalf("expected 1 UpdateBilling call, got %d", len(store.updateBillingCalls))
	}
}

func TestWebhook_BadSignature_IsRejected(t *testing.T) {
	payload := eventPayload(t, EventCheckoutCompleted, checkoutSessionObject())

	tests := []struct {
		name      string
		signature func() string
		body      []byte
	}{
		{
			name: "signed with the wrong secret",
			signature: func() string {
				return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
					Payload: payload, Secret: "whsec_wrong", Timestamp: time.Now(),
				}).Header
			},
			body: payload,
		},
		{
			name:      "malformed header",
			signature: func() string { return "not-a-signature-header" },
			body:      payload,
		},
		{
			name:      "header with no v1 scheme",
			signature: func() string { return "t=1234567890" },
			body:      payload,
		},
		{
			name: "payload tampered after signing",
			signature: func() string {
				return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
					Payload: payload, Secret: testWebhookSecret, Timestamp: time.Now(),
				}).Header
			},
			body: eventPayload(t, EventCheckoutCompleted, map[string]any{
				"customer": "cus_attacker",
				"metadata": map[string]string{"user_id": "owner-1", "tier": TierStudio},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", bytes.NewReader(tt.body))
			req.Header.Set("Stripe-Signature", tt.signature())
			rec := httptest.NewRecorder()
			h.Webhook(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if store.callCount() != 0 {
				t.Errorf("rejected webhook still touched the store: %d calls", store.callCount())
			}
		})
	}
}

func TestWebhook_MissingSignatureHeader_IsRejected(t *testing.T) {
	h, store := newTestHandler()
	payload := eventPayload(t, EventCheckoutCompleted, checkoutSessionObject())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.Webhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if store.callCount() != 0 {
		t.Errorf("expected no store calls, got %d", store.callCount())
	}
}

// TestWebhook_TimestampTolerance pins the replay window: VerifyWebhook uses
// webhook.ConstructEvent, which enforces webhook.DefaultTolerance (5 minutes).
func TestWebhook_TimestampTolerance(t *testing.T) {
	tests := []struct {
		name       string
		age        time.Duration
		wantStatus int
	}{
		{name: "fresh", age: 0, wantStatus: http.StatusOK},
		{name: "just inside tolerance", age: webhook.DefaultTolerance - 30*time.Second, wantStatus: http.StatusOK},
		{name: "just outside tolerance", age: webhook.DefaultTolerance + 30*time.Second, wantStatus: http.StatusBadRequest},
		{name: "long stale replay", age: 24 * time.Hour, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}

			payload := eventPayload(t, EventCheckoutCompleted, checkoutSessionObject())
			rec := httptest.NewRecorder()
			h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now().Add(-tt.age)))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusBadRequest && store.callCount() != 0 {
				t.Errorf("stale webhook still touched the store: %d calls", store.callCount())
			}
		})
	}
}

// TestWebhook_IncompatibleAPIVersion pins a non-obvious rejection: a correctly
// signed event is still refused when its api_version is from a different Stripe
// release train than the SDK expects.
func TestWebhook_IncompatibleAPIVersion(t *testing.T) {
	h, store := newTestHandler()

	payload, err := json.Marshal(map[string]any{
		"id":          "evt_test_1",
		"object":      "event",
		"api_version": "2019-12-03",
		"type":        EventCheckoutCompleted,
		"data":        map[string]any{"object": checkoutSessionObject()},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if store.callCount() != 0 {
		t.Errorf("expected no store calls, got %d", store.callCount())
	}
}

// --- Event dispatch ---

// TestWebhook_Dispatch checks that each event type reaches its handler, using
// the store call it makes as the observable signal.
func TestWebhook_Dispatch(t *testing.T) {
	tests := []struct {
		name            string
		eventType       string
		object          any
		wantByOwner     int
		wantByCustomer  int
		wantUpdateCalls int
	}{
		{
			name:            "checkout completed looks the tenant up by owner",
			eventType:       EventCheckoutCompleted,
			object:          checkoutSessionObject(),
			wantByOwner:     1,
			wantUpdateCalls: 1,
		},
		{
			name:            "subscription updated looks the tenant up by customer",
			eventType:       EventSubscriptionUpdated,
			object:          subscriptionObject("price_pro"),
			wantByCustomer:  1,
			wantUpdateCalls: 1,
		},
		{
			name:            "subscription deleted looks the tenant up by customer",
			eventType:       EventSubscriptionDeleted,
			object:          subscriptionObject("price_pro"),
			wantByCustomer:  1,
			wantUpdateCalls: 1,
		},
		{
			name:      "invoice payment failed only logs",
			eventType: EventInvoicePaymentFailed,
			object: map[string]any{
				"customer": "cus_123", "customer_email": "user@example.com",
				"amount_due": 1500, "currency": "usd",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}
			store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}

			payload := eventPayload(t, tt.eventType, tt.object)
			rec := httptest.NewRecorder()
			h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := len(store.byOwnerCalls); got != tt.wantByOwner {
				t.Errorf("GetByOwnerID calls = %d, want %d", got, tt.wantByOwner)
			}
			if got := len(store.byCustomerCalls); got != tt.wantByCustomer {
				t.Errorf("GetByStripeCustomerID calls = %d, want %d", got, tt.wantByCustomer)
			}
			if got := len(store.updateBillingCalls); got != tt.wantUpdateCalls {
				t.Errorf("UpdateBilling calls = %d, want %d", got, tt.wantUpdateCalls)
			}
		})
	}
}

func TestWebhook_UnknownEventType_AcknowledgedWithoutSideEffects(t *testing.T) {
	unknownTypes := []string{
		"customer.updated",
		"payment_intent.succeeded",
		"invoice.paid",
	}

	for _, eventType := range unknownTypes {
		t.Run(eventType, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}
			store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}

			payload := eventPayload(t, eventType, map[string]any{"customer": "cus_123"})
			rec := httptest.NewRecorder()
			h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d: unknown events must still be acknowledged", rec.Code, http.StatusOK)
			}
			if store.callCount() != 0 {
				t.Errorf("unknown event produced %d store calls, want 0", store.callCount())
			}
		})
	}
}

// TestWebhook_UnparseableEventEnvelope pins that an event whose data object is
// not a JSON object fails inside ConstructEvent, so it is rejected with 400
// before any handler runs -- the signature is valid, the envelope is not.
func TestWebhook_UnparseableEventEnvelope(t *testing.T) {
	h, store := newTestHandler()

	payload, err := json.Marshal(map[string]any{
		"id":          "evt_test_1",
		"object":      "event",
		"api_version": stripe.APIVersion,
		"type":        EventSubscriptionUpdated,
		"data":        map[string]json.RawMessage{"object": json.RawMessage(`"not-an-object"`)},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if store.callCount() != 0 {
		t.Errorf("expected no store calls, got %d", store.callCount())
	}
}

// TestWebhook_HandlerLevelParseFailure pins the graceful path: the envelope is
// valid but the inner object does not match what the handler expects, so the
// handler gives up quietly and the webhook is still acknowledged with 200.
func TestWebhook_HandlerLevelParseFailure(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}

	payload := eventPayload(t, EventSubscriptionUpdated, map[string]any{
		"id":       "sub_123",
		"customer": "cus_123",
		"items":    "not-the-expected-shape",
	})

	rec := httptest.NewRecorder()
	h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.callCount() != 0 {
		t.Errorf("expected no store calls after a parse failure, got %d", store.callCount())
	}
}

// --- Retry semantics ---

// TestWebhook_TransientStoreFailure_Returns500 pins that a store failure asks
// Stripe to redeliver rather than silently dropping a billing change.
func TestWebhook_TransientStoreFailure_Returns500(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		object    any
		setup     func(*mockTenantStore)
	}{
		{
			name:      "checkout lookup fails",
			eventType: EventCheckoutCompleted,
			object:    checkoutSessionObject(),
			setup:     func(s *mockTenantStore) { s.byOwnerErr = errStore },
		},
		{
			name:      "checkout update fails",
			eventType: EventCheckoutCompleted,
			object:    checkoutSessionObject(),
			setup: func(s *mockTenantStore) {
				s.tenantsByOwner = []TenantBilling{{ID: "tenant-1"}}
				s.updateBillingErr = errStore
			},
		},
		{
			name:      "subscription updated lookup fails",
			eventType: EventSubscriptionUpdated,
			object:    subscriptionObject("price_pro"),
			setup:     func(s *mockTenantStore) { s.byCustomerErr = errStore },
		},
		{
			name:      "subscription updated write fails",
			eventType: EventSubscriptionUpdated,
			object:    subscriptionObject("price_pro"),
			setup: func(s *mockTenantStore) {
				s.tenantByCustomer = &TenantBilling{ID: "tenant-1"}
				s.updateBillingErr = errStore
			},
		},
		{
			name:      "subscription deleted lookup fails",
			eventType: EventSubscriptionDeleted,
			object:    subscriptionObject("price_pro"),
			setup:     func(s *mockTenantStore) { s.byCustomerErr = errStore },
		},
		{
			name:      "subscription deleted write fails",
			eventType: EventSubscriptionDeleted,
			object:    subscriptionObject("price_pro"),
			setup: func(s *mockTenantStore) {
				s.tenantByCustomer = &TenantBilling{ID: "tenant-1"}
				s.updateBillingErr = errStore
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			tt.setup(store)

			payload := eventPayload(t, tt.eventType, tt.object)
			rec := httptest.NewRecorder()
			h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want %d so Stripe retries (body: %s)",
					rec.Code, http.StatusInternalServerError, rec.Body.String())
			}
		})
	}
}

// TestWebhook_PermanentConditions_Return200 pins the other half of the
// contract: conditions a retry can never fix are acknowledged, so Stripe stops.
func TestWebhook_PermanentConditions_Return200(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		object    any
		setup     func(*mockTenantStore)
	}{
		{
			name:      "checkout for a user with no tenants",
			eventType: EventCheckoutCompleted,
			object:    checkoutSessionObject(),
			setup:     func(s *mockTenantStore) { s.tenantsByOwner = nil },
		},
		{
			name:      "checkout without metadata",
			eventType: EventCheckoutCompleted,
			object:    map[string]any{"customer": "cus_123", "subscription": "sub_123"},
			setup:     func(s *mockTenantStore) {},
		},
		{
			name:      "subscription update for an unknown customer",
			eventType: EventSubscriptionUpdated,
			object:    subscriptionObject("price_pro"),
			setup:     func(s *mockTenantStore) { s.tenantByCustomer = nil },
		},
		{
			name:      "subscription deletion for an unknown customer",
			eventType: EventSubscriptionDeleted,
			object:    subscriptionObject("price_pro"),
			setup:     func(s *mockTenantStore) { s.tenantByCustomer = nil },
		},
		{
			name:      "data the handler cannot parse",
			eventType: EventSubscriptionUpdated,
			object:    map[string]any{"id": "sub_1", "customer": "cus_1", "items": "wrong-shape"},
			setup:     func(s *mockTenantStore) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			tt.setup(store)

			payload := eventPayload(t, tt.eventType, tt.object)
			rec := httptest.NewRecorder()
			h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d so Stripe stops retrying (body: %s)",
					rec.Code, http.StatusOK, rec.Body.String())
			}
		})
	}
}

// TestWebhook_RetryConvergesOnSameState covers the idempotency requirement: a
// delivery that fails transiently and is then redelivered leaves the tenant in
// the same state a single successful delivery would have produced.
func TestWebhook_RetryConvergesOnSameState(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}
	store.updateBillingErr = errStore

	payload := eventPayload(t, EventSubscriptionUpdated, subscriptionObject("price_pro"))

	// First delivery: the store is down.
	first := httptest.NewRecorder()
	h.Webhook(first, signedRequest(payload, testWebhookSecret, time.Now()))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first delivery status = %d, want %d", first.Code, http.StatusInternalServerError)
	}

	// Stripe redelivers the same event once the store recovers.
	store.updateBillingErr = nil
	second := httptest.NewRecorder()
	h.Webhook(second, signedRequest(payload, testWebhookSecret, time.Now()))
	if second.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d, want %d", second.Code, http.StatusOK)
	}

	// A third delivery of the same event must not change anything further.
	third := httptest.NewRecorder()
	h.Webhook(third, signedRequest(payload, testWebhookSecret, time.Now()))
	if third.Code != http.StatusOK {
		t.Fatalf("third delivery status = %d, want %d", third.Code, http.StatusOK)
	}

	want := updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "sub_123", Tier: TierPro}
	if len(store.updateBillingCalls) != 3 {
		t.Fatalf("UpdateBilling calls = %d, want 3 (one per delivery)", len(store.updateBillingCalls))
	}
	for i, got := range store.updateBillingCalls {
		if got != want {
			t.Errorf("delivery %d wrote %+v, want %+v", i+1, got, want)
		}
	}
}

// TestWebhook_LateUpdateAfterDeletionKeepsTenantFree covers out-of-order
// delivery, which Stripe does not guarantee against: the deletion lands first
// and downgrades the tenant, then a stale update for the same subscription
// arrives carrying its paid price. Because that update reports a canceled
// status, the tenant stays free instead of having its paid tier resurrected.
func TestWebhook_LateUpdateAfterDeletionKeepsTenantFree(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}

	// 1. customer.subscription.deleted arrives and downgrades the tenant.
	deleted := eventPayload(t, EventSubscriptionDeleted, subscriptionObject("price_pro"))
	first := httptest.NewRecorder()
	h.Webhook(first, signedRequest(deleted, testWebhookSecret, time.Now()))
	if first.Code != http.StatusOK {
		t.Fatalf("deletion status = %d, want %d", first.Code, http.StatusOK)
	}

	// The tenant is now on the free tier, as the store would report from here on.
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}

	// 2. A stale customer.subscription.updated for the same subscription arrives
	// late, still carrying price_pro but reporting the canceled status.
	late := eventPayload(t, EventSubscriptionUpdated, subscriptionObjectWithStatus("price_pro", "canceled"))
	second := httptest.NewRecorder()
	h.Webhook(second, signedRequest(late, testWebhookSecret, time.Now()))
	if second.Code != http.StatusOK {
		t.Fatalf("late update status = %d, want %d", second.Code, http.StatusOK)
	}

	if len(store.updateBillingCalls) != 2 {
		t.Fatalf("UpdateBilling calls = %d, want 2", len(store.updateBillingCalls))
	}
	want := updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "", Tier: TierFree}
	for i, got := range store.updateBillingCalls {
		if got != want {
			t.Errorf("write %d = %+v, want %+v: the paid tier must not come back", i+1, got, want)
		}
	}
}

// failingReader stands in for a request body that fails mid-read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestWebhook_BodyReadError_Returns400(t *testing.T) {
	h, store := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", nil)
	req.Body = io.NopCloser(failingReader{})
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	rec := httptest.NewRecorder()
	h.Webhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if store.callCount() != 0 {
		t.Errorf("expected no store calls, got %d", store.callCount())
	}
}

// --- Billing disabled ---

func TestWebhook_BillingNotConfigured_Returns501(t *testing.T) {
	store := &mockTenantStore{}
	h := NewHandler(nil, store)

	payload := eventPayload(t, EventCheckoutCompleted, checkoutSessionObject())
	rec := httptest.NewRecorder()
	h.Webhook(rec, signedRequest(payload, testWebhookSecret, time.Now()))

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	if store.callCount() != 0 {
		t.Errorf("expected no store calls, got %d", store.callCount())
	}
}

// TestVerifyWebhook_ErrorsAreWrapped pins that verification failures surface as
// a wrapped error with a nil event, and that the tolerance check runs before the
// signature check.
func TestVerifyWebhook_ErrorsAreWrapped(t *testing.T) {
	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())
	payload := eventPayload(t, EventCheckoutCompleted, checkoutSessionObject())

	tests := []struct {
		name    string
		header  string
		wantErr error
	}{
		{
			name:    "recent timestamp with a bogus signature",
			header:  fmt.Sprintf("t=%d,v1=deadbeef", time.Now().Unix()),
			wantErr: webhook.ErrNoValidSignature,
		},
		{
			name:    "stale timestamp is rejected before the signature is checked",
			header:  "t=1,v1=deadbeef",
			wantErr: webhook.ErrTooOld,
		},
		{
			name:    "header without a signature scheme",
			header:  "nonsense",
			wantErr: webhook.ErrInvalidHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := svc.VerifyWebhook(payload, tt.header)
			if err == nil {
				t.Fatal("expected an error")
			}
			if event != nil {
				t.Errorf("expected a nil event on failure, got %+v", event)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tt.wantErr)
			}
		})
	}
}
