package wireguard

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/netip"
	"os/exec"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"controlplane/internal/crypto"
)

// Service manages WireGuard operations: key generation, configs, QR codes, applying to wg0.
type Service struct {
	store         *Store
	encryptionKey string
	hubPublicKey  string
	hubEndpoint   string // e.g. 203.0.113.1:51820
	networkCIDR   string // e.g. 10.10.0.0/24
}

// NewService creates a new WireGuard service.
func NewService(store *Store, encryptionKey, hubPublicKey, hubEndpoint, networkCIDR string) *Service {
	if networkCIDR == "" {
		networkCIDR = "10.10.0.0/24"
	}
	return &Service{
		store:         store,
		encryptionKey: encryptionKey,
		hubPublicKey:  hubPublicKey,
		hubEndpoint:   hubEndpoint,
		networkCIDR:   networkCIDR,
	}
}

// GenerateKeypair generates a WireGuard key pair (privateKey, publicKey).
func (s *Service) GenerateKeypair() (privateKey, publicKey string, err error) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", fmt.Errorf("generate private key: %w", err)
	}
	return key.String(), key.PublicKey().String(), nil
}

// GeneratePresharedKey generates a preshared key for additional security.
func (s *Service) GeneratePresharedKey() (string, error) {
	key, err := wgtypes.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("generate preshared key: %w", err)
	}
	return key.String(), nil
}

// EncryptPSK encrypts a preshared key for DB storage.
func (s *Service) EncryptPSK(psk string) (string, error) {
	return crypto.Encrypt(psk, s.encryptionKey)
}

// DecryptPSK decrypts a preshared key from the DB.
func (s *Service) DecryptPSK(encrypted string) (string, error) {
	return crypto.Decrypt(encrypted, s.encryptionKey)
}

// CreatePeer creates a new peer: generates keys, allocates IP, saves to DB.
// Returns the peer and private key (private key is not stored on the server).
func (s *Service) CreatePeer(ctx context.Context, req CreatePeerRequest) (*Peer, string, error) {
	// Generate keys
	privateKey, publicKey, err := s.GenerateKeypair()
	if err != nil {
		return nil, "", fmt.Errorf("generate keypair: %w", err)
	}

	psk, err := s.GeneratePresharedKey()
	if err != nil {
		return nil, "", fmt.Errorf("generate psk: %w", err)
	}

	encryptedPSK, err := s.EncryptPSK(psk)
	if err != nil {
		return nil, "", fmt.Errorf("encrypt psk: %w", err)
	}

	// Allocate IP
	wgIP, err := s.store.GetNextAvailableIP(ctx, s.networkCIDR)
	if err != nil {
		return nil, "", fmt.Errorf("allocate IP: %w", err)
	}

	// Determine allowed_ips
	allowedIPs := req.AllowedIPs
	if allowedIPs == "" {
		allowedIPs = wgIP + "/32"
	}

	// Prepare peer
	peer := &Peer{
		Name:                  req.Name,
		PublicKey:             publicKey,
		PresharedKeyEncrypted: &encryptedPSK,
		WgIP:                  wgIP,
		AllowedIPs:            allowedIPs,
		Type:                  req.Type,
		Enabled:               true,
	}

	if req.Endpoint != "" {
		peer.Endpoint = &req.Endpoint
	}
	if req.TenantID != "" {
		peer.TenantID = &req.TenantID
	}

	// Save to DB
	created, err := s.store.Create(ctx, peer)
	if err != nil {
		return nil, "", fmt.Errorf("create peer in DB: %w", err)
	}

	return created, privateKey, nil
}

// BuildPeerConfig builds a full WireGuard .conf for the client.
func (s *Service) BuildPeerConfig(peer *Peer, privateKey string) string {
	var sb strings.Builder

	// Derive DNS and AllowedIPs from networkCIDR
	dnsAddr := "10.10.0.1"
	peerAllowedIPs := s.networkCIDR
	if prefix, err := netip.ParsePrefix(s.networkCIDR); err == nil {
		// DNS = first address in subnet + 1 (gateway)
		base := prefix.Addr()
		dnsAddr = base.Next().String()
		peerAllowedIPs = prefix.String()
	}

	sb.WriteString("[Interface]\n")
	sb.WriteString(fmt.Sprintf("Address = %s/24\n", peer.WgIP))
	sb.WriteString(fmt.Sprintf("PrivateKey = %s\n", privateKey))
	sb.WriteString(fmt.Sprintf("DNS = %s\n", dnsAddr))
	sb.WriteString("\n")
	sb.WriteString("[Peer]\n")
	sb.WriteString(fmt.Sprintf("PublicKey = %s\n", s.hubPublicKey))

	// Decrypt PSK if present
	if peer.PresharedKeyEncrypted != nil && *peer.PresharedKeyEncrypted != "" {
		psk, err := s.DecryptPSK(*peer.PresharedKeyEncrypted)
		if err == nil {
			sb.WriteString(fmt.Sprintf("PresharedKey = %s\n", psk))
		}
	}

	sb.WriteString(fmt.Sprintf("AllowedIPs = %s\n", peerAllowedIPs))
	sb.WriteString(fmt.Sprintf("Endpoint = %s\n", s.hubEndpoint))
	sb.WriteString("PersistentKeepalive = 25\n")

	return sb.String()
}

// GenerateQRCode generates a PNG QR code from a config string.
func (s *Service) GenerateQRCode(config string) ([]byte, error) {
	png, err := qrcode.Encode(config, qrcode.Medium, 512)
	if err != nil {
		return nil, fmt.Errorf("generate qr code: %w", err)
	}
	return png, nil
}

// ApplyPeer adds/updates a peer on the host's wg0 interface.
func (s *Service) ApplyPeer(peer *Peer) error {
	args := []string{"set", "wg0", "peer", peer.PublicKey, "allowed-ips", peer.AllowedIPs}

	if peer.Endpoint != nil && *peer.Endpoint != "" {
		args = append(args, "endpoint", *peer.Endpoint)
	}

	// PSK via pipe (safer than file)
	if peer.PresharedKeyEncrypted != nil && *peer.PresharedKeyEncrypted != "" {
		psk, err := s.DecryptPSK(*peer.PresharedKeyEncrypted)
		if err != nil {
			return fmt.Errorf("decrypt psk for apply: %w", err)
		}
		// wg set ... preshared-key /dev/stdin < echo psk
		// Use file descriptor via pipe
		args = append(args, "preshared-key", "/dev/stdin")
		cmd := exec.Command("wg", args...)
		cmd.Stdin = strings.NewReader(psk)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("wg set peer: %w (output: %s)", err, string(output))
		}
		return nil
	}

	cmd := exec.Command("wg", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg set peer: %w (output: %s)", err, string(output))
	}

	return nil
}

// RemovePeer removes a peer from the wg0 interface.
func (s *Service) RemovePeer(publicKey string) error {
	cmd := exec.Command("wg", "set", "wg0", "peer", publicKey, "remove")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg remove peer: %w (output: %s)", err, string(output))
	}
	return nil
}

// SyncPeers syncs DB state with wg0: adds enabled peers, removes disabled/deleted ones.
func (s *Service) SyncPeers(ctx context.Context) error {
	peers, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list peers for sync: %w", err)
	}

	// Get current peers on wg0
	currentPeers, err := s.getWGPeers()
	if err != nil {
		slog.Warn("wireguard: failed to get current wg0 peers, skipping sync", "error", err)
		return nil
	}

	// Collect public keys from DB (enabled only)
	enabledKeys := make(map[string]bool)
	for _, p := range peers {
		if p.Enabled {
			enabledKeys[p.PublicKey] = true
			// Apply peer to wg0
			if err := s.ApplyPeer(&p); err != nil {
				slog.Error("wireguard: failed to apply peer", "peer", p.Name, "error", err)
			}
		}
	}

	// Remove peers not in DB or disabled
	for _, key := range currentPeers {
		if !enabledKeys[key] {
			if err := s.RemovePeer(key); err != nil {
				slog.Error("wireguard: failed to remove peer", "public_key", key, "error", err)
			}
		}
	}

	return nil
}

// getWGPeers gets the list of public keys of current wg0 peers.
func (s *Service) getWGPeers() ([]string, error) {
	cmd := exec.Command("wg", "show", "wg0", "peers")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("wg show peers: %w", err)
	}

	var keys []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Check that it's a valid base64 key
		if _, err := base64.StdEncoding.DecodeString(line); err == nil {
			keys = append(keys, line)
		}
	}

	return keys, nil
}

// NetworkCIDR returns the network CIDR.
func (s *Service) NetworkCIDR() string {
	return s.networkCIDR
}

// HubPublicKey returns the hub's public key.
func (s *Service) HubPublicKey() string {
	return s.hubPublicKey
}

// HubEndpoint returns the hub's endpoint.
func (s *Service) HubEndpoint() string {
	return s.hubEndpoint
}
