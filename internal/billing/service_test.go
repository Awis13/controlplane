package billing

import (
	"encoding/json"
	"testing"
)

// --- Tier and price mapping ---

// TestTierPriceRoundTrip walks the configured map in both directions.
func TestTierPriceRoundTrip(t *testing.T) {
	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())

	for tier, priceID := range testPrices() {
		t.Run(tier, func(t *testing.T) {
			if got := svc.GetPriceID(tier); got != priceID {
				t.Errorf("GetPriceID(%q) = %q, want %q", tier, got, priceID)
			}
			if got := svc.GetTierFromPriceID(priceID); got != tier {
				t.Errorf("GetTierFromPriceID(%q) = %q, want %q", priceID, got, tier)
			}
		})
	}
}

func TestGetPriceID_UnconfiguredTier(t *testing.T) {
	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())

	for _, tier := range []string{TierFree, "enterprise", ""} {
		if got := svc.GetPriceID(tier); got != "" {
			t.Errorf("GetPriceID(%q) = %q, want an empty string", tier, got)
		}
	}
}

func TestGetTierFromPriceID_UnknownFallsBackToFree(t *testing.T) {
	svc := NewService("sk_test_unused", testWebhookSecret, testPrices())

	for _, priceID := range []string{"price_retired", "", "PRICE_PRO"} {
		if got := svc.GetTierFromPriceID(priceID); got != TierFree {
			t.Errorf("GetTierFromPriceID(%q) = %q, want %q", priceID, got, TierFree)
		}
	}
}

// TestNewService_SkipsEmptyPriceIDs pins that tiers configured with an empty
// price are left out of the reverse map, so they cannot all collide on "".
func TestNewService_SkipsEmptyPriceIDs(t *testing.T) {
	svc := NewService("sk_test_unused", testWebhookSecret, map[string]string{
		TierStarter: "",
		TierPro:     "price_pro",
		TierStudio:  "",
	})

	if got := svc.GetTierFromPriceID(""); got != TierFree {
		t.Errorf("GetTierFromPriceID(\"\") = %q, want %q", got, TierFree)
	}
	if got := svc.GetTierFromPriceID("price_pro"); got != TierPro {
		t.Errorf("GetTierFromPriceID(\"price_pro\") = %q, want %q", got, TierPro)
	}
	if got := svc.GetPriceID(TierStarter); got != "" {
		t.Errorf("GetPriceID(starter) = %q, want an empty string", got)
	}
}

// --- Tier helpers ---

func TestTierPredicates(t *testing.T) {
	tests := []struct {
		tier      string
		wantValid bool
		wantPaid  bool
	}{
		{tier: TierFree, wantValid: true, wantPaid: false},
		{tier: TierStarter, wantValid: true, wantPaid: true},
		{tier: TierPro, wantValid: true, wantPaid: true},
		{tier: TierStudio, wantValid: true, wantPaid: true},
		{tier: "enterprise", wantValid: false, wantPaid: false},
		{tier: "", wantValid: false, wantPaid: false},
		{tier: "PRO", wantValid: false, wantPaid: false},
	}

	for _, tt := range tests {
		t.Run("tier="+tt.tier, func(t *testing.T) {
			if got := IsValidTier(tt.tier); got != tt.wantValid {
				t.Errorf("IsValidTier(%q) = %v, want %v", tt.tier, got, tt.wantValid)
			}
			if got := IsPaidTier(tt.tier); got != tt.wantPaid {
				t.Errorf("IsPaidTier(%q) = %v, want %v", tt.tier, got, tt.wantPaid)
			}
		})
	}
}

func TestGetLimits_UnknownTierFallsBackToFree(t *testing.T) {
	free := GetLimits(TierFree)

	for _, tier := range []string{"enterprise", "", "PRO"} {
		if got := GetLimits(tier); got != free {
			t.Errorf("GetLimits(%q) = %+v, want the free limits %+v", tier, got, free)
		}
	}
}

// TestGetLimits_TiersAreDistinct guards against the limits table being wired up
// so that paid tiers silently share the free tier's limits.
func TestGetLimits_TiersAreDistinct(t *testing.T) {
	free := GetLimits(TierFree)
	studio := GetLimits(TierStudio)

	if studio == free {
		t.Error("studio limits are identical to free limits")
	}
	if !free.Watermark {
		t.Error("expected the free tier to be watermarked")
	}
	if GetLimits(TierStarter).Watermark {
		t.Error("expected paid tiers to drop the watermark")
	}
}

// --- Event payload parsing ---

func TestParseCheckoutSession(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"customer":         "cus_123",
		"subscription":     "sub_123",
		"customer_details": map[string]any{"email": "user@example.com"},
		"metadata":         map[string]string{"user_id": "owner-1", "tier": TierPro},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := ParseCheckoutSession(data)
	if err != nil {
		t.Fatalf("ParseCheckoutSession: %v", err)
	}
	if got.CustomerID != "cus_123" || got.SubscriptionID != "sub_123" {
		t.Errorf("ids = %q/%q, want cus_123/sub_123", got.CustomerID, got.SubscriptionID)
	}
	if got.CustomerEmail != "user@example.com" {
		t.Errorf("email = %q", got.CustomerEmail)
	}
	if got.Metadata["tier"] != TierPro {
		t.Errorf("metadata tier = %q", got.Metadata["tier"])
	}
}

func TestParseCheckoutSession_MissingFieldsAreZero(t *testing.T) {
	got, err := ParseCheckoutSession([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseCheckoutSession: %v", err)
	}
	if got.CustomerID != "" || got.SubscriptionID != "" || got.CustomerEmail != "" {
		t.Errorf("expected zero values, got %+v", got)
	}
	if got.Metadata != nil {
		t.Errorf("metadata = %v, want nil", got.Metadata)
	}
}

func TestParseSubscription_UsesFirstLineItem(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"id":       "sub_123",
		"customer": "cus_123",
		"status":   "active",
		"items": map[string]any{
			"data": []map[string]any{
				{"price": map[string]any{"id": "price_pro"}},
				{"price": map[string]any{"id": "price_studio"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := ParseSubscription(data)
	if err != nil {
		t.Fatalf("ParseSubscription: %v", err)
	}
	if got.PriceID != "price_pro" {
		t.Errorf("PriceID = %q, want the first line item price_pro", got.PriceID)
	}
	if got.ID != "sub_123" || got.CustomerID != "cus_123" || got.Status != "active" {
		t.Errorf("unexpected fields: %+v", got)
	}
}

func TestParseSubscription_NoItemsGivesEmptyPrice(t *testing.T) {
	got, err := ParseSubscription([]byte(`{"id":"sub_1","customer":"cus_1","items":{"data":[]}}`))
	if err != nil {
		t.Fatalf("ParseSubscription: %v", err)
	}
	if got.PriceID != "" {
		t.Errorf("PriceID = %q, want an empty string", got.PriceID)
	}
}

func TestParseInvoice(t *testing.T) {
	got, err := ParseInvoice([]byte(`{"customer":"cus_1","customer_email":"user@example.com","amount_due":1500,"currency":"usd"}`))
	if err != nil {
		t.Fatalf("ParseInvoice: %v", err)
	}
	if got.CustomerID != "cus_1" || got.CustomerEmail != "user@example.com" {
		t.Errorf("unexpected customer fields: %+v", got)
	}
	if got.AmountDue != 1500 || got.Currency != "usd" {
		t.Errorf("amount = %d %q, want 1500 usd", got.AmountDue, got.Currency)
	}
}

func TestParsersRejectMalformedJSON(t *testing.T) {
	bad := []byte(`{"customer": `)

	if _, err := ParseCheckoutSession(bad); err == nil {
		t.Error("ParseCheckoutSession: expected an error")
	}
	if _, err := ParseSubscription(bad); err == nil {
		t.Error("ParseSubscription: expected an error")
	}
	if _, err := ParseInvoice(bad); err == nil {
		t.Error("ParseInvoice: expected an error")
	}
}
