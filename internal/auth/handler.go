package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"controlplane/internal/response"
	"controlplane/internal/user"
)

const (
	bcryptCost            = 12
	accessTokenExpiration = 15 * time.Minute
	refreshTokenExpiry    = 30 * 24 * time.Hour // 30 days
	minPasswordLen        = 12
	maxLoginAttempts      = 5
	loginLimitWindow      = 15 * time.Minute

	// maxLimiterEntries caps the login limiter map. Generous enough that a real
	// deployment never reaches it, small enough that a flood of failed logins
	// against invented addresses cannot grow the map without limit.
	maxLimiterEntries = 10000
)

// loginLimiter tracks failed login attempts per email.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures int
	lastFail time.Time
}

func (l *loginLimiter) check(email string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.entries[email]
	if e == nil {
		return true
	}
	if time.Since(e.lastFail) > loginLimitWindow {
		delete(l.entries, email)
		return true
	}
	return e.failures < maxLoginAttempts
}

func (l *loginLimiter) recordFailure(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.entries == nil {
		l.entries = make(map[string]*limitEntry)
	}
	e := l.entries[email]
	if e == nil {
		l.makeRoomLocked()
		e = &limitEntry{}
		l.entries[email] = e
	}
	e.failures++
	e.lastFail = time.Now()
}

// makeRoomLocked keeps the map bounded between janitor sweeps, where a caller
// guessing a fresh address each time would otherwise add an entry per attempt.
// Expired entries go first; if the map is still full, the least recently failed
// entry is dropped. Dropping beats refusing to record, which would let whoever
// filled the map switch off rate limiting for everyone else.
func (l *loginLimiter) makeRoomLocked() {
	if len(l.entries) < maxLimiterEntries {
		return
	}

	l.sweepLocked(time.Now())
	if len(l.entries) < maxLimiterEntries {
		return
	}

	var oldestEmail string
	var oldestAt time.Time
	for email, e := range l.entries {
		if oldestEmail == "" || e.lastFail.Before(oldestAt) {
			oldestEmail, oldestAt = email, e.lastFail
		}
	}
	delete(l.entries, oldestEmail)
}

// sweep removes entries whose window has closed and returns how many went.
func (l *loginLimiter) sweep(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweepLocked(now)
}

func (l *loginLimiter) sweepLocked(now time.Time) int {
	evicted := 0
	for email, e := range l.entries {
		if now.Sub(e.lastFail) > loginLimitWindow {
			delete(l.entries, email)
			evicted++
		}
	}
	return evicted
}

func (l *loginLimiter) reset(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, email)
}

// userStore is the user store surface these handlers need. *user.Store
// satisfies it; the interface exists so the login and registration paths can be
// driven in tests without a database.
type userStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*user.User, error)
	GetByEmail(ctx context.Context, email string) (*user.User, error)
	Create(ctx context.Context, u *user.User) error
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error
}

type Handler struct {
	userStore         userStore
	tokenStore        *TokenStore
	jwtSecret         []byte
	registrationToken string
	cookieSecure      bool
	limiter           loginLimiter
}

func NewHandler(userStore *user.Store, tokenStore *TokenStore, jwtSecret, registrationToken string, cookieSecure bool) *Handler {
	if registrationToken != "" {
		slog.Info("registration gated by REGISTRATION_TOKEN")
	} else {
		slog.Warn("REGISTRATION_TOKEN not set — registration is open to anyone")
	}
	return &Handler{
		userStore:         userStore,
		tokenStore:        tokenStore,
		jwtSecret:         []byte(jwtSecret),
		registrationToken: registrationToken,
		cookieSecure:      cookieSecure,
	}
}

type registerRequest struct {
	Email             string `json:"email"`
	Password          string `json:"password"`
	DisplayName       string `json:"display_name"`
	RegistrationToken string `json:"registration_token"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Token        string    `json:"token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	User         *userView `json:"user"`
}

type userView struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
}

func toUserView(u *user.User) *userView {
	return &userView{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
	}
}

// Register handles POST /api/v1/auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Check invite token if configured (from body or header)
	if h.registrationToken != "" {
		provided := req.RegistrationToken
		if provided == "" {
			provided = r.Header.Get("X-Registration-Token")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(h.registrationToken)) != 1 {
			response.Error(w, http.StatusForbidden, "registration is not open")
			return
		}
	}

	// Validate the address, then keep the address itself. ParseAddress also
	// accepts the "Display Name <addr@example.com>" form, and storing that
	// whole string as the email would make the account unreachable by a plain
	// login. The parsed address is then normalized for storage.
	addr, err := mail.ParseAddress(req.Email)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid email format")
		return
	}
	email := user.NormalizeEmail(addr.Address)

	// Validate password
	if len(req.Password) < minPasswordLen {
		response.Error(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		slog.Error("hash password", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to process registration")
		return
	}

	u := &user.User{
		Email:        email,
		PasswordHash: string(hash),
		DisplayName:  req.DisplayName,
	}

	if err := h.userStore.Create(r.Context(), u); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			response.Error(w, http.StatusConflict, "email already registered")
			return
		}
		slog.Error("create user", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	resp, err := h.issueTokenPair(r.Context(), u)
	if err != nil {
		slog.Error("issue tokens", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to generate tokens")
		return
	}

	h.setAuthCookies(w, resp.Token, resp.RefreshToken)
	response.JSON(w, http.StatusCreated, resp)
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		response.Error(w, http.StatusBadRequest, "email and password are required")
		return
	}

	// Normalized once, then used for both the lookup and the rate limit key.
	// Keying the limiter on the raw address would let someone walk through
	// case variants of one address to get a fresh allowance each time.
	email := user.NormalizeEmail(req.Email)

	// Rate limit per email
	if !h.limiter.check(email) {
		response.Error(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	u, err := h.userStore.GetByEmail(r.Context(), email)
	if err != nil {
		slog.Error("get user by email", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to process login")
		return
	}
	if u == nil {
		h.limiter.recordFailure(email)
		response.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		h.limiter.recordFailure(email)
		response.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	// Successful login — reset counter
	h.limiter.reset(email)

	resp, err := h.issueTokenPair(r.Context(), u)
	if err != nil {
		slog.Error("issue tokens", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to generate tokens")
		return
	}

	h.setAuthCookies(w, resp.Token, resp.RefreshToken)
	response.JSON(w, http.StatusOK, resp)
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	// Try reading from the request body (ignore the error — the body may be empty)
	_ = response.Decode(r, &req)

	// Fallback: read refresh_token from the cookie
	refreshToken := req.RefreshToken
	if refreshToken == "" {
		if c, err := r.Cookie("refresh_token"); err == nil {
			refreshToken = c.Value
		}
	}
	if refreshToken == "" {
		response.Error(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	tokenHash := HashToken(refreshToken)

	userID, err := h.tokenStore.ValidateRefreshToken(r.Context(), tokenHash)
	if err != nil {
		response.Error(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	// Revoke old refresh token (rotation)
	if err := h.tokenStore.RevokeRefreshToken(r.Context(), tokenHash); err != nil {
		slog.Error("revoke old refresh token", "error", err)
	}

	u, err := h.userStore.GetByID(r.Context(), userID)
	if err != nil || u == nil {
		response.Error(w, http.StatusUnauthorized, "user not found")
		return
	}

	resp, err := h.issueTokenPair(r.Context(), u)
	if err != nil {
		slog.Error("issue tokens on refresh", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to generate tokens")
		return
	}

	h.setAuthCookies(w, resp.Token, resp.RefreshToken)
	response.JSON(w, http.StatusOK, resp)
}

// Logout handles POST /api/v1/auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Extract jti from current access token and revoke it
	authHeader := r.Header.Get("Authorization")
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenStr == "" || tokenStr == authHeader {
		// Fallback: read from the cookie
		if c, err := r.Cookie("access_token"); err == nil {
			tokenStr = c.Value
		}
	}

	token, err := jwt.Parse(tokenStr, hmacKeyfunc(h.jwtSecret))
	if err == nil {
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if jti, _ := claims["jti"].(string); jti != "" {
				exp, _ := claims["exp"].(float64)
				expiresAt := time.Unix(int64(exp), 0)
				if err := h.tokenStore.Revoke(r.Context(), jti, u.ID, expiresAt); err != nil {
					slog.Error("revoke access token", "error", err)
				}
			}
		}
	}

	// Also revoke the refresh token if provided (body or cookie)
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = response.Decode(r, &req)
	refreshTokenValue := req.RefreshToken
	if refreshTokenValue == "" {
		if c, err := r.Cookie("refresh_token"); err == nil {
			refreshTokenValue = c.Value
		}
	}
	if refreshTokenValue != "" {
		tokenHash := HashToken(refreshTokenValue)
		if err := h.tokenStore.RevokeRefreshToken(r.Context(), tokenHash); err != nil {
			slog.Error("revoke refresh token on logout", "error", err)
		}
	}

	h.clearAuthCookies(w)
	response.JSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// ChangePassword handles POST /api/v1/auth/password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.OldPassword == "" || req.NewPassword == "" {
		response.Error(w, http.StatusBadRequest, "old_password and new_password are required")
		return
	}

	if len(req.NewPassword) < minPasswordLen {
		response.Error(w, http.StatusBadRequest, "new password must be at least 12 characters")
		return
	}

	// Verify old password
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.OldPassword)); err != nil {
		response.Error(w, http.StatusUnauthorized, "incorrect current password")
		return
	}

	// Hash new password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcryptCost)
	if err != nil {
		slog.Error("hash new password", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	if err := h.userStore.UpdatePassword(r.Context(), u.ID, string(hash)); err != nil {
		slog.Error("update password", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	// Revoke all existing refresh tokens — force re-login on all devices
	if err := h.tokenStore.RevokeAllUserRefreshTokens(r.Context(), u.ID); err != nil {
		slog.Error("revoke tokens on password change", "error", err)
	}

	// Issue new token pair for current session
	resp, err := h.issueTokenPair(r.Context(), u)
	if err != nil {
		slog.Error("issue tokens after password change", "error", err)
		response.Error(w, http.StatusInternalServerError, "password updated but failed to generate new tokens")
		return
	}

	h.setAuthCookies(w, resp.Token, resp.RefreshToken)
	response.JSON(w, http.StatusOK, resp)
}

// Me handles GET /api/v1/auth/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	response.JSON(w, http.StatusOK, toUserView(u))
}

// issueTokenPair generates an access token + refresh token pair.
func (h *Handler) issueTokenPair(ctx context.Context, u *user.User) (*authResponse, error) {
	accessToken, err := h.generateAccessToken(u)
	if err != nil {
		return nil, err
	}

	refreshToken, err := GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	tokenHash := HashToken(refreshToken)
	expiresAt := time.Now().Add(refreshTokenExpiry)
	if err := h.tokenStore.CreateRefreshToken(ctx, u.ID, tokenHash, expiresAt); err != nil {
		return nil, err
	}

	return &authResponse{
		Token:        accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(accessTokenExpiration.Seconds()),
		User:         toUserView(u),
	}, nil
}

// setAuthCookies sets httpOnly cookies for access and refresh tokens.
func (h *Handler) setAuthCookies(w http.ResponseWriter, accessToken, refreshToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(accessTokenExpiration.Seconds()), // 15 minutes
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    refreshToken,
		Path:     "/api/v1/auth/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(refreshTokenExpiry.Seconds()), // 30 days
	})
}

// clearAuthCookies removes auth cookies by setting MaxAge=-1.
func (h *Handler) clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/v1/auth/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) generateAccessToken(u *user.User) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   u.ID.String(),
		"email": u.Email,
		"jti":   uuid.New().String(),
		"exp":   now.Add(accessTokenExpiration).Unix(),
		"iat":   now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.jwtSecret)
}
