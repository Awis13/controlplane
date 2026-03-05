package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"controlplane/internal/response"
	"controlplane/internal/user"
)

type contextKey string

const userContextKey contextKey = "auth_user"

// JWTAuth returns middleware that validates JWT Bearer tokens and sets the user in context.
// This is separate from the existing admin WebAuthn auth and the API Bearer token auth.
func JWTAuth(userStore *user.Store, tokenStore *TokenStore, jwtSecret string) func(http.Handler) http.Handler {
	secret := []byte(jwtSecret)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				response.Error(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				response.Error(w, http.StatusUnauthorized, "invalid authorization header format")
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return secret, nil
			})
			if err != nil || !token.Valid {
				response.Error(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				response.Error(w, http.StatusUnauthorized, "invalid token claims")
				return
			}

			// Check if token is revoked by jti
			if jti, ok := claims["jti"].(string); ok && jti != "" && tokenStore != nil {
				revoked, err := tokenStore.IsRevoked(r.Context(), jti)
				if err != nil {
					slog.Error("jwt middleware: check revocation", "error", err)
					response.Error(w, http.StatusInternalServerError, "failed to verify token")
					return
				}
				if revoked {
					response.Error(w, http.StatusUnauthorized, "token has been revoked")
					return
				}
			}

			sub, ok := claims["sub"].(string)
			if !ok {
				response.Error(w, http.StatusUnauthorized, "invalid token subject")
				return
			}

			userID, err := uuid.Parse(sub)
			if err != nil {
				response.Error(w, http.StatusUnauthorized, "invalid user ID in token")
				return
			}

			u, err := userStore.GetByID(r.Context(), userID)
			if err != nil {
				slog.Error("jwt middleware: get user", "error", err)
				response.Error(w, http.StatusInternalServerError, "failed to verify user")
				return
			}
			if u == nil {
				response.Error(w, http.StatusUnauthorized, "user not found")
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext extracts the authenticated user from the request context.
// Returns nil if no user is set (unauthenticated request).
func UserFromContext(ctx context.Context) *user.User {
	u, _ := ctx.Value(userContextKey).(*user.User)
	return u
}
