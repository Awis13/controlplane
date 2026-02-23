package project

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
