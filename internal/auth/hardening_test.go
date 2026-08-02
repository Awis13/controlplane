package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// --- Login through the handler ---

// fakeUserStore lets the login and registration paths run without a database.
type fakeUserStore struct {
	byEmail map[string]*user.User
	created []*user.User
}

func (f *fakeUserStore) GetByEmail(_ context.Context, email string) (*user.User, error) {
	return f.byEmail[email], nil
}

func (f *fakeUserStore) GetByID(context.Context, uuid.UUID) (*user.User, error) { return nil, nil }

func (f *fakeUserStore) Create(_ context.Context, u *user.User) error {
	f.created = append(f.created, u)
	return nil
}

func (f *fakeUserStore) UpdatePassword(context.Context, uuid.UUID, string) error { return nil }

func newLoginRequest(email, password string) *http.Request {
	body := `{"email":` + strconv.Quote(email) + `,"password":` + strconv.Quote(password) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestLogin_LimiterSharesOneBucketAcrossSpellings drives the real handler. The
// limiter must key on the normalized address, or walking through
// capitalizations of one address hands out a fresh allowance each time and the
// lockout never arrives.
func TestLogin_LimiterSharesOneBucketAcrossSpellings(t *testing.T) {
	h := &Handler{
		userStore: &fakeUserStore{}, // no such user, so every attempt fails
		jwtSecret: []byte("test-secret"),
	}

	spellings := []string{
		"user@example.com",
		"User@example.com",
		"USER@EXAMPLE.COM",
		"uSeR@example.com",
		" User@Example.Com ",
	}
	if len(spellings) != maxLoginAttempts {
		t.Fatalf("this test assumes %d attempts exhaust the allowance, got %d spellings", maxLoginAttempts, len(spellings))
	}

	for i, spelling := range spellings {
		w := httptest.NewRecorder()
		h.Login(w, newLoginRequest(spelling, "wrong-password"))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d with %q: status = %d, want %d", i+1, spelling, w.Code, http.StatusUnauthorized)
		}
	}

	// The allowance is spent, whichever spelling is used next.
	w := httptest.NewRecorder()
	h.Login(w, newLoginRequest("USER@example.com", "wrong-password"))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d: the spellings must share one bucket", w.Code, http.StatusTooManyRequests)
	}

	if len(h.limiter.entries) != 1 {
		t.Errorf("limiter entries = %d, want 1", len(h.limiter.entries))
	}
}

// TestLogin_FindsUserByAnySpelling pins the other half: the lookup key is
// normalized, so a user stored canonically is found however they type it.
func TestLogin_FindsUserByAnySpelling(t *testing.T) {
	store := &fakeUserStore{byEmail: map[string]*user.User{
		"user@example.com": {ID: uuid.New(), Email: "user@example.com", PasswordHash: "not-a-real-hash"},
	}}
	h := &Handler{userStore: store, jwtSecret: []byte("test-secret")}

	for _, spelling := range []string{"user@example.com", "USER@EXAMPLE.COM", " User@Example.Com "} {
		h.limiter.reset(user.NormalizeEmail(spelling))

		w := httptest.NewRecorder()
		h.Login(w, newLoginRequest(spelling, "wrong-password"))

		// 401 means the user was found and the password compared. A lookup with
		// the raw spelling would miss the map and also give 401, so the
		// distinguishing check is that the limiter recorded against one key.
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%q: status = %d, want %d", spelling, w.Code, http.StatusUnauthorized)
		}
	}

	if len(h.limiter.entries) != 1 {
		t.Errorf("limiter entries = %d, want every spelling to reach the same key", len(h.limiter.entries))
	}
}

// TestRegister_StoresParsedAddress pins the display-name fix: ParseAddress
// accepts "Bob <bob@example.com>", and storing that whole string would leave an
// account nobody can log into.
func TestRegister_StoresParsedAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "display name form", input: "Bob <bob@example.com>", want: "bob@example.com"},
		{name: "display name with mixed case", input: "Bob <Bob@Example.COM>", want: "bob@example.com"},
		{name: "plain address", input: "bob@example.com", want: "bob@example.com"},
		{name: "plain address with spaces", input: "  Bob@Example.com  ", want: "bob@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeUserStore{}
			h := &Handler{userStore: store, jwtSecret: []byte("test-secret")}

			body := `{"email":` + strconv.Quote(tt.input) + `,"password":"a-long-enough-password","display_name":"Bob"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			// issueTokenPair needs a token store, so the response is not
			// reachable here; the stored record is what this test is about.
			func() {
				defer func() { recover() }()
				h.Register(httptest.NewRecorder(), req)
			}()

			if len(store.created) != 1 {
				t.Fatalf("created %d users, want 1", len(store.created))
			}
			if got := store.created[0].Email; got != tt.want {
				t.Errorf("stored email = %q, want %q", got, tt.want)
			}
		})
	}
}
