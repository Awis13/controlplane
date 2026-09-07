package sso

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// testSigner returns a Signer backed by a freshly generated key.
func testSigner(t *testing.T) *Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &Signer{priv: priv}
}

func TestNewSigner_FromSeed(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	seed := priv.Seed()
	hexSeed := hex.EncodeToString(seed)

	s, err := NewSigner(hexSeed)
	if err != nil {
		t.Fatalf("NewSigner(seed): %v", err)
	}
	if len(s.PublicKey()) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(s.PublicKey()), ed25519.PublicKeySize)
	}
}

func TestNewSigner_FromFullKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	hexKey := hex.EncodeToString(priv)

	s, err := NewSigner(hexKey)
	if err != nil {
		t.Fatalf("NewSigner(full key): %v", err)
	}
	if len(s.PublicKey()) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(s.PublicKey()), ed25519.PublicKeySize)
	}
}

func TestNewSigner_InvalidLength(t *testing.T) {
	for _, bad := range []string{
		"", // empty
		hex.EncodeToString(make([]byte, 16)),
		hex.EncodeToString(make([]byte, 48)),
	} {
		if _, err := NewSigner(bad); err == nil {
			t.Errorf("NewSigner(%q): expected error for invalid key length", bad)
		}
	}
}

func TestNewSigner_InvalidHex(t *testing.T) {
	if _, err := NewSigner("not-hex"); err == nil {
		t.Error("NewSigner: expected error for non-hex input")
	}
}

func TestSign_VerifyRoundTrip(t *testing.T) {
	s := testSigner(t)
	assertion := BuildAssertion("user-1", "tenant-1", "studio", 100, 160, "controlplane", "tenant-1")

	sig, err := s.Sign(assertion)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !ed25519.Verify(s.PublicKey(), assertion, sig) {
		t.Error("signature did not verify against the public key")
	}
}

func TestSign_TamperedPayloadFailsVerify(t *testing.T) {
	s := testSigner(t)
	assertion := BuildAssertion("user-1", "tenant-1", "free", 100, 160, "controlplane", "tenant-1")

	sig, err := s.Sign(assertion)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Tamper with the tier: a browser forging an upgrade must not verify.
	tampered := BuildAssertion("user-1", "tenant-1", "studio", 100, 160, "controlplane", "tenant-1")
	if ed25519.Verify(s.PublicKey(), tampered, sig) {
		t.Error("tampered payload verified — signature must be over the original bytes")
	}
}

func TestBuildAssertion_Format(t *testing.T) {
	got := string(BuildAssertion("user-1", "tenant-1", "studio", 100, 160, "controlplane", "tenant-1"))
	want := "v1|user-1|tenant-1|studio|100|160|controlplane|tenant-1"
	if got != want {
		t.Errorf("BuildAssertion = %q, want %q", got, want)
	}
}

func TestEncodeToken_Format(t *testing.T) {
	payload := []byte("v1|user-1|tenant-1|studio|100|160|controlplane|tenant-1")
	sig := []byte{0x01, 0x02, 0x03}

	token := EncodeToken(payload, sig)
	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		t.Fatalf("token = %q, want exactly one ':' separator", token)
	}

	decodedPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(decodedPayload) != string(payload) {
		t.Errorf("decoded payload = %q, want %q", decodedPayload, payload)
	}

	decodedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if string(decodedSig) != string(sig) {
		t.Errorf("decoded sig = %v, want %v", decodedSig, sig)
	}
}
