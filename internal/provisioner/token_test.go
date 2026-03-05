package provisioner

import (
	"encoding/hex"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	token, err := generateToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must be 64 hex chars (32 bytes)
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}

	// Must be valid hex
	_, err = hex.DecodeString(token)
	if err != nil {
		t.Errorf("token is not valid hex: %v", err)
	}
}

func TestGenerateToken_Uniqueness(t *testing.T) {
	tokens := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token, err := generateToken()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens[token] {
			t.Fatalf("duplicate token generated: %s", token)
		}
		tokens[token] = true
	}
}
