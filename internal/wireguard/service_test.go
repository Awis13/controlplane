package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// testEncryptionKey is a test encryption key (32 bytes hex).
var testEncryptionKey = hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func TestGenerateKeypair(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	privKey, pubKey, err := svc.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}

	if privKey == "" {
		t.Error("private key is empty")
	}
	if pubKey == "" {
		t.Error("public key is empty")
	}
	if privKey == pubKey {
		t.Error("private and public keys should not be equal")
	}

	// Check that keys are valid base64, 32 bytes long
	privBytes, err := base64.StdEncoding.DecodeString(privKey)
	if err != nil {
		t.Fatalf("private key is not valid base64: %v", err)
	}
	if len(privBytes) != 32 {
		t.Errorf("private key length = %d, want 32", len(privBytes))
	}

	pubBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		t.Fatalf("public key is not valid base64: %v", err)
	}
	if len(pubBytes) != 32 {
		t.Errorf("public key length = %d, want 32", len(pubBytes))
	}
}

func TestGenerateKeypairUniqueness(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	priv1, pub1, err := svc.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	priv2, pub2, err := svc.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}

	if priv1 == priv2 {
		t.Error("two generated private keys should not be equal")
	}
	if pub1 == pub2 {
		t.Error("two generated public keys should not be equal")
	}
}

func TestGeneratePresharedKey(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	psk, err := svc.GeneratePresharedKey()
	if err != nil {
		t.Fatalf("GeneratePresharedKey: %v", err)
	}

	if psk == "" {
		t.Error("preshared key is empty")
	}

	decoded, err := base64.StdEncoding.DecodeString(psk)
	if err != nil {
		t.Fatalf("PSK is not valid base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("PSK length = %d, want 32", len(decoded))
	}
}

func TestEncryptDecryptPSK(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	psk, err := svc.GeneratePresharedKey()
	if err != nil {
		t.Fatal(err)
	}

	encrypted, err := svc.EncryptPSK(psk)
	if err != nil {
		t.Fatalf("EncryptPSK: %v", err)
	}
	if encrypted == "" {
		t.Error("encrypted PSK is empty")
	}
	if encrypted == psk {
		t.Error("encrypted PSK should differ from plaintext")
	}

	decrypted, err := svc.DecryptPSK(encrypted)
	if err != nil {
		t.Fatalf("DecryptPSK: %v", err)
	}
	if decrypted != psk {
		t.Errorf("decrypted PSK = %q, want %q", decrypted, psk)
	}
}

func TestBuildPeerConfig(t *testing.T) {
	hubPubKey := "aGVsbG8gd29ybGQgdGhpcyBpcyBhIHRlc3Qga2V5IQ=="
	hubEndpoint := "203.0.113.1:51820"

	svc := NewService(nil, testEncryptionKey, hubPubKey, hubEndpoint, "10.10.0.0/24")

	peer := &Peer{
		WgIP:       "10.10.0.5",
		PublicKey:  "some-public-key",
		AllowedIPs: "10.10.0.5/32",
	}
	privateKey := "some-private-key"

	config := svc.BuildPeerConfig(peer, privateKey)

	// Check for all required sections
	if !strings.Contains(config, "[Interface]") {
		t.Error("config missing [Interface] section")
	}
	if !strings.Contains(config, "[Peer]") {
		t.Error("config missing [Peer] section")
	}
	if !strings.Contains(config, "Address = 10.10.0.5/24") {
		t.Error("config missing correct Address")
	}
	if !strings.Contains(config, "PrivateKey = some-private-key") {
		t.Error("config missing PrivateKey")
	}
	if !strings.Contains(config, "DNS = 10.10.0.1") {
		t.Error("config missing DNS")
	}
	if !strings.Contains(config, "PublicKey = "+hubPubKey) {
		t.Error("config missing hub PublicKey")
	}
	if !strings.Contains(config, "AllowedIPs = 10.10.0.0/24") {
		t.Error("config missing AllowedIPs")
	}
	if !strings.Contains(config, "Endpoint = "+hubEndpoint) {
		t.Error("config missing Endpoint")
	}
	if !strings.Contains(config, "PersistentKeepalive = 25") {
		t.Error("config missing PersistentKeepalive")
	}
}

func TestBuildPeerConfigWithPSK(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	// Encrypt PSK
	psk := "test-preshared-key-value"
	encrypted, err := svc.EncryptPSK(psk)
	if err != nil {
		t.Fatal(err)
	}

	peer := &Peer{
		WgIP:                  "10.10.0.5",
		PublicKey:             "some-public-key",
		AllowedIPs:            "10.10.0.5/32",
		PresharedKeyEncrypted: &encrypted,
	}

	config := svc.BuildPeerConfig(peer, "priv-key")

	if !strings.Contains(config, "PresharedKey = "+psk) {
		t.Error("config missing PresharedKey")
	}
}

func TestBuildPeerConfigNoPSK(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	peer := &Peer{
		WgIP:       "10.10.0.5",
		PublicKey:  "some-public-key",
		AllowedIPs: "10.10.0.5/32",
	}

	config := svc.BuildPeerConfig(peer, "priv-key")

	if strings.Contains(config, "PresharedKey") {
		t.Error("config should not contain PresharedKey when PSK is nil")
	}
}

func TestGenerateQRCode(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	config := "[Interface]\nAddress = 10.10.0.5/24\nPrivateKey = test\n"
	png, err := svc.GenerateQRCode(config)
	if err != nil {
		t.Fatalf("GenerateQRCode: %v", err)
	}

	if len(png) == 0 {
		t.Error("QR code PNG is empty")
	}

	// Check PNG signature
	pngSig := []byte{0x89, 0x50, 0x4e, 0x47}
	if len(png) < 4 || string(png[:4]) != string(pngSig) {
		t.Error("QR code is not a valid PNG")
	}
}

func TestDefaultNetworkCIDR(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "")

	if svc.NetworkCIDR() != "10.10.0.0/24" {
		t.Errorf("default NetworkCIDR = %q, want 10.10.0.0/24", svc.NetworkCIDR())
	}
}

func TestCustomNetworkCIDR(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "192.168.1.0/24")

	if svc.NetworkCIDR() != "192.168.1.0/24" {
		t.Errorf("custom NetworkCIDR = %q, want 192.168.1.0/24", svc.NetworkCIDR())
	}
}

func TestBuildPeerConfigUsesNetworkCIDR(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/16")

	peer := &Peer{
		WgIP:       "10.10.5.10",
		PublicKey:  "some-public-key",
		AllowedIPs: "10.10.5.10/32",
	}

	config := svc.BuildPeerConfig(peer, "priv-key")

	if !strings.Contains(config, "DNS = 10.10.0.1") {
		t.Error("config should derive DNS from networkCIDR base + 1")
	}
	if !strings.Contains(config, "AllowedIPs = 10.10.0.0/16") {
		t.Error("config should use networkCIDR for AllowedIPs")
	}
}

func TestHubPublicKey(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "test-hub-key", "1.2.3.4:51820", "10.10.0.0/24")

	if svc.HubPublicKey() != "test-hub-key" {
		t.Errorf("HubPublicKey = %q, want test-hub-key", svc.HubPublicKey())
	}
}

func TestHubEndpoint(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-key", "203.0.113.1:51820", "10.10.0.0/24")

	if svc.HubEndpoint() != "203.0.113.1:51820" {
		t.Errorf("HubEndpoint = %q, want 203.0.113.1:51820", svc.HubEndpoint())
	}
}

func TestValidPeerTypes(t *testing.T) {
	valid := []string{"admin", "node", "user"}
	for _, tp := range valid {
		if !ValidPeerTypes[tp] {
			t.Errorf("type %q should be valid", tp)
		}
	}

	invalid := []string{"", "superadmin", "guest", "root"}
	for _, tp := range invalid {
		if ValidPeerTypes[tp] {
			t.Errorf("type %q should not be valid", tp)
		}
	}
}

// --- Topology ---

// TestPeerConfig_TopologyDefaults pins the values the generated peer config
// carried before DNS and keepalive became settings.
func TestPeerConfig_TopologyDefaults(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24")

	config := svc.BuildPeerConfig(&Peer{Name: "p", WgIP: "10.10.0.5", PublicKey: "peer-key"}, "private-key")

	if !strings.Contains(config, "DNS = 10.10.0.1") {
		t.Errorf("config = %q, want the previous DNS literal", config)
	}
	if !strings.Contains(config, "PersistentKeepalive = 25") {
		t.Errorf("config = %q, want the previous keepalive literal", config)
	}
}

// TestPeerConfig_TopologyOverrides pins that configured values reach the file
// handed to the peer.
func TestPeerConfig_TopologyOverrides(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.20.0.0/24").
		WithDNSAddr("10.20.0.1").WithKeepalive(45)

	config := svc.BuildPeerConfig(&Peer{Name: "p", WgIP: "10.20.0.5", PublicKey: "peer-key"}, "private-key")

	if !strings.Contains(config, "DNS = 10.20.0.1") {
		t.Errorf("config = %q, want the configured DNS", config)
	}
	if strings.Contains(config, "DNS = 10.10.0.1") {
		t.Errorf("config = %q, still carries the default DNS", config)
	}
	if !strings.Contains(config, "PersistentKeepalive = 45") {
		t.Errorf("config = %q, want the configured keepalive", config)
	}
}

// TestServiceTopology_EmptyOverridesKeepDefaults pins that a partially
// configured deployment keeps the previous values for anything left unset.
func TestServiceTopology_EmptyOverridesKeepDefaults(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24").
		WithInterface("").WithDNSAddr("").WithKeepalive(0)

	if svc.iface != "wg0" || svc.dnsAddr != "10.10.0.1" || svc.keepalive != 25 {
		t.Errorf("empty overrides changed the defaults: %q %q %d", svc.iface, svc.dnsAddr, svc.keepalive)
	}
}

// TestServiceTopology_InterfaceIsConfigured pins the hub interface the peer
// commands are applied to.
func TestServiceTopology_InterfaceIsConfigured(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "10.10.0.0/24").WithInterface("wg1")

	if svc.iface != "wg1" {
		t.Errorf("iface = %q, want the configured interface", svc.iface)
	}
}

// --- Interface mask ---

// TestPeerConfig_AddressMaskFollowsTheAllocation pins that the mask a peer puts
// on its interface comes from the network it was allocated in. A peer on a /25
// advertising /24 claims half a network that is not its own.
func TestPeerConfig_AddressMaskFollowsTheAllocation(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wgIP    string
		want    string
		wantDNS string
	}{
		{
			name: "a /24 is unchanged from before", cidr: "10.10.0.0/24", wgIP: "10.10.0.5",
			want: "Address = 10.10.0.5/24", wantDNS: "DNS = 10.10.0.1",
		},
		{
			name: "a /25 follows the allocation", cidr: "10.10.0.0/25", wgIP: "10.10.0.5",
			want: "Address = 10.10.0.5/25", wantDNS: "DNS = 10.10.0.1",
		},
		{
			name: "a /28 follows the allocation", cidr: "10.10.0.0/28", wgIP: "10.10.0.5",
			want: "Address = 10.10.0.5/28", wantDNS: "DNS = 10.10.0.1",
		},
		{
			name: "a /16 follows the allocation", cidr: "10.10.0.0/16", wgIP: "10.10.0.5",
			want: "Address = 10.10.0.5/16", wantDNS: "DNS = 10.10.0.1",
		},
		{
			name: "a network outside 10.10 follows too", cidr: "192.168.50.0/26", wgIP: "192.168.50.5",
			want: "Address = 192.168.50.5/26", wantDNS: "DNS = 192.168.50.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", tt.cidr)

			config := svc.BuildPeerConfig(&Peer{Name: "p", WgIP: tt.wgIP, PublicKey: "peer-key"}, "private-key")

			if !strings.Contains(config, tt.want) {
				t.Errorf("config = %q, want %q", config, tt.want)
			}
			if !strings.Contains(config, tt.wantDNS) {
				t.Errorf("config = %q, want %q", config, tt.wantDNS)
			}
			if !strings.Contains(config, "AllowedIPs = "+tt.cidr) {
				t.Errorf("config = %q, want AllowedIPs to be the whole network %q", config, tt.cidr)
			}
		})
	}
}

// TestPeerConfig_MalformedNetworkKeepsTheOldMask pins the fallback: a network
// that cannot be parsed still produces the mask this used to hardcode, rather
// than an empty or zero one.
func TestPeerConfig_MalformedNetworkKeepsTheOldMask(t *testing.T) {
	svc := NewService(nil, testEncryptionKey, "hub-pub-key", "1.2.3.4:51820", "not-a-cidr")

	config := svc.BuildPeerConfig(&Peer{Name: "p", WgIP: "10.10.0.5", PublicKey: "peer-key"}, "private-key")

	if !strings.Contains(config, "Address = 10.10.0.5/24") {
		t.Errorf("config = %q, want the previous /24 fallback", config)
	}
}
