package admin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const challengeTTL = 5 * time.Minute

// sessionEntry pairs session data with a flow type for extra safety.
type sessionEntry struct {
	data     *webauthn.SessionData
	flowType string // "register" or "login"
}

// webauthnSessions stores challenge data between begin/finish calls.
// Sessions are keyed by a random token (not a fixed key) to prevent
// concurrent requests from overwriting each other's challenges.
type webauthnSessions struct {
	mu       sync.Mutex
	sessions map[string]sessionEntry
}

func newWebAuthnSessions() *webauthnSessions {
	return &webauthnSessions{sessions: make(map[string]sessionEntry)}
}

// generateToken creates a random 32-byte base64url token for session keying.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Set stores session data under a new random token and returns the token.
func (s *webauthnSessions) Set(flowType string, data *webauthn.SessionData) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// GC expired entries (bounded: at most a handful)
	for k, v := range s.sessions {
		if time.Now().After(v.data.Expires) {
			delete(s.sessions, k)
		}
	}
	s.sessions[token] = sessionEntry{data: data, flowType: flowType}
	return token, nil
}

// Get retrieves and deletes session data by token (one-time use).
// Returns nil if token not found, expired, or flow type mismatch.
func (s *webauthnSessions) Get(token, expectedFlow string) *webauthn.SessionData {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.sessions[token]
	if !ok {
		return nil
	}
	delete(s.sessions, token) // one-time use
	if entry.flowType != expectedFlow {
		return nil
	}
	if time.Now().After(entry.data.Expires) {
		return nil
	}
	return entry.data
}

// --- Login page ---

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if isAuthenticated(r, h.encryptionKey) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}

	has, err := h.webauthnStore.HasCredentials(r.Context())
	if err != nil {
		slog.Error("admin: check credentials", "error", err)
		http.Error(w, "internal error", 500)
		return
	}

	data := struct {
		HasCredentials bool
	}{
		HasCredentials: has,
	}

	if err := h.tmpl.RenderStandalone(w, "login", data); err != nil {
		slog.Error("admin: render login", "error", err)
	}
}

// --- Registration ---

func (h *Handler) registerBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	has, err := h.webauthnStore.HasCredentials(ctx)
	if err != nil {
		slog.Error("admin: check credentials", "error", err)
		jsonError(w, "internal error", 500)
		return
	}

	// If credentials exist, only allow registration when authenticated
	if has && !isAuthenticated(r, h.encryptionKey) {
		jsonError(w, "forbidden", 403)
		return
	}

	creds, err := h.webauthnStore.ListCredentials(ctx)
	if err != nil {
		slog.Error("admin: list credentials", "error", err)
		jsonError(w, "internal error", 500)
		return
	}

	user := NewAdminUser(h.encryptionKey, creds)

	creation, session, err := h.webauthn.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)
	if err != nil {
		slog.Error("admin: begin registration", "error", err)
		jsonError(w, "failed to begin registration", 500)
		return
	}

	session.Expires = time.Now().Add(challengeTTL)
	token, err := h.webauthnSessions.Set("register", session)
	if err != nil {
		slog.Error("admin: generate session token", "error", err)
		jsonError(w, "internal error", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"publicKey":    creation.Response,
		"sessionToken": token,
	})
}

func (h *Handler) registerFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	has, err := h.webauthnStore.HasCredentials(ctx)
	if err != nil {
		slog.Error("admin: check credentials", "error", err)
		jsonError(w, "internal error", 500)
		return
	}
	if has && !isAuthenticated(r, h.encryptionKey) {
		jsonError(w, "forbidden", 403)
		return
	}

	sessionToken := r.Header.Get("X-Session-Token")
	if sessionToken == "" {
		jsonError(w, "missing session token", 400)
		return
	}

	session := h.webauthnSessions.Get(sessionToken, "register")
	if session == nil {
		jsonError(w, "no pending registration", 400)
		return
	}

	creds, err := h.webauthnStore.ListCredentials(ctx)
	if err != nil {
		slog.Error("admin: list credentials", "error", err)
		jsonError(w, "internal error", 500)
		return
	}

	user := NewAdminUser(h.encryptionKey, creds)

	credential, err := h.webauthn.FinishRegistration(user, *session, r)
	if err != nil {
		slog.Error("admin: finish registration", "error", err)
		jsonError(w, "registration failed", 400)
		return
	}

	if err := h.webauthnStore.AddCredential(ctx, credential); err != nil {
		slog.Error("admin: store credential", "error", err)
		jsonError(w, "failed to store credential", 500)
		return
	}

	slog.Info("admin: webauthn credential registered")

	// Auto-login after first registration
	if err := setSessionCookie(w, h.encryptionKey); err != nil {
		slog.Error("admin: set session cookie", "error", err)
		jsonError(w, "registered but failed to create session", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// --- Login ---

func (h *Handler) loginBegin(w http.ResponseWriter, r *http.Request) {
	assertion, session, err := h.webauthn.BeginDiscoverableLogin()
	if err != nil {
		slog.Error("admin: begin login", "error", err)
		jsonError(w, "failed to begin login", 500)
		return
	}

	session.Expires = time.Now().Add(challengeTTL)
	token, err := h.webauthnSessions.Set("login", session)
	if err != nil {
		slog.Error("admin: generate session token", "error", err)
		jsonError(w, "internal error", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"publicKey":    assertion.Response,
		"sessionToken": token,
	})
}

func (h *Handler) loginFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sessionToken := r.Header.Get("X-Session-Token")
	if sessionToken == "" {
		jsonError(w, "no pending login", 400)
		return
	}

	session := h.webauthnSessions.Get(sessionToken, "login")
	if session == nil {
		jsonError(w, "no pending login", 400)
		return
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		creds, err := h.webauthnStore.ListCredentials(ctx)
		if err != nil {
			return nil, err
		}
		return NewAdminUser(h.encryptionKey, creds), nil
	}

	_, credential, err := h.webauthn.FinishPasskeyLogin(handler, *session, r)
	if err != nil {
		slog.Error("admin: finish login", "error", err)
		jsonError(w, "login failed", 401)
		return
	}

	// Update sign count (anti-cloning)
	if err := h.webauthnStore.UpdateSignCount(ctx, credential.ID, credential.Authenticator.SignCount); err != nil {
		slog.Error("admin: update sign count", "error", err)
	}

	if err := setSessionCookie(w, h.encryptionKey); err != nil {
		slog.Error("admin: set session cookie", "error", err)
		jsonError(w, "authenticated but failed to create session", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// --- Logout ---

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// --- Helpers ---

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
