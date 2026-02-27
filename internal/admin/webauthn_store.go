package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialInfo is a lightweight view of a WebAuthn credential for the settings page.
type CredentialInfo struct {
	ID        string
	CreatedAt time.Time
	SignCount int
}

// AdminUser implements webauthn.User for the single admin account.
// The user ID is deterministically derived from the encryption key via HMAC.
type AdminUser struct {
	id          []byte
	credentials []webauthn.Credential
}

func NewAdminUser(encryptionKey string, credentials []webauthn.Credential) *AdminUser {
	key, _ := hex.DecodeString(encryptionKey)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("webauthn-admin"))
	return &AdminUser{
		id:          mac.Sum(nil),
		credentials: credentials,
	}
}

func (u *AdminUser) WebAuthnID() []byte                         { return u.id }
func (u *AdminUser) WebAuthnName() string                       { return "admin" }
func (u *AdminUser) WebAuthnDisplayName() string                { return "Control Plane Admin" }
func (u *AdminUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// WebAuthnStore persists WebAuthn credentials in PostgreSQL.
type WebAuthnStore struct {
	pool *pgxpool.Pool
}

func NewWebAuthnStore(pool *pgxpool.Pool) *WebAuthnStore {
	return &WebAuthnStore{pool: pool}
}

// ListCredentials returns all stored WebAuthn credentials.
func (s *WebAuthnStore) ListCredentials(ctx context.Context) ([]webauthn.Credential, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT credential_id, public_key, attestation_type, transports, aaguid, sign_count
		FROM webauthn_credentials
		ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []webauthn.Credential
	for rows.Next() {
		var (
			credID          []byte
			publicKey       []byte
			attestationType string
			transports      []string
			aaguid          []byte
			signCount       int
		)
		if err := rows.Scan(&credID, &publicKey, &attestationType, &transports, &aaguid, &signCount); err != nil {
			return nil, err
		}

		ts := make([]protocol.AuthenticatorTransport, len(transports))
		for i, t := range transports {
			ts[i] = protocol.AuthenticatorTransport(t)
		}

		creds = append(creds, webauthn.Credential{
			ID:              credID,
			PublicKey:       publicKey,
			AttestationType: attestationType,
			Transport:       ts,
			Authenticator: webauthn.Authenticator{
				AAGUID:    aaguid,
				SignCount: uint32(signCount),
			},
		})
	}

	return creds, rows.Err()
}

// AddCredential stores a new WebAuthn credential.
func (s *WebAuthnStore) AddCredential(ctx context.Context, cred *webauthn.Credential) error {
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO webauthn_credentials (credential_id, public_key, attestation_type, transports, aaguid, sign_count)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		cred.ID, cred.PublicKey, cred.AttestationType, transports, cred.Authenticator.AAGUID, cred.Authenticator.SignCount)
	return err
}

// UpdateSignCount updates the sign counter for a credential (anti-cloning).
func (s *WebAuthnStore) UpdateSignCount(ctx context.Context, credentialID []byte, signCount uint32) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webauthn_credentials SET sign_count = $1 WHERE credential_id = $2`,
		signCount, credentialID)
	return err
}

// HasCredentials returns true if at least one credential exists.
func (s *WebAuthnStore) HasCredentials(ctx context.Context) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM webauthn_credentials)`).Scan(&exists)
	return exists, err
}

// ListCredentialInfos returns lightweight credential info for the settings page.
func (s *WebAuthnStore) ListCredentialInfos(ctx context.Context) ([]CredentialInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, created_at, sign_count FROM webauthn_credentials ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var infos []CredentialInfo
	for rows.Next() {
		var ci CredentialInfo
		if err := rows.Scan(&ci.ID, &ci.CreatedAt, &ci.SignCount); err != nil {
			return nil, err
		}
		infos = append(infos, ci)
	}
	return infos, rows.Err()
}

// DeleteCredential deletes a WebAuthn credential by ID.
func (s *WebAuthnStore) DeleteCredential(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id = $1`, id)
	return err
}
