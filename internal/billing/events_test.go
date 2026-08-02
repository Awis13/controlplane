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

// --- handleCheckoutCompleted ---

func TestHandleCheckoutCompleted_HappyPath(t *testing.T) {
	h, store := newTestHandler()
	store.tenantsByOwner = []TenantBilling{
		{ID: "tenant-1", Name: "first", Tier: TierFree},
		{ID: "tenant-2", Name: "second", Tier: TierFree},
	}

	h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject()))

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
			h.handleCheckoutCompleted(context.Background(), objectJSON(t, session))

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
	h.handleCheckoutCompleted(context.Background(), objectJSON(t, session))

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

	h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject()))

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

		h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject()))

		if len(store.updateBillingCalls) != 0 {
			t.Errorf("expected no UpdateBilling after a failed lookup, got %d", len(store.updateBillingCalls))
		}
	})

	t.Run("update fails and is swallowed", func(t *testing.T) {
		h, store := newTestHandler()
		store.tenantsByOwner = []TenantBilling{{ID: "tenant-1", Tier: TierFree}}
		store.updateBillingErr = errStore

		// The handler logs and returns; the webhook is still acknowledged, so a
		// failed write is silently lost.
		h.handleCheckoutCompleted(context.Background(), objectJSON(t, checkoutSessionObject()))

		if len(store.updateBillingCalls) != 1 {
			t.Errorf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
		}
	})
}

func TestHandleCheckoutCompleted_UnparseableData(t *testing.T) {
	h, store := newTestHandler()

	h.handleCheckoutCompleted(context.Background(), []byte(`{"metadata": "not-a-map"}`))

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

			h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject(tt.priceID)))

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
	h.handleSubscriptionUpdated(context.Background(), objectJSON(t, sub))

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

	h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject("price_pro")))

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

		h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject("price_pro")))

		if len(store.updateBillingCalls) != 0 {
			t.Errorf("expected no UpdateBilling after a failed lookup, got %d", len(store.updateBillingCalls))
		}
	})

	t.Run("update fails and is swallowed", func(t *testing.T) {
		h, store := newTestHandler()
		store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierFree}
		store.updateBillingErr = errStore

		h.handleSubscriptionUpdated(context.Background(), objectJSON(t, subscriptionObject("price_pro")))

		if len(store.updateBillingCalls) != 1 {
			t.Errorf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
		}
	})
}

func TestHandleSubscriptionUpdated_UnparseableData(t *testing.T) {
	h, store := newTestHandler()

	h.handleSubscriptionUpdated(context.Background(), []byte(`{"items": "not-the-expected-shape"}`))

	if store.callCount() != 0 {
		t.Errorf("expected no store calls for unparseable data, got %d", store.callCount())
	}
}

// --- handleSubscriptionDeleted ---

func TestHandleSubscriptionDeleted_DowngradesToFree(t *testing.T) {
	h, store := newTestHandler()
	store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierStudio}

	h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_studio")))

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

			h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject(priceID)))

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

	h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_pro")))

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

		h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_pro")))

		if len(store.updateBillingCalls) != 0 {
			t.Errorf("expected no UpdateBilling after a failed lookup, got %d", len(store.updateBillingCalls))
		}
	})

	t.Run("update fails and is swallowed", func(t *testing.T) {
		h, store := newTestHandler()
		store.tenantByCustomer = &TenantBilling{ID: "tenant-1", Tier: TierPro}
		store.updateBillingErr = errStore

		// A failed downgrade leaves the tenant on a paid tier with no retry.
		h.handleSubscriptionDeleted(context.Background(), objectJSON(t, subscriptionObject("price_pro")))

		if len(store.updateBillingCalls) != 1 {
			t.Errorf("UpdateBilling calls = %d, want 1", len(store.updateBillingCalls))
		}
	})
}

func TestHandleSubscriptionDeleted_UnparseableData(t *testing.T) {
	h, store := newTestHandler()

	h.handleSubscriptionDeleted(context.Background(), []byte(`not json at all`))

	if store.callCount() != 0 {
		t.Errorf("expected no store calls for unparseable data, got %d", store.callCount())
	}
}
