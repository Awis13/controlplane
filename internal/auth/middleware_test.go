package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"controlplane/internal/user"

	"github.com/golang-jwt/jwt/v5"
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

// --- Cookie fallback tests for JWTAuth middleware ---

// mockUserStore — заглушка для user.Store (нужен pgxpool, но мы тестируем до DB-вызова)
// JWTAuth middleware вызывает userStore.GetByID — на nil store будет паника.
// Мы тестируем: 1) что cookie принимается, 2) что Authorization header приоритетнее.

// TestJWTAuth_NoHeaderNoCookie проверяет 401 без auth header и без cookie.
func TestJWTAuth_NoHeaderNoCookie(t *testing.T) {
	middleware := JWTAuth(nil, nil, "test-secret")
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestJWTAuth_CookieFallback проверяет, что middleware читает JWT из access_token cookie.
func TestJWTAuth_CookieFallback(t *testing.T) {
	jwtSecret := "test-secret-for-cookie"
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	// Создаём валидный JWT
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   userID.String(),
		"email": "cookie@example.com",
		"jti":   uuid.New().String(),
		"exp":   time.Now().Add(15 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
	})
	tokenStr, err := tok.SignedString([]byte(jwtSecret))
	if err != nil {
		t.Fatal(err)
	}

	// tokenStore=nil → паника при IsRevoked? Нет — проверяем if tokenStore != nil.
	// userStore=nil → паника при GetByID. Значит, если дошли до паники — cookie был прочитан.
	middleware := JWTAuth(nil, nil, jwtSecret)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called (userStore is nil)")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// Нет Authorization header, только cookie
	req.AddCookie(&http.Cookie{
		Name:  "access_token",
		Value: tokenStr,
	})

	w := httptest.NewRecorder()

	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		handler.ServeHTTP(w, req)
	}()

	// Паника на nil userStore.GetByID = JWT из cookie был успешно распарсен
	if !panicked {
		// Если не было паники, проверяем что не 401 "missing authorization"
		if w.Code == http.StatusUnauthorized {
			t.Fatal("middleware should read JWT from cookie, but returned 401")
		}
	}
	// Паника — значит cookie прочитан и JWT валидирован
}

// TestJWTAuth_HeaderPriorityOverCookie проверяет, что Authorization header приоритетнее cookie.
func TestJWTAuth_HeaderPriorityOverCookie(t *testing.T) {
	jwtSecret := "test-secret-priority"

	// Создаём валидный JWT для header
	headerToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   uuid.New().String(),
		"email": "header@example.com",
		"exp":   time.Now().Add(15 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
	})
	headerTokenStr, _ := headerToken.SignedString([]byte(jwtSecret))

	// Невалидный токен для cookie
	cookieTokenStr := "invalid-cookie-token"

	middleware := JWTAuth(nil, nil, jwtSecret)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+headerTokenStr)
	req.AddCookie(&http.Cookie{
		Name:  "access_token",
		Value: cookieTokenStr,
	})

	w := httptest.NewRecorder()

	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		handler.ServeHTTP(w, req)
	}()

	// Если header JWT валиден — должна быть паника на GetByID (= header токен использован, а не cookie)
	if !panicked && w.Code == http.StatusUnauthorized {
		t.Fatal("valid header token should take priority over invalid cookie")
	}
}

// TestJWTAuth_InvalidCookieToken проверяет, что невалидный JWT в cookie отклоняется.
func TestJWTAuth_InvalidCookieToken(t *testing.T) {
	middleware := JWTAuth(nil, nil, "test-secret")
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.AddCookie(&http.Cookie{
		Name:  "access_token",
		Value: "totally-invalid-jwt",
	})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid cookie JWT, got %d", w.Code)
	}
}
