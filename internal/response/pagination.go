package response

import (
	"net/http"
	"strconv"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// ListParams holds pagination and filtering parameters.
type ListParams struct {
	Limit  int
	Offset int

	// Filters (used by tenants, extensible)
	Status    string
	NodeID    string
	ProjectID string
}

// ListResult holds a paginated list with total count.
type ListResult[T any] struct {
	Items      []T `json:"items"`
	Total      int `json:"total"`
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
	HasMore    bool `json:"has_more"`
}

// ParseListParams parses pagination query parameters from the request.
func ParseListParams(r *http.Request) ListParams {
	p := ListParams{
		Limit:  DefaultLimit,
		Offset: 0,
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Limit = n
		}
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}

	p.Status = r.URL.Query().Get("status")
	p.NodeID = r.URL.Query().Get("node_id")
	p.ProjectID = r.URL.Query().Get("project_id")

	return p
}
