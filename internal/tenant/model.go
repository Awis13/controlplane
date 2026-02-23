package tenant

import (
	"time"
)

// Project represents an LXC project type (e.g. studio23).
type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	TemplateID    int       `json:"template_id"`
	Ports         []int     `json:"ports"`
	StripePriceID *string   `json:"stripe_price_id,omitempty"`
	HealthPath    string    `json:"health_path"`
	RAMMB         int       `json:"ram_mb"`
	CreatedAt     time.Time `json:"created_at"`
}

type CreateProjectRequest struct {
	Name          string  `json:"name"`
	TemplateID    int     `json:"template_id"`
	Ports         []int   `json:"ports"`
	StripePriceID *string `json:"stripe_price_id,omitempty"`
	HealthPath    string  `json:"health_path"`
	RAMMB         int     `json:"ram_mb"`
}

// Tenant represents an individual customer instance.
type Tenant struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	ProjectID            string    `json:"project_id"`
	NodeID               string    `json:"node_id"`
	LXCID                *int      `json:"lxc_id,omitempty"`
	Subdomain            string    `json:"subdomain"`
	Status               string    `json:"status"`
	StripeSubscriptionID *string   `json:"stripe_subscription_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type CreateTenantRequest struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
	NodeID    string `json:"node_id"`
	Subdomain string `json:"subdomain"`
}
