package wireguard

import (
	"net"
	"testing"
	"time"
)

func TestScanPeerColumns(t *testing.T) {
	// Check that peerColumns count matches Peer fields
	cols := peerColumns
	count := 0
	for _, c := range cols {
		if c == ',' {
			count++
		}
	}
	count++ // n commas = n+1 columns

	expected := 12 // id, name, public_key, preshared_key_encrypted, wg_ip, allowed_ips, endpoint, type, tenant_id, enabled, created_at, updated_at
	if count != expected {
		t.Errorf("peerColumns has %d columns, want %d", count, expected)
	}
}

func TestPeerModel(t *testing.T) {
	now := time.Now()
	endpoint := "1.2.3.4:51820"
	tenantID := "test-tenant-id"
	psk := "encrypted-psk"

	peer := Peer{
		ID:                    "test-id",
		Name:                  "test-peer",
		PublicKey:             "test-pub-key",
		PresharedKeyEncrypted: &psk,
		WgIP:                  "10.10.0.5",
		AllowedIPs:            "10.10.0.5/32",
		Endpoint:              &endpoint,
		Type:                  "admin",
		TenantID:              &tenantID,
		Enabled:               true,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	if peer.ID != "test-id" {
		t.Errorf("ID = %q, want test-id", peer.ID)
	}
	if peer.Name != "test-peer" {
		t.Errorf("Name = %q, want test-peer", peer.Name)
	}
	if *peer.PresharedKeyEncrypted != "encrypted-psk" {
		t.Errorf("PresharedKeyEncrypted = %q, want encrypted-psk", *peer.PresharedKeyEncrypted)
	}
	if *peer.Endpoint != "1.2.3.4:51820" {
		t.Errorf("Endpoint = %q, want 1.2.3.4:51820", *peer.Endpoint)
	}
	if *peer.TenantID != "test-tenant-id" {
		t.Errorf("TenantID = %q, want test-tenant-id", *peer.TenantID)
	}
}

func TestPeerModelNilFields(t *testing.T) {
	peer := Peer{
		ID:         "test-id",
		Name:       "test-peer",
		PublicKey:  "test-pub-key",
		WgIP:       "10.10.0.5",
		AllowedIPs: "10.10.0.5/32",
		Type:       "user",
		Enabled:    true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if peer.PresharedKeyEncrypted != nil {
		t.Error("PresharedKeyEncrypted should be nil")
	}
	if peer.Endpoint != nil {
		t.Error("Endpoint should be nil")
	}
	if peer.TenantID != nil {
		t.Error("TenantID should be nil")
	}
}

func TestCreatePeerRequest(t *testing.T) {
	req := CreatePeerRequest{
		Name:       "my-laptop",
		Type:       "user",
		Endpoint:   "5.6.7.8:51820",
		TenantID:   "some-tenant",
		AllowedIPs: "10.10.0.0/24",
	}

	if req.Name != "my-laptop" {
		t.Errorf("Name = %q, want my-laptop", req.Name)
	}
	if req.Type != "user" {
		t.Errorf("Type = %q, want user", req.Type)
	}
	if req.Endpoint != "5.6.7.8:51820" {
		t.Errorf("Endpoint = %q, want 5.6.7.8:51820", req.Endpoint)
	}
}

func TestUpdatePeerRequest(t *testing.T) {
	name := "new-name"
	endpoint := "9.8.7.6:51820"
	enabled := false

	req := UpdatePeerRequest{
		Name:     &name,
		Endpoint: &endpoint,
		Enabled:  &enabled,
	}

	if *req.Name != "new-name" {
		t.Errorf("Name = %q, want new-name", *req.Name)
	}
	if *req.Endpoint != "9.8.7.6:51820" {
		t.Errorf("Endpoint = %q, want 9.8.7.6:51820", *req.Endpoint)
	}
	if *req.Enabled != false {
		t.Error("Enabled should be false")
	}
}

func TestUpdatePeerRequestNilFields(t *testing.T) {
	req := UpdatePeerRequest{}

	if req.Name != nil {
		t.Error("Name should be nil")
	}
	if req.Endpoint != nil {
		t.Error("Endpoint should be nil")
	}
	if req.Enabled != nil {
		t.Error("Enabled should be nil")
	}
}

func TestGetNextAvailableIPSubnetParsing(t *testing.T) {
	// Check subnet parsing logic (no DB)
	tests := []struct {
		subnet string
		valid  bool
	}{
		{"10.10.0.0/24", true},
		{"192.168.1.0/24", true},
		{"172.16.0.0/16", true},
		{"not-a-subnet", false},
		{"", false},
	}

	for _, tt := range tests {
		_, _, err := net.ParseCIDR(tt.subnet)
		if tt.valid && err != nil {
			t.Errorf("subnet %q should be valid, got error: %v", tt.subnet, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("subnet %q should be invalid", tt.subnet)
		}
	}
}
