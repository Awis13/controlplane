package audit

import (
	"encoding/json"
	"time"
)

// Entry represents a single audit log entry.
type Entry struct {
	ID         string          `json:"id"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Details    json.RawMessage `json:"details,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}
