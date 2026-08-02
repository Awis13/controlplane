package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"controlplane/internal/user"
)

// --- Signing algorithm ---

func TestHMACKeyfunc(t *testing.T) {
	secret := []byte("test-secret")

	tests := []struct {
		name    string
		token   *jwt.Token
		wantErr bool
	}{
		{name: "HS256 is accepted", token: jwt.New(jwt.SigningMethodHS256)},
		{name: "HS384 is accepted", token: jwt.New(jwt.SigningMethodHS384)},
		{name: "HS512 is accepted", token: jwt.New(jwt.SigningMethodHS512)},
		{name: "none is refused", token: jwt.New(jwt.SigningMethodNone), wantErr: true},
		{name: "RS256 is refused", token: jwt.New(jwt.SigningMethodRS256), wantErr: true},
		{name: "ES256 is refused", token: jwt.New(jwt.SigningMethodES256), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := hmacKeyfunc(secret)(tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s", tt.token.Method.Alg())
				}
				if key != nil {
					t.Error("the secret must not be handed over for a refused algorithm")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got, ok := key.([]byte); !ok || string(got) != string(secret) {
				t.Errorf("key = %v, want the secret", key)
			}
		})
	}
}

// reachedRevocation reports whether Logout accepted the token and got as far as
// revoking it. The handler under test has no token store, so reaching the
// revocation panics; that panic is the signal. The first subtest below proves
// the tripwire actually fires for a token that is accepted, so a quiet run
// really does mean the token was rejected.
func reachedRevocation(h *Handler, req *http.Request) (reached bool) {
	defer func() { reached = recover() != nil }()
	h.Logout(httptest.NewRecorder(), req)
	return reached
}

func logoutRequest(t *testing.T, tokenStr string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	return req.WithContext(SetUserForTest(req.Context(), &user.User{
		ID:    uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Email: "test@example.com",
	}))
}

func logoutClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub": "11111111-1111-1111-1111-111111111111",
		"jti": uuid.New().String(),
		"exp": time.Now().Add(15 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	}
}

// TestLogout_RejectsForeignAlgorithms covers the hardening: a token that is not
// HMAC-signed must never be treated as this server's own.
func TestLogout_RejectsForeignAlgorithms(t *testing.T) {
	secret := "test-secret-key-for-logout"
	h := &Handler{jwtSecret: []byte(secret), cookieSecure: true}

	t.Run("a genuine HS256 token is accepted", func(t *testing.T) {
		tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, logoutClaims()).SignedString([]byte(secret))
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if !reachedRevocation(h, logoutRequest(t, tokenStr)) {
			t.Fatal("a valid token must reach revocation; without this the other cases prove nothing")
		}
	})

	t.Run("alg none is rejected", func(t *testing.T) {
		tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodNone, logoutClaims()).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if reachedRevocation(h, logoutRequest(t, tokenStr)) {
			t.Error("an unsigned token must not be accepted")
		}
	})

	t.Run("RS256 is rejected", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate rsa key: %v", err)
		}
		tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodRS256, logoutClaims()).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if reachedRevocation(h, logoutRequest(t, tokenStr)) {
			t.Error("a token signed with another algorithm must not be accepted")
		}
	})

	t.Run("a token signed with the wrong secret is rejected", func(t *testing.T) {
		tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, logoutClaims()).
			SignedString([]byte("not-the-server-secret"))
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if reachedRevocation(h, logoutRequest(t, tokenStr)) {
			t.Error("a token signed with an unknown secret must not be accepted")
		}
	})
}

// --- Email normalization ---

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "already canonical", input: "user@example.com", want: "user@example.com"},
		{name: "uppercase", input: "USER@EXAMPLE.COM", want: "user@example.com"},
		{name: "mixed case", input: "User@Example.Com", want: "user@example.com"},
		{name: "surrounding spaces", input: "  user@example.com  ", want: "user@example.com"},
		{name: "tab and newline", input: "\tuser@example.com\n", want: "user@example.com"},
		{name: "both", input: "  User@EXAMPLE.com ", want: "user@example.com"},
		{name: "empty", input: "", want: ""},
		{name: "only spaces", input: "   ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := user.NormalizeEmail(tt.input); got != tt.want {
				t.Errorf("NormalizeEmail(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestNormalizeEmail_CaseVariantsCollapse is the property that matters: any
// spelling of one address reaches the same key, so it cannot become a second
// account or a second rate-limit bucket.
func TestNormalizeEmail_CaseVariantsCollapse(t *testing.T) {
	variants := []string{
		"user@example.com",
		"User@example.com",
		"USER@EXAMPLE.COM",
		" user@Example.com ",
		"uSeR@eXaMpLe.CoM",
	}

	want := user.NormalizeEmail(variants[0])
	for _, v := range variants {
		if got := user.NormalizeEmail(v); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", v, got, want)
		}
	}
}

// TestLoginLimiter_KeyedOnNormalizedEmail pins that case variants share one
// bucket. Keyed on the raw address, five failures per spelling would hand out a
// fresh allowance for every capitalization of the same address.
func TestLoginLimiter_KeyedOnNormalizedEmail(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{}}

	spellings := []string{"user@example.com", "User@example.com", "USER@EXAMPLE.COM", "uSeR@example.com", "User@Example.Com"}
	for _, s := range spellings {
		l.recordFailure(user.NormalizeEmail(s))
	}

	if len(l.entries) != 1 {
		t.Fatalf("entries = %d, want 1 shared bucket", len(l.entries))
	}
	if l.check(user.NormalizeEmail("USER@example.com")) {
		t.Error("five failures across spellings of one address must lock it out")
	}
}
