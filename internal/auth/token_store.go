package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenStore manages JWT token revocation.
type TokenStore struct {
	pool *pgxpool.Pool
}

// NewTokenStore creates a new TokenStore.
func NewTokenStore(pool *pgxpool.Pool) *TokenStore {
	return &TokenStore{pool: pool}
}

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

// Cleanup removes expired revocations (called periodically).
func (s *TokenStore) Cleanup(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM revoked_tokens WHERE expires_at < now()`)
	return err
}
