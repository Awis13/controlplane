package tenant

import (
	"time"
)

// Tenant represents an individual customer instance.
type Tenant struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	ProjectID            string     `json:"project_id"`
	NodeID               string     `json:"node_id"`
	LXCID                *int       `json:"lxc_id,omitempty"`
	LXCIP                *string    `json:"lxc_ip,omitempty"`
	Subdomain            string     `json:"subdomain"`
	Status               string     `json:"status"`
	ErrorMessage         *string    `json:"error_message,omitempty"`
	OwnerID              *string    `json:"owner_id,omitempty"`
	StripeSubscriptionID *string    `json:"stripe_subscription_id,omitempty"`
	StripeCustomerID     *string    `json:"stripe_customer_id,omitempty"`
	Tier                 string     `json:"tier"`
	DashboardToken       *string    `json:"-"`
	HealthStatus         string     `json:"health_status"`
	HealthCheckedAt      *time.Time `json:"health_checked_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type CreateTenantRequest struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
	NodeID    string `json:"node_id"`
	Subdomain string `json:"subdomain"`
}

type UpdateTenantRequest struct {
	Name                 *string `json:"name,omitempty"`
	StripeSubscriptionID *string `json:"stripe_subscription_id,omitempty"`
	StripeCustomerID     *string `json:"stripe_customer_id,omitempty"`
}
