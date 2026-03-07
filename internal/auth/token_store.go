package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenStore manages JWT token revocation and refresh tokens.
type TokenStore struct {
	pool *pgxpool.Pool
}

// NewTokenStore creates a new TokenStore.
func NewTokenStore(pool *pgxpool.Pool) *TokenStore {
	return &TokenStore{pool: pool}
}

// --- Access token revocation ---

// Revoke marks a token as revoked by its jti.
func (s *TokenStore) Revoke(ctx context.Context, jti string, userID uuid.UUID, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO revoked_tokens (jti, user_id, expires_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		jti, userID, expiresAt)
	return err
}

// IsRevoked checks if a token jti has been revoked.
func (s *TokenStore) IsRevoked(ctx context.Context, jti string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM revoked_tokens WHERE jti = $1)`, jti).Scan(&exists)
	return exists, err
}

// Cleanup removes expired revocations and refresh tokens.
func (s *TokenStore) Cleanup(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM revoked_tokens WHERE expires_at < now()`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < now()`)
	return err
}

// --- Refresh tokens ---

// GenerateRefreshToken creates a random refresh token string.
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the SHA-256 hash of a token (stored in DB, not the raw token).
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateRefreshToken stores a hashed refresh token for a user.
func (s *TokenStore) CreateRefreshToken(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt)
	return err
}

// ValidateRefreshToken checks if a refresh token hash is valid (exists, not expired, not revoked).
// Returns the user ID if valid.
func (s *TokenStore) ValidateRefreshToken(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM refresh_tokens
		 WHERE token_hash = $1 AND expires_at > now() AND revoked_at IS NULL`,
		tokenHash).Scan(&userID)
	return userID, err
}

// RevokeRefreshToken marks a single refresh token as revoked.
func (s *TokenStore) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		tokenHash)
	return err
}

// RevokeAllUserRefreshTokens revokes all refresh tokens for a user.
func (s *TokenStore) RevokeAllUserRefreshTokens(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
		userID)
	return err
}
