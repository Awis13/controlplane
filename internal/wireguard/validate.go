package wireguard

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// allowedSupernet is the allowed range for peer AllowedIPs.
var allowedSupernet = netip.MustParsePrefix("10.10.0.0/16")

// ValidateAllowedIPs checks that AllowedIPs contains valid CIDR addresses
// within the allowed subnet (10.10.0.0/16). Rejects 0.0.0.0/0 and addresses
// outside the allowed range.
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

		// Reject 0.0.0.0/0 — route-all traffic
		if prefix.Bits() == 0 {
			return fmt.Errorf("0.0.0.0/0 is not allowed — route-all traffic is prohibited")
		}

		// Check that address is within the allowed subnet
		if !allowedSupernet.Contains(prefix.Addr()) {
			return fmt.Errorf("CIDR %q is outside allowed range %s", cidr, allowedSupernet)
		}

		// Check that the entire range fits within the allowed subnet.
		// It suffices to check that the mask is not wider than the supernet's.
		if prefix.Bits() < allowedSupernet.Bits() {
			return fmt.Errorf("CIDR %q is wider than allowed range %s", cidr, allowedSupernet)
		}
	}

	return nil
}

// ValidatePublicKey checks that the string is a valid WireGuard public key (32 bytes base64).
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

// ValidateEndpoint checks the host:port format.
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
