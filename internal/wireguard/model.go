package wireguard

import "time"

// Peer represents a WireGuard peer in the mesh network.
type Peer struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	PublicKey             string    `json:"public_key"`
	PresharedKeyEncrypted *string   `json:"-"` // encrypted preshared key, not exposed in JSON
	WgIP                  string    `json:"wg_ip"`
	AllowedIPs            string    `json:"allowed_ips"`
	Endpoint              *string   `json:"endpoint,omitempty"`
	Type                  string    `json:"type"` // admin, node, user
	TenantID              *string   `json:"tenant_id,omitempty"`
	Enabled               bool      `json:"enabled"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// CreatePeerRequest is a request to create a new peer.
type CreatePeerRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`        // admin, node, user
	Endpoint   string `json:"endpoint"`    // optional
	TenantID   string `json:"tenant_id"`   // optional, for user type
	AllowedIPs string `json:"allowed_ips"` // optional, override
}

// UpdatePeerRequest is a request to update a peer. Nil fields are not updated.
type UpdatePeerRequest struct {
	Name     *string `json:"name,omitempty"`
	Endpoint *string `json:"endpoint,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}

// ValidPeerTypes lists the valid peer types.
var ValidPeerTypes = map[string]bool{
	"admin": true,
	"node":  true,
	"user":  true,
}
