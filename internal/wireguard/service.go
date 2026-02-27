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

// Service управляет операциями WireGuard: генерация ключей, конфигов, QR-кодов, применение к wg0.
type Service struct {
	store         *Store
	encryptionKey string
	hubPublicKey  string
	hubEndpoint   string // например, 46.225.113.2:51820
	networkCIDR   string // например, 10.10.0.0/24
}

// NewService создаёт новый WireGuard сервис.
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

// GenerateKeypair генерирует пару ключей WireGuard (privateKey, publicKey).
func (s *Service) GenerateKeypair() (privateKey, publicKey string, err error) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", fmt.Errorf("generate private key: %w", err)
	}
	return key.String(), key.PublicKey().String(), nil
}

// GeneratePresharedKey генерирует preshared key для дополнительной защиты.
func (s *Service) GeneratePresharedKey() (string, error) {
	key, err := wgtypes.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("generate preshared key: %w", err)
	}
	return key.String(), nil
}

// EncryptPSK шифрует preshared key для хранения в БД.
func (s *Service) EncryptPSK(psk string) (string, error) {
	return crypto.Encrypt(psk, s.encryptionKey)
}

// DecryptPSK расшифровывает preshared key из БД.
func (s *Service) DecryptPSK(encrypted string) (string, error) {
	return crypto.Decrypt(encrypted, s.encryptionKey)
}

// CreatePeer создаёт нового пира: генерирует ключи, выделяет IP, сохраняет в БД.
// Возвращает пир и приватный ключ (приватный ключ не хранится на сервере).
func (s *Service) CreatePeer(ctx context.Context, req CreatePeerRequest) (*Peer, string, error) {
	// Генерируем ключи
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

	// Выделяем IP
	wgIP, err := s.store.GetNextAvailableIP(ctx, s.networkCIDR)
	if err != nil {
		return nil, "", fmt.Errorf("allocate IP: %w", err)
	}

	// Определяем allowed_ips
	allowedIPs := req.AllowedIPs
	if allowedIPs == "" {
		allowedIPs = wgIP + "/32"
	}

	// Подготавливаем пир
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

	// Сохраняем в БД
	created, err := s.store.Create(ctx, peer)
	if err != nil {
		return nil, "", fmt.Errorf("create peer in DB: %w", err)
	}

	return created, privateKey, nil
}

// BuildPeerConfig собирает полный WireGuard .conf для клиента.
func (s *Service) BuildPeerConfig(peer *Peer, privateKey string) string {
	var sb strings.Builder

	// Определяем DNS и AllowedIPs на основе networkCIDR
	dnsAddr := "10.10.0.1"
	peerAllowedIPs := s.networkCIDR
	if prefix, err := netip.ParsePrefix(s.networkCIDR); err == nil {
		// DNS = первый адрес в подсети + 1 (gateway)
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

	// Расшифровываем PSK если есть
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

// GenerateQRCode генерирует PNG QR-код из строки конфига.
func (s *Service) GenerateQRCode(config string) ([]byte, error) {
	png, err := qrcode.Encode(config, qrcode.Medium, 512)
	if err != nil {
		return nil, fmt.Errorf("generate qr code: %w", err)
	}
	return png, nil
}

// ApplyPeer добавляет/обновляет пир на wg0 интерфейсе хоста.
func (s *Service) ApplyPeer(peer *Peer) error {
	args := []string{"set", "wg0", "peer", peer.PublicKey, "allowed-ips", peer.AllowedIPs}

	if peer.Endpoint != nil && *peer.Endpoint != "" {
		args = append(args, "endpoint", *peer.Endpoint)
	}

	// PSK через pipe (безопаснее чем файл)
	if peer.PresharedKeyEncrypted != nil && *peer.PresharedKeyEncrypted != "" {
		psk, err := s.DecryptPSK(*peer.PresharedKeyEncrypted)
		if err != nil {
			return fmt.Errorf("decrypt psk for apply: %w", err)
		}
		// wg set ... preshared-key /dev/stdin < echo psk
		// Используем файловый дескриптор через pipe
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

// RemovePeer удаляет пир с wg0 интерфейса.
func (s *Service) RemovePeer(publicKey string) error {
	cmd := exec.Command("wg", "set", "wg0", "peer", publicKey, "remove")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg remove peer: %w (output: %s)", err, string(output))
	}
	return nil
}

// SyncPeers синхронизирует состояние БД с wg0: добавляет включённых, удаляет отключённых/удалённых.
func (s *Service) SyncPeers(ctx context.Context) error {
	peers, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list peers for sync: %w", err)
	}

	// Получаем текущие пиры на wg0
	currentPeers, err := s.getWGPeers()
	if err != nil {
		slog.Warn("wireguard: не удалось получить текущие пиры wg0, пропускаем sync", "error", err)
		return nil
	}

	// Собираем множество публичных ключей из БД (только enabled)
	enabledKeys := make(map[string]bool)
	for _, p := range peers {
		if p.Enabled {
			enabledKeys[p.PublicKey] = true
			// Применяем пир к wg0
			if err := s.ApplyPeer(&p); err != nil {
				slog.Error("wireguard: ошибка применения пира", "peer", p.Name, "error", err)
			}
		}
	}

	// Удаляем пиры которых нет в БД или которые disabled
	for _, key := range currentPeers {
		if !enabledKeys[key] {
			if err := s.RemovePeer(key); err != nil {
				slog.Error("wireguard: ошибка удаления пира", "public_key", key, "error", err)
			}
		}
	}

	return nil
}

// getWGPeers получает список публичных ключей текущих пиров wg0.
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
		// Проверяем что это валидный base64 ключ
		if _, err := base64.StdEncoding.DecodeString(line); err == nil {
			keys = append(keys, line)
		}
	}

	return keys, nil
}

// NetworkCIDR возвращает сетевой CIDR.
func (s *Service) NetworkCIDR() string {
	return s.networkCIDR
}

// HubPublicKey возвращает публичный ключ хаба.
func (s *Service) HubPublicKey() string {
	return s.hubPublicKey
}

// HubEndpoint возвращает endpoint хаба.
func (s *Service) HubEndpoint() string {
	return s.hubEndpoint
}
