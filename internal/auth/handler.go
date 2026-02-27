package auth

import (
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
	bcryptCost       = 12
	tokenExpiration  = 24 * time.Hour
	minPasswordLen   = 12
	maxLoginAttempts = 5
	loginLimitWindow = 15 * time.Minute
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
		e = &limitEntry{}
		l.entries[email] = e
	}
	e.failures++
	e.lastFail = time.Now()
}

func (l *loginLimiter) reset(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, email)
}

type Handler struct {
	userStore         *user.Store
	tokenStore        *TokenStore
	jwtSecret         []byte
	registrationToken string
	limiter           loginLimiter
}

func NewHandler(userStore *user.Store, tokenStore *TokenStore, jwtSecret, registrationToken string) *Handler {
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
	}
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Token string    `json:"token"`
	User  *userView `json:"user"`
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
	// Проверяем invite token если задан
	if h.registrationToken != "" {
		provided := r.Header.Get("X-Registration-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(h.registrationToken)) != 1 {
			response.Error(w, http.StatusForbidden, "registration is not open")
			return
		}
	}

	var req registerRequest
	if err := response.Decode(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate email
	if _, err := mail.ParseAddress(req.Email); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid email format")
		return
	}

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
		Email:        req.Email,
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

	token, err := h.generateToken(u)
	if err != nil {
		slog.Error("generate token", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	response.JSON(w, http.StatusCreated, authResponse{
		Token: token,
		User:  toUserView(u),
	})
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

	// Rate limit per email
	if !h.limiter.check(req.Email) {
		response.Error(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	u, err := h.userStore.GetByEmail(r.Context(), req.Email)
	if err != nil {
		slog.Error("get user by email", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to process login")
		return
	}
	if u == nil {
		h.limiter.recordFailure(req.Email)
		response.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		h.limiter.recordFailure(req.Email)
		response.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	// Успешный логин — сбрасываем счётчик
	h.limiter.reset(req.Email)

	token, err := h.generateToken(u)
	if err != nil {
		slog.Error("generate token", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	response.JSON(w, http.StatusOK, authResponse{
		Token: token,
		User:  toUserView(u),
	})
}

// Logout handles POST /api/v1/auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Извлекаем jti из текущего токена
	authHeader := r.Header.Get("Authorization")
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		return h.jwtSecret, nil
	})
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid token")
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		response.Error(w, http.StatusBadRequest, "invalid token claims")
		return
	}

	jti, _ := claims["jti"].(string)
	if jti == "" {
		// Старый токен без jti — просто OK
		response.JSON(w, http.StatusOK, map[string]string{"status": "logged out"})
		return
	}

	exp, _ := claims["exp"].(float64)
	expiresAt := time.Unix(int64(exp), 0)

	if err := h.tokenStore.Revoke(r.Context(), jti, u.ID, expiresAt); err != nil {
		slog.Error("revoke token", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "logged out"})
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

func (h *Handler) generateToken(u *user.User) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   u.ID.String(),
		"email": u.Email,
		"jti":   uuid.New().String(),
		"exp":   now.Add(tokenExpiration).Unix(),
		"iat":   now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.jwtSecret)
}
