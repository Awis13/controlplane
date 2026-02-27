CREATE TABLE revoked_tokens (
    jti TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_revoked_tokens_expires_at ON revoked_tokens(expires_at);
