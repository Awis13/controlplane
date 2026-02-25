package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const challengeTTL = 5 * time.Minute

// webauthnSessions stores challenge data between begin/finish calls.
type webauthnSessions struct {
	mu       sync.Mutex
	sessions map[string]*webauthn.SessionData // key: "register" or "login"
}

func newWebAuthnSessions() *webauthnSessions {
	return &webauthnSessions{sessions: make(map[string]*webauthn.SessionData)}
}

func (s *webauthnSessions) Set(key string, data *webauthn.SessionData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[key] = data
}

func (s *webauthnSessions) Get(key string) *webauthn.SessionData {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.sessions[key]
	if !ok {
		return nil
	}
	if time.Now().After(data.Expires) {
		delete(s.sessions, key)
		return nil
	}
	delete(s.sessions, key) // one-time use
	return data
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
	h.webauthnSessions.Set("register", session)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creation)
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

	session := h.webauthnSessions.Get("register")
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
	h.webauthnSessions.Set("login", session)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(assertion)
}

func (h *Handler) loginFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	session := h.webauthnSessions.Get("login")
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
