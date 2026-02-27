package wireguard

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// allowedSupernet — разрешённый диапазон для AllowedIPs пиров.
var allowedSupernet = netip.MustParsePrefix("10.10.0.0/16")

// ValidateAllowedIPs проверяет, что AllowedIPs содержит валидные CIDR-адреса
// в рамках разрешённой подсети (10.10.0.0/16). Отклоняет 0.0.0.0/0 и адреса
// вне разрешённого диапазона.
func ValidateAllowedIPs(allowedIPs string) error {
	parts := strings.Split(allowedIPs, ",")
	if len(parts) == 0 {
		return fmt.Errorf("empty allowed IPs")
	}

	for _, part := range parts {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}

		// Отклоняем 0.0.0.0/0 — маршрут всего трафика
		if prefix.Bits() == 0 {
			return fmt.Errorf("0.0.0.0/0 is not allowed — route-all traffic is prohibited")
		}

		// Проверяем что адрес входит в разрешённую подсеть
		if !allowedSupernet.Contains(prefix.Addr()) {
			return fmt.Errorf("CIDR %q is outside allowed range %s", cidr, allowedSupernet)
		}

		// Проверяем что весь диапазон входит в разрешённую подсеть.
		// Для этого достаточно проверить, что маска не шире чем у supernet
		// при одинаковом network prefix.
		if prefix.Bits() < allowedSupernet.Bits() {
			return fmt.Errorf("CIDR %q is wider than allowed range %s", cidr, allowedSupernet)
		}
	}

	return nil
}

// ValidatePublicKey проверяет что строка — валидный WireGuard public key (32 байта base64).
func ValidatePublicKey(key string) error {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return fmt.Errorf("invalid base64: %w", err)
	}
	if len(b) != 32 {
		return fmt.Errorf("key must be 32 bytes, got %d", len(b))
	}
	return nil
}

// ValidateEndpoint проверяет формат host:port.
func ValidateEndpoint(endpoint string) error {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return fmt.Errorf("must be host:port format")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be 1-65535")
	}
	return nil
}
