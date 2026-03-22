package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// callRegisterSafe вызывает Register и перехватывает панику от nil store.
// Возвращает записанный HTTP-код и true если была паника (= токен принят, дошли до DB).
func callRegisterSafe(h *Handler, req *http.Request) (code int, panicked bool) {
	w := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			code = w.Code
		}
	}()
	h.Register(w, req)
	return w.Code, false
}

// TestRegister_TokenFromBody проверяет, что registration_token из JSON body принимается.
func TestRegister_TokenFromBody(t *testing.T) {
	h := &Handler{
		registrationToken: "secret-invite-token",
	}

	body := `{"email":"test@example.com","password":"longpassword12","registration_token":"secret-invite-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	code, panicked := callRegisterSafe(h, req)
	// Паника на nil store означает, что проверка токена пройдена успешно.
	if code == http.StatusForbidden && !panicked {
		t.Fatal("expected token from body to be accepted, got 403 Forbidden")
	}
}

// TestRegister_TokenFromHeader проверяет fallback на X-Registration-Token заголовок.
func TestRegister_TokenFromHeader(t *testing.T) {
	h := &Handler{
		registrationToken: "secret-invite-token",
	}

	body := `{"email":"test@example.com","password":"longpassword12"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Token", "secret-invite-token")

	code, panicked := callRegisterSafe(h, req)
	if code == http.StatusForbidden && !panicked {
		t.Fatal("expected token from header to be accepted, got 403 Forbidden")
	}
}

// TestRegister_WrongToken проверяет отказ при неверном токене.
func TestRegister_WrongToken(t *testing.T) {
	h := &Handler{
		registrationToken: "secret-invite-token",
	}

	body := `{"email":"test@example.com","password":"longpassword12","registration_token":"wrong-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Register(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for wrong token, got %d", w.Code)
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "registration is not open" {
		t.Errorf("unexpected error message: %q", resp["error"])
	}
}

// TestRegister_NoTokenWhenRequired проверяет отказ когда токен обязателен, но не передан.
func TestRegister_NoTokenWhenRequired(t *testing.T) {
	h := &Handler{
		registrationToken: "secret-invite-token",
	}

	body := `{"email":"test@example.com","password":"longpassword12"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Register(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when no token provided, got %d", w.Code)
	}
}

// TestRegister_BodyTokenTakesPriority проверяет, что токен из body приоритетнее заголовка.
func TestRegister_BodyTokenTakesPriority(t *testing.T) {
	h := &Handler{
		registrationToken: "correct-token",
	}

	// Body содержит правильный токен, заголовок — неправильный.
	body := `{"email":"test@example.com","password":"longpassword12","registration_token":"correct-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Token", "wrong-token")

	code, panicked := callRegisterSafe(h, req)
	if code == http.StatusForbidden && !panicked {
		t.Fatal("body token should take priority over header; got 403")
	}
}
