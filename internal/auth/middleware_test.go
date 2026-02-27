package auth

import (
	"context"
	"testing"

	"controlplane/internal/user"

	"github.com/google/uuid"
)

func TestUserFromContext_WithUser(t *testing.T) {
	u := &user.User{
		ID:          uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Email:       "test@example.com",
		DisplayName: "Test",
	}
	ctx := context.WithValue(context.Background(), userContextKey, u)

	got := UserFromContext(ctx)
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.Email != "test@example.com" {
		t.Errorf("email = %q, want test@example.com", got.Email)
	}
}

func TestUserFromContext_WithoutUser(t *testing.T) {
	ctx := context.Background()

	got := UserFromContext(ctx)
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestUserFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), userContextKey, "not a user")

	got := UserFromContext(ctx)
	if got != nil {
		t.Errorf("expected nil for wrong type, got %+v", got)
	}
}
