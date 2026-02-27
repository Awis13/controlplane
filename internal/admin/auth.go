package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"controlplane/internal/crypto"
)

const (
	sessionCookieName = "admin_session"
	sessionMaxAge     = 8 * time.Hour
)

type sessionPayload struct {
	UserID    string    `json:"uid"`
	ExpiresAt time.Time `json:"exp"`
}

// requireAuth redirects unauthenticated requests to /admin/login.
func requireAuth(encryptionKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}

			plaintext, err := crypto.Decrypt(cookie.Value, encryptionKey)
			if err != nil {
				clearSessionCookie(w)
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}

			var sess sessionPayload
			if err := json.Unmarshal([]byte(plaintext), &sess); err != nil {
				clearSessionCookie(w)
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}

			if time.Now().After(sess.ExpiresAt) {
				clearSessionCookie(w)
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// setSessionCookie creates an encrypted session cookie.
func setSessionCookie(w http.ResponseWriter, encryptionKey string) error {
	sess := sessionPayload{
		UserID:    "admin",
		ExpiresAt: time.Now().Add(sessionMaxAge),
	}

	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}

	encrypted, err := crypto.Encrypt(string(data), encryptionKey)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    encrypted,
		Path:     "/admin",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	return nil
}

// clearSessionCookie removes the session cookie.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// isAuthenticated checks if the request has a valid session without redirecting.
func isAuthenticated(r *http.Request, encryptionKey string) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	plaintext, err := crypto.Decrypt(cookie.Value, encryptionKey)
	if err != nil {
		return false
	}

	var sess sessionPayload
	if err := json.Unmarshal([]byte(plaintext), &sess); err != nil {
		return false
	}

	return time.Now().Before(sess.ExpiresAt)
}
