// Package sso signs SSO assertions for tenant dashboards with a persistent
// Ed25519 key that lives only in the control plane.
//
// # Assertion format (versioned fixture contract, shared with FR-2 in Node)
//
// The payload is a canonical byte string, NOT JSON, so the signature does not
// depend on serialization order. Fields are joined with a single '|' separator
// in exactly this order:
//
//		v1|<userID>|<tenantID>|<tier>|<issuedUnix>|<expiresUnix>|<issuer>|<audience>
//
//	  - v1          — assertion format version.
//	  - userID      — the control-plane user ID (owner) the assertion is for.
//	  - tenantID    — the tenant the assertion grants access to.
//	  - tier        — the tenant tier the dashboard should honor (e.g. "free").
//	  - issuedUnix  — Unix seconds (int64) when the assertion was issued.
//	  - expiresUnix — Unix seconds (int64) when the assertion expires.
//	  - issuer      — the signing party, "controlplane" by default.
//	  - audience    — the tenant the assertion is addressed to (the tenant ID).
//
// The token handed to the browser is:
//
//	base64url(payload):base64url(sig)
//
// where base64url is base64.RawURLEncoding and sig is the Ed25519 signature
// over the raw payload bytes. The tenant verifies the signature with the
// public key written into its env (SSO_PUBLIC_KEY), so a browser holding the
// dashboard token cannot forge a tier upgrade: it does not have the private
// key, which never leaves the control plane.
package sso

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Signer signs SSO assertions with a persistent Ed25519 key that lives only
// in the control plane. The tenant verifies with the public key written into
// its env, so a browser holding the dashboard token cannot forge a tier
// upgrade: it does not have the private key.
type Signer struct {
	priv ed25519.PrivateKey
}

// NewSigner parses a hex-encoded Ed25519 private key. Both a 64-byte key
// (seed+public, the Go ed25519.PrivateKey form) and a 32-byte seed are
// accepted; anything else is an error.
func NewSigner(hexKey string) (*Signer, error) {
	decoded, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("sso: decode signing key: %w", err)
	}

	var priv ed25519.PrivateKey
	switch len(decoded) {
	case ed25519.PrivateKeySize: // 64 bytes: seed+public
		priv = ed25519.PrivateKey(decoded)
	case ed25519.SeedSize: // 32 bytes: seed only
		priv = ed25519.NewKeyFromSeed(decoded)
	default:
		return nil, fmt.Errorf("sso: signing key must be %d or %d bytes, got %d", ed25519.SeedSize, ed25519.PrivateKeySize, len(decoded))
	}

	return &Signer{priv: priv}, nil
}

// PublicKey returns the 32-byte Ed25519 public key.
func (s *Signer) PublicKey() []byte {
	return s.priv.Public().(ed25519.PublicKey)
}

// Sign signs the assertion bytes with Ed25519.
func (s *Signer) Sign(assertion []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, assertion), nil
}

// BuildAssertion builds the canonical v1 assertion payload. See the package
// comment for the exact format; FR-2 in Node verifies the same bytes.
func BuildAssertion(userID, tenantID, tier string, issued, expires int64, issuer, audience string) []byte {
	return []byte(strings.Join([]string{
		"v1",
		userID,
		tenantID,
		tier,
		strconv.FormatInt(issued, 10),
		strconv.FormatInt(expires, 10),
		issuer,
		audience,
	}, "|"))
}

// EncodeToken encodes a payload and its signature into the browser-facing
// token: base64url(payload):base64url(sig).
func EncodeToken(payload, sig []byte) string {
	return base64.RawURLEncoding.EncodeToString(payload) + ":" + base64.RawURLEncoding.EncodeToString(sig)
}
