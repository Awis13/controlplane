package wireguard

import (
	"testing"
)

func TestValidateAllowedIPs_Valid(t *testing.T) {
	valid := []string{
		"10.10.0.5/32",
		"10.10.0.0/24",
		"10.10.10.0/24",
		"10.10.0.0/24, 10.10.10.0/24",
		"10.10.255.0/24",
		"10.10.0.1/32",
	}
	for _, ips := range valid {
		if err := ValidateAllowedIPs(ips); err != nil {
			t.Errorf("ValidateAllowedIPs(%q) = %v, want nil", ips, err)
		}
	}
}

func TestValidateAllowedIPs_RejectRouteAll(t *testing.T) {
	err := ValidateAllowedIPs("0.0.0.0/0")
	if err == nil {
		t.Error("ValidateAllowedIPs(0.0.0.0/0) = nil, want error")
	}
}

func TestValidateAllowedIPs_RejectOutsideRange(t *testing.T) {
	invalid := []string{
		"192.168.1.0/24",
		"10.20.0.0/24",
		"172.16.0.0/16",
		"8.8.8.8/32",
		"10.11.0.0/24",
	}
	for _, ips := range invalid {
		if err := ValidateAllowedIPs(ips); err == nil {
			t.Errorf("ValidateAllowedIPs(%q) = nil, want error", ips)
		}
	}
}

func TestValidateAllowedIPs_RejectWiderThanSupernet(t *testing.T) {
	// 10.10.0.0/8 is wider than /16 — not allowed
	err := ValidateAllowedIPs("10.0.0.0/8")
	if err == nil {
		t.Error("ValidateAllowedIPs(10.0.0.0/8) = nil, want error")
	}
}

func TestValidateAllowedIPs_RejectInvalidCIDR(t *testing.T) {
	invalid := []string{
		"not-a-cidr",
		"10.10.0.0",
		"256.256.256.256/32",
		"10.10.0.0/33",
	}
	for _, ips := range invalid {
		if err := ValidateAllowedIPs(ips); err == nil {
			t.Errorf("ValidateAllowedIPs(%q) = nil, want error", ips)
		}
	}
}

func TestValidateAllowedIPs_MultipleWithOneInvalid(t *testing.T) {
	// First is valid, second is not
	err := ValidateAllowedIPs("10.10.0.5/32, 192.168.1.0/24")
	if err == nil {
		t.Error("expected error when one of multiple CIDRs is invalid")
	}
}

func TestValidateAllowedIPs_MultipleValid(t *testing.T) {
	err := ValidateAllowedIPs("10.10.0.0/24, 10.10.10.0/24")
	if err == nil {
		return // OK
	}
	t.Errorf("ValidateAllowedIPs with multiple valid CIDRs: %v", err)
}

func TestValidateAllowedIPs_EmptyString(t *testing.T) {
	// Empty string — all parts will be empty, skipped.
	// Edge case — we don't call validation with empty string in handlers.
	err := ValidateAllowedIPs("")
	// Empty string has one empty element after split, all skipped, OK
	if err != nil {
		t.Errorf("ValidateAllowedIPs('') = %v, want nil", err)
	}
}
