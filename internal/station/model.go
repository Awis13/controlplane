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
	StreamURL   string    `json:"stream_url"`
	OwnerID     *string   `json:"owner_id,omitempty"`
	TenantID    *string   `json:"tenant_id,omitempty"`
	IsPublic    bool      `json:"is_public"`
	IsOnline    bool      `json:"is_online"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Live fields — populated from poller cache, not stored in DB.
	ListenersCount int     `json:"listeners_count,omitempty"`
	NowPlaying     string  `json:"now_playing,omitempty"`
	BPM            float64 `json:"bpm,omitempty"`
}

// ListPublicParams holds query parameters for listing public stations.
type ListPublicParams struct {
	Query  string // search term (ILIKE on name, genre, description)
	Genre  string // exact genre match
	Sort   string // "name", "listeners", "online_first", "newest"
	Limit  int
	Offset int
}

type CreateStationRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Genre       string  `json:"genre"`
	Description string  `json:"description"`
	ArtworkURL  string  `json:"artwork_url"`
	StreamURL   string  `json:"stream_url"`
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
	StreamURL   *string `json:"stream_url,omitempty"`
	IsPublic    *bool   `json:"is_public,omitempty"`
	IsOnline    *bool   `json:"is_online,omitempty"`
}
