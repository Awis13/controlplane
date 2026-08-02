package billing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// objectJSON marshals an event data object the way the dispatcher hands it to a
// handler: the raw contents of data.object.
func objectJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal object: %v", err)
	}
	return raw
}

var errStore = errors.New("store unavailable")

// mustHandle asserts the handler reported success, meaning either the update
// went through or the condition is permanent and a retry would not help.
func mustHandle(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected no error, so the webhook is acknowledged: %v", err)
	}
}

// wantRetry asserts the handler surfaced a transient failure, which the webhook
// endpoint turns into a 500 so Stripe redelivers.
func wantRetry(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error so Stripe retries the delivery, got nil")
	}
}

// --- handleCheckoutCompleted ---

func TestHandleCheckoutCompleted_HappyPath(t *testing.T) {
	h, store := newTestHandler()
	store.tenantsByOwner = []TenantBilling{
		{ID: "tenant-1", Name: "first", Tier: TierFree},
		{ID: "tenant-2", Name: "second", Tier: TierFree},
	}

	mustHandle(t, h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject())))

	if len(store.byOwnerCalls) != 1 || store.byOwnerCalls[0] != "owner-1" {
		t.Fatalf("GetByOwnerID calls = %v, want one lookup for owner-1", store.byOwnerCalls)
	}
	if len(store.updateBillingCalls) != 1 {
		t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
	}

	got := store.updateBillingCalls[0]
	want := updateBillingCall{
		TenantID:       "tenant-1", // the first tenant returned, as the handler documents
		CustomerID:     "cus_123",
		SubscriptionID: "sub_123",
		Tier:           TierPro,
	}
	if got != want {
		t.Errorf("UpdateBilling = %+v, want %+v", got, want)
	}
}

func TestHandleCheckoutCompleted_MissingMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
	}{
		{name: "no metadata at all", metadata: nil},
		{name: "user_id only", metadata: map[string]string{"user_id": "owner-1"}},
		{name: "tier only", metadata: map[string]string{"tier": TierPro}},
		{name: "empty values", metadata: map[string]string{"user_id": "", "tier": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}

			session := checkoutSessionObject()
			session["metadata"] = tt.metadata
			mustHandle(t, h.handleCheckoutCompleted(context.Background(), objectJSON(t, session)))

			if store.callCount() != 0 {
				t.Errorf("expected the handler to bail out before touching the store, got %d calls", store.callCount())
			}
		})
	}
}

// TestHandleCheckoutCompleted_TierFromMetadataIsNotValidated pins current
// behavior: whatever tier string the session metadata carries is written to the
// tenant unchecked, including values that are not valid tiers.
func TestHandleCheckoutCompleted_TierFromMetadataIsNotValidated(t *testing.T) {
	h, store := newTestHandler()
	store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}

	session := checkoutSessionObject()
	session["metadata"] = map[string]string{"user_id": "owner-1", "tier": "enterprise"}
	mustHandle(t, h.handleCheckoutCompleted(context.Background(), objectJSON(t, session)))

	if len(store.updateBillingCalls) != 1 {
		t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
	}
	if got := store.updateBillingCalls[0].Tier; got != "enterprise" {
		t.Errorf("tier written = %q, want %q (unvalidated metadata is passed straight through)", got, "enterprise")
	}
}

func TestHandleCheckoutCompleted_NoTenantsForOwner(t *testing.T) {
	h, store := newTestHandler()
	store.tenantsByOwner = nil

	mustHandle(t, h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject())))

	if len(store.byOwnerCalls) != 1 {
		t.Errorf("GetByOwnerID calls = %d, want 1", len(store.byOwnerCalls))
	}
	if len(store.updateBillingCalls) != 0 {
		t.Errorf("expected no UpdateBilling when the user has no tenants, got %d", len(store.updateBillingCalls))
	}
}

func TestHandleCheckoutCompleted_StoreErrors(t *testing.T) {
	t.Run("lookup fails", func(t *testing.T) {
		h, store := newTestHandler()
		store.byOwnerErr = errStore

		wantRetry(t, h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject())))

		if len(store.updateBillingCalls) != 0 {
			t.Errorf("expected no UpdateBilling after a failed lookup, got %d", len(store.updateBillingCalls))
		}
	})

	t.Run("update fails", func(t *testing.T) {
		h, store := newTestHandler()
		store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}
		store.updateBillingErr = errStore

		wantRetry(t, h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject())))

		if len(store.updateBillingCalls) != 1 {
			t.Errorf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
		}
	})
}

func TestHandleCheckoutCompleted_UnparseableData(t *testing.T) {
	h, store := newTestHandler()

	mustHandle(t, h.handleCheckoutCompleted(context.Background(), []byte(`{"metadata": "not-a-map"}`)))

	if store.callCount() != 0 {
		t.Errorf("expected no store calls for unparseable data, got %d", store.callCount())
	}
}

// --- handleSubscriptionUpdated ---

func TestHandleSubscriptionUpdated_TierMapping(t *testing.T) {
	tests := []struct {
		name     string
		priceID  string
		wantTier string
	}{
		{name: "starter price", priceID: "price_starter", wantTier: TierStarter},
		{name: "pro price", priceID: "price_pro", wantTier: TierPro},
		{name: "studio price", priceID: "price_studio", wantTier: TierStudio},
		{name: "unknown price falls back to free", priceID: "price_does_not_exist", wantTier: TierFree},
		{name: "empty price falls back to free", priceID: "", wantTier: TierFree},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}

			mustHandle(t, h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject(tt.priceID))))

			if len(store.updateBillingCalls) != 1 {
				t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
			}
			got := store.updateBillingCalls[0]
			want := updateBillingCall{
				TenantID:       "tenant-1",
				CustomerID:     "cus_123",
				SubscriptionID: "sub_123",
				Tier:           tt.wantTier,
			}
			if got != want {
				t.Errorf("UpdateBilling = %+v, want %+v", got, want)
			}
		})
	}
}

// TestHandleSubscriptionUpdated_StatusDecidesTier covers the tier decision for
// every subscription status: terminal statuses drop to free and clear the
// subscription reference, live statuses follow the price, and anything with an
// unresolved payment leaves the tenant alone.
func TestHandleSubscriptionUpdated_StatusDecidesTier(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		priceID   string
		wantWrite bool
		want      updateBillingCall
	}{
		{
			name: "active follows the price", status: "active", priceID: "price_pro", wantWrite: true,
			want: updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "sub_123", Tier: TierPro},
		},
		{
			name: "trialing follows the price", status: "trialing", priceID: "price_studio", wantWrite: true,
			want: updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "sub_123", Tier: TierStudio},
		},
		{
			name: "canceled drops to free and clears the reference", status: "canceled", priceID: "price_pro", wantWrite: true,
			want: updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "", Tier: TierFree},
		},
		{
			name: "unpaid drops to free", status: "unpaid", priceID: "price_pro", wantWrite: true,
			want: updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "", Tier: TierFree},
		},
		{
			name: "incomplete_expired drops to free", status: "incomplete_expired", priceID: "price_studio", wantWrite: true,
			want: updateBillingCall{TenantID: "tenant-1", CustomerID: "cus_123", SubscriptionID: "", Tier: TierFree},
		},
		{name: "past_due leaves the tier alone", status: "past_due", priceID: "price_pro"},
		{name: "incomplete leaves the tier alone", status: "incomplete", priceID: "price_pro"},
		{name: "paused leaves the tier alone", status: "paused", priceID: "price_pro"},
		{name: "an unknown future status leaves the tier alone", status: "quantum_superposition", priceID: "price_pro"},
		{name: "a missing status leaves the tier alone", status: "", priceID: "price_pro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}

			mustHandle(t, h.handleSubscriptionUpdated(context.Background(),
				objectJSON(t, subscriptionObjectWithStatus(tt.priceID, tt.status))))

			if !tt.wantWrite {
				if store.callCount() != 0 {
					t.Fatalf("expected no store calls at all, got %d", store.callCount())
				}
				return
			}
			if len(store.updateBillingCalls) != 1 {
				t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
			}
			if got := store.updateBillingCalls[0]; got != tt.want {
				t.Errorf("UpdateBilling = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestHandleSubscriptionUpdated_TerminalStatusIgnoresPrice pins that a
// terminal status wins over the price, so a canceled subscription still
// carrying a paid price does not keep the tenant on that tier.
func TestHandleSubscriptionUpdated_TerminalStatusIgnoresPrice(t *testing.T) {
	for _, priceID := range []string{"price_starter", "price_pro", "price_studio", "price_unknown", ""} {
		t.Run("price="+priceID, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierStudio}

			mustHandle(t, h.handleSubscriptionUpdated(context.Background(),
				objectJSON(t, subscriptionObjectWithStatus(priceID, "canceled"))))

			if len(store.updateBillingCalls) != 1 {
				t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
			}
			if got := store.updateBillingCalls[0].Tier; got != TierFree {
				t.Errorf("tier = %q, want %q", got, TierFree)
			}
		})
	}
}

// TestHandleSubscriptionUpdated_UnresolvedStatusSkipsLookup pins that an
// unresolved status costs nothing: the handler returns before even looking the
// tenant up, so a store outage cannot turn it into a retry.
func TestHandleSubscriptionUpdated_UnresolvedStatusSkipsLookup(t *testing.T) {
	h, store := newTestHandler()
	store.byCustomerErr = errStore

	mustHandle(t, h.handleSubscriptionUpdated(context.Background(),
		objectJSON(t, subscriptionObjectWithStatus("price_pro", "past_due"))))

	if store.callCount() != 0 {
		t.Errorf("expected no store calls, got %d", store.callCount())
	}
}

// TestHandleSubscriptionUpdated_TerminalStatusStoreErrorIsTransient checks the
// interaction with the retry semantics: a status-driven downgrade that fails to
// write is still worth retrying.
func TestHandleSubscriptionUpdated_TerminalStatusStoreErrorIsTransient(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}
	store.updateBillingErr = errStore

	wantRetry(t, h.handleSubscriptionUpdated(context.Background(),
		objectJSON(t, subscriptionObjectWithStatus("price_pro", "canceled"))))
}

// TestHandleSubscriptionUpdated_NoItems pins that a subscription carrying no
// line items maps to the free tier, since ParseSubscription yields an empty
// price ID and GetTierFromPriceID defaults to free.
func TestHandleSubscriptionUpdated_NoItems(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierStudio}

	sub := map[string]any{
		"id":       "sub_123",
		"customer": "cus_123",
		"status":   "active",
		"items":    map[string]any{"data": []map[string]any{}},
	}
	mustHandle(t, h.handleSubscriptionUpdated(context.Background(), objectJSON(t, sub)))

	if len(store.updateBillingCalls) != 1 {
		t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
	}
	if got := store.updateBillingCalls[0].Tier; got != TierFree {
		t.Errorf("tier = %q, want %q", got, TierFree)
	}
}

func TestHandleSubscriptionUpdated_TenantNotFound(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = nil // store reports "no such customer" as a nil tenant

	mustHandle(t, h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject("price_pro"))))

	if len(store.byCustomerCalls) != 1 {
		t.Errorf("GetByStripeCustomerID calls = %d, want 1", len(store.byCustomerCalls))
	}
	if len(store.updateBillingCalls) != 0 {
		t.Errorf("expected no UpdateBilling for an unknown customer, got %d", len(store.updateBillingCalls))
	}
}

func TestHandleSubscriptionUpdated_StoreErrors(t *testing.T) {
	t.Run("lookup fails", func(t *testing.T) {
		h, store := newTestHandler()
		store.byCustomerErr = errStore

		wantRetry(t, h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject("price_pro"))))

		if len(store.updateBillingCalls) != 0 {
			t.Errorf("expected no UpdateBilling after a failed lookup, got %d", len(store.updateBillingCalls))
		}
	})

	t.Run("update fails", func(t *testing.T) {
		h, store := newTestHandler()
		store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}
		store.updateBillingErr = errStore

		wantRetry(t, h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject("price_pro"))))

		if len(store.updateBillingCalls) != 1 {
			t.Errorf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
		}
	})
}

func TestHandleSubscriptionUpdated_UnparseableData(t *testing.T) {
	h, store := newTestHandler()

	mustHandle(t, h.handleSubscriptionUpdated(context.Background(), []byte(`{"items": "not-the-expected-shape"}`)))

	if store.callCount() != 0 {
		t.Errorf("expected no store calls for unparseable data, got %d", store.callCount())
	}
}

// --- handleSubscriptionDeleted ---

func TestHandleSubscriptionDeleted_DowngradesToFree(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierStudio}

	mustHandle(t, h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_studio"))))

	if len(store.updateBillingCalls) != 1 {
		t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
	}
	got := store.updateBillingCalls[0]
	want := updateBillingCall{
		TenantID:       "tenant-1",
		CustomerID:     "cus_123",
		SubscriptionID: "", // the subscription reference is cleared
		Tier:           TierFree,
	}
	if got != want {
		t.Errorf("UpdateBilling = %+v, want %+v", got, want)
	}
}

// TestHandleSubscriptionDeleted_IgnoresPriceID pins that deletion always lands
// on the free tier regardless of which price the subscription carried.
func TestHandleSubscriptionDeleted_IgnoresPriceID(t *testing.T) {
	for _, priceID := range []string{"price_starter", "price_pro", "price_studio", "price_unknown", ""} {
		t.Run("price="+priceID, func(t *testing.T) {
			h, store := newTestHandler()
			store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}

			mustHandle(t, h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject(priceID))))

			if len(store.updateBillingCalls) != 1 {
				t.Fatalf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
			}
			if got := store.updateBillingCalls[0].Tier; got != TierFree {
				t.Errorf("tier = %q, want %q", got, TierFree)
			}
		})
	}
}

func TestHandleSubscriptionDeleted_TenantNotFound(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = nil

	mustHandle(t, h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_pro"))))

	if len(store.byCustomerCalls) != 1 {
		t.Errorf("GetByStripeCustomerID calls = %d, want 1", len(store.byCustomerCalls))
	}
	if len(store.updateBillingCalls) != 0 {
		t.Errorf("expected no UpdateBilling for an unknown customer, got %d", len(store.updateBillingCalls))
	}
}

func TestHandleSubscriptionDeleted_StoreErrors(t *testing.T) {
	t.Run("lookup fails", func(t *testing.T) {
		h, store := newTestHandler()
		store.byCustomerErr = errStore

		wantRetry(t, h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_pro"))))

		if len(store.updateBillingCalls) != 0 {
			t.Errorf("expected no UpdateBilling after a failed lookup, got %d", len(store.updateBillingCalls))
		}
	})

	t.Run("update fails", func(t *testing.T) {
		h, store := newTestHandler()
		store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}
		store.updateBillingErr = errStore

		// A failed downgrade must be retried, otherwise the tenant keeps a paid
		// tier it no longer pays for.
		wantRetry(t, h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_pro"))))

		if len(store.updateBillingCalls) != 1 {
			t.Errorf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
		}
	})
}

// --- handleInvoicePaymentFailed ---

// TestHandleInvoicePaymentFailed pins that a failed payment only logs: the
// tenant is left alone whether or not the invoice parses.
func TestHandleInvoicePaymentFailed(t *testing.T) {
	t.Run("valid invoice", func(t *testing.T) {
		h, store := newTestHandler()
		h.handleInvoicePaymentFailed(objectJSON(t, map[string]any{
			"customer": "cus_123", "customer_email": "user@example.com",
			"amount_due": 1500, "currency": "usd",
		}))
		if store.callCount() != 0 {
			t.Errorf("expected no store calls, got %d", store.callCount())
		}
	})

	t.Run("unparseable invoice", func(t *testing.T) {
		h, store := newTestHandler()
		h.handleInvoicePaymentFailed([]byte(`{"amount_due": "not-a-number"}`))
		if store.callCount() != 0 {
			t.Errorf("expected no store calls, got %d", store.callCount())
		}
	})
}

func TestHandleSubscriptionDeleted_UnparseableData(t *testing.T) {
	h, store := newTestHandler()

	mustHandle(t, h.handleSubscriptionDeleted(context.Background(), []byte(`not json at all`)))

	if store.callCount() != 0 {
		t.Errorf("expected no store calls for unparseable data, got %d", store.callCount())
	}
}
