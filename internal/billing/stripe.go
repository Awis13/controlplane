package billing

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/stripe/stripe-go/v82"
	billingSession "github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutSession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/webhook"
)

// Event types we handle from Stripe webhooks.
const (
	EventCheckoutCompleted     = "checkout.session.completed"
	EventSubscriptionUpdated   = "customer.subscription.updated"
	EventSubscriptionDeleted   = "customer.subscription.deleted"
	EventInvoicePaymentFailed  = "invoice.payment_failed"
)

// Service handles Stripe billing operations.
type Service struct {
	webhookSecret string
	prices        map[string]string // tier name -> Stripe price ID
	priceToTier   map[string]string // Stripe price ID -> tier name
}

// NewService creates a new billing service.
// Sets the global Stripe API key and builds price-to-tier mappings.
func NewService(secretKey, webhookSecret string, prices map[string]string) *Service {
	stripe.Key = secretKey

	// Build reverse mapping: price ID -> tier name
	priceToTier := make(map[string]string, len(prices))
	for tier, priceID := range prices {
		if priceID != "" {
			priceToTier[priceID] = tier
		}
	}

	return &Service{
		webhookSecret: webhookSecret,
		prices:        prices,
		priceToTier:   priceToTier,
	}
}

// CreateCheckoutSession creates a Stripe Checkout session for a subscription.
func (s *Service) CreateCheckoutSession(customerEmail, priceID, successURL, cancelURL string, metadata map[string]string) (string, error) {
	params := &stripe.CheckoutSessionParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL:    stripe.String(successURL),
		CancelURL:     stripe.String(cancelURL),
		CustomerEmail: stripe.String(customerEmail),
	}
	if len(metadata) > 0 {
		params.Metadata = metadata
	}

	session, err := checkoutSession.New(params)
	if err != nil {
		return "", fmt.Errorf("create checkout session: %w", err)
	}

	return session.URL, nil
}

// CreateCustomerPortalSession creates a Stripe Billing Portal session.
func (s *Service) CreateCustomerPortalSession(customerID, returnURL string) (string, error) {
	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	}

	session, err := billingSession.New(params)
	if err != nil {
		return "", fmt.Errorf("create portal session: %w", err)
	}

	return session.URL, nil
}

// WebhookEvent represents a parsed and verified Stripe webhook event.
type WebhookEvent struct {
	Type string
	Data json.RawMessage
}

// VerifyWebhook verifies the webhook signature and returns the parsed event.
func (s *Service) VerifyWebhook(payload []byte, signature string) (*stripe.Event, error) {
	event, err := webhook.ConstructEvent(payload, signature, s.webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("verify webhook signature: %w", err)
	}
	return &event, nil
}

// CheckoutSessionData holds the fields we extract from checkout.session.completed.
type CheckoutSessionData struct {
	CustomerID     string
	SubscriptionID string
	CustomerEmail  string
	Metadata       map[string]string
}

// ParseCheckoutSession extracts relevant fields from a checkout.session.completed event.
func ParseCheckoutSession(data json.RawMessage) (*CheckoutSessionData, error) {
	var session struct {
		Customer     string            `json:"customer"`
		Subscription string            `json:"subscription"`
		CustomerDetails struct {
			Email string `json:"email"`
		} `json:"customer_details"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse checkout session: %w", err)
	}
	return &CheckoutSessionData{
		CustomerID:     session.Customer,
		SubscriptionID: session.Subscription,
		CustomerEmail:  session.CustomerDetails.Email,
		Metadata:       session.Metadata,
	}, nil
}

// SubscriptionData holds fields from subscription updated/deleted events.
type SubscriptionData struct {
	ID         string
	CustomerID string
	PriceID    string
	Status     string
}

// ParseSubscription extracts relevant fields from subscription events.
func ParseSubscription(data json.RawMessage) (*SubscriptionData, error) {
	var sub struct {
		ID       string `json:"id"`
		Customer string `json:"customer"`
		Status   string `json:"status"`
		Items    struct {
			Data []struct {
				Price struct {
					ID string `json:"id"`
				} `json:"price"`
			} `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &sub); err != nil {
		return nil, fmt.Errorf("parse subscription: %w", err)
	}

	priceID := ""
	if len(sub.Items.Data) > 0 {
		priceID = sub.Items.Data[0].Price.ID
	}

	return &SubscriptionData{
		ID:         sub.ID,
		CustomerID: sub.Customer,
		PriceID:    priceID,
		Status:     sub.Status,
	}, nil
}

// InvoiceData holds fields from invoice events.
type InvoiceData struct {
	CustomerID string
	CustomerEmail string
	AmountDue    int64
	Currency     string
}

// ParseInvoice extracts relevant fields from invoice events.
func ParseInvoice(data json.RawMessage) (*InvoiceData, error) {
	var inv struct {
		Customer      string `json:"customer"`
		CustomerEmail string `json:"customer_email"`
		AmountDue     int64  `json:"amount_due"`
		Currency      string `json:"currency"`
	}
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("parse invoice: %w", err)
	}
	return &InvoiceData{
		CustomerID:    inv.Customer,
		CustomerEmail: inv.CustomerEmail,
		AmountDue:     inv.AmountDue,
		Currency:      inv.Currency,
	}, nil
}

// GetTierFromPriceID maps a Stripe price ID to a tier name.
// Returns "free" if the price ID is unknown.
func (s *Service) GetTierFromPriceID(priceID string) string {
	if tier, ok := s.priceToTier[priceID]; ok {
		return tier
	}
	slog.Warn("billing: unknown price ID, defaulting to free", "price_id", priceID)
	return TierFree
}

// GetPriceID returns the Stripe price ID for a given tier.
// Returns empty string if tier has no associated price.
func (s *Service) GetPriceID(tier string) string {
	return s.prices[tier]
}
