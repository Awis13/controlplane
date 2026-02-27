package station

import (
	"time"
)

// Station represents a radio station with metadata.
type Station struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Genre       string    `json:"genre"`
	Description string    `json:"description"`
	ArtworkURL  string    `json:"artwork_url"`
	OwnerID     *string   `json:"owner_id,omitempty"`
	TenantID    *string   `json:"tenant_id,omitempty"`
	IsPublic    bool      `json:"is_public"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateStationRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Genre       string  `json:"genre"`
	Description string  `json:"description"`
	ArtworkURL  string  `json:"artwork_url"`
	OwnerID     *string `json:"owner_id,omitempty"`
	TenantID    *string `json:"tenant_id,omitempty"`
	IsPublic    bool    `json:"is_public"`
}

type UpdateStationRequest struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Genre       *string `json:"genre,omitempty"`
	Description *string `json:"description,omitempty"`
	ArtworkURL  *string `json:"artwork_url,omitempty"`
	IsPublic    *bool   `json:"is_public,omitempty"`
}
