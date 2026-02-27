package auth

import (
	"context"

	"controlplane/internal/user"
)

// SetUserForTest injects a user into context. Intended for use in tests
// across packages that need to simulate an authenticated request.
func SetUserForTest(ctx context.Context, u *user.User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}
