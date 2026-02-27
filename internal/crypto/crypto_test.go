package crypto

import (
	"encoding/hex"
	"strings"
	"testing"
)

// validKey is a 32-byte hex-encoded key for tests.
var validKey = hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func TestRoundTrip(t *testing.T) {
	tests := []string{
		"hello world",
		"",
		"short",
		strings.Repeat("a", 10000),
		"special chars: !@#$%^&*()_+{}|:<>?",
		"unicode: 日本語テスト 🎵",
	}

	for _, plaintext := range tests {
		ciphertext, err := Encrypt(plaintext, validKey)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		if ciphertext == "" {
			t.Fatalf("Encrypt(%q) returned empty ciphertext", plaintext)
		}

		decrypted, err := Decrypt(ciphertext, validKey)
		if err != nil {
			t.Fatalf("Decrypt(%q): %v", plaintext, err)
		}
		if decrypted != plaintext {
			t.Errorf("round-trip failed: got %q, want %q", decrypted, plaintext)
		}
	}
}

func TestEncryptDifferentNonces(t *testing.T) {
	plaintext := "same input"
	c1, err := Encrypt(plaintext, validKey)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Encrypt(plaintext, validKey)
	if err != nil {
		t.Fatal(err)
	}
	if c1 == c2 {
		t.Error("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	ciphertext, err := Encrypt("secret", validKey)
	if err != nil {
		t.Fatal(err)
	}

	wrongKey := hex.EncodeToString([]byte("ffffffffffffffffffffffffffffffff"))
	_, err = Decrypt(ciphertext, wrongKey)
	if err == nil {
		t.Error("Decrypt with wrong key should fail")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	ciphertext, err := Encrypt("secret", validKey)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the middle
	bytes, _ := hex.DecodeString(ciphertext)
	bytes[len(bytes)/2] ^= 0xff
	tampered := hex.EncodeToString(bytes)

	_, err = Decrypt(tampered, validKey)
	if err == nil {
		t.Error("Decrypt of tampered ciphertext should fail")
	}
}

func TestInvalidKeyLength(t *testing.T) {
	shortKey := hex.EncodeToString([]byte("too-short"))

	_, err := Encrypt("test", shortKey)
	if err == nil {
		t.Error("Encrypt with short key should fail")
	}

	_, err = Decrypt("aabbcc", shortKey)
	if err == nil {
		t.Error("Decrypt with short key should fail")
	}
}

func TestInvalidKeyHex(t *testing.T) {
	_, err := Encrypt("test", "not-hex-at-all!")
	if err == nil {
		t.Error("Encrypt with invalid hex key should fail")
	}

	_, err = Decrypt("aabbcc", "not-hex-at-all!")
	if err == nil {
		t.Error("Decrypt with invalid hex key should fail")
	}
}

func TestDecryptInvalidHexCiphertext(t *testing.T) {
	_, err := Decrypt("not-valid-hex!", validKey)
	if err == nil {
		t.Error("Decrypt with invalid hex ciphertext should fail")
	}
}

func TestDecryptTooShortCiphertext(t *testing.T) {
	// Less than nonce size (12 bytes for GCM)
	_, err := Decrypt(hex.EncodeToString([]byte{1, 2, 3}), validKey)
	if err == nil {
		t.Error("Decrypt with too-short ciphertext should fail")
	}
}
