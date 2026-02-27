package wireguard

import "time"

// Peer представляет WireGuard пир в mesh-сети.
type Peer struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	PublicKey             string    `json:"public_key"`
	PresharedKeyEncrypted *string   `json:"-"` // зашифрованный preshared key, не отдаём в JSON
	WgIP                  string    `json:"wg_ip"`
	AllowedIPs            string    `json:"allowed_ips"`
	Endpoint              *string   `json:"endpoint,omitempty"`
	Type                  string    `json:"type"` // admin, node, user
	TenantID              *string   `json:"tenant_id,omitempty"`
	Enabled               bool      `json:"enabled"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// CreatePeerRequest — запрос на создание нового пира.
type CreatePeerRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`        // admin, node, user
	Endpoint   string `json:"endpoint"`    // необязательно
	TenantID   string `json:"tenant_id"`   // необязательно, для user type
	AllowedIPs string `json:"allowed_ips"` // необязательно, override
}

// UpdatePeerRequest — запрос на обновление пира. Nil поля не обновляются.
type UpdatePeerRequest struct {
	Name     *string `json:"name,omitempty"`
	Endpoint *string `json:"endpoint,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}

// ValidPeerTypes — допустимые типы пиров.
var ValidPeerTypes = map[string]bool{
	"admin": true,
	"node":  true,
	"user":  true,
}
