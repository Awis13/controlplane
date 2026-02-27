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
	UpdatedAt     time.Time `json:"updated_at"`
}

type CreateProjectRequest struct {
	Name          string  `json:"name"`
	TemplateID    int     `json:"template_id"`
	Ports         []int   `json:"ports"`
	StripePriceID *string `json:"stripe_price_id,omitempty"`
	HealthPath    string  `json:"health_path"`
	RAMMB         int     `json:"ram_mb"`
}

type UpdateProjectRequest struct {
	Name          *string `json:"name,omitempty"`
	TemplateID    *int    `json:"template_id,omitempty"`
	Ports         *[]int  `json:"ports,omitempty"`
	StripePriceID *string `json:"stripe_price_id,omitempty"`
	HealthPath    *string `json:"health_path,omitempty"`
	RAMMB         *int    `json:"ram_mb,omitempty"`
}
