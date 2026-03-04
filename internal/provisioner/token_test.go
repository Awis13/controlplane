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

	// Должен быть 64 hex символа (32 байта)
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}

	// Должен быть валидным hex
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
