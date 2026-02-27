package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"controlplane/internal/response"
	"controlplane/internal/user"
)

const (
	bcryptCost      = 12
	tokenExpiration = 24 * time.Hour
	minPasswordLen  = 8
)

type Handler struct {
	userStore *user.Store
	jwtSecret []byte
}

func NewHandler(userStore *user.Store, jwtSecret string) *Handler {
	return &Handler{
		userStore: userStore,
		jwtSecret: []byte(jwtSecret),
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
		response.Error(w, http.StatusBadRequest, "password must be at least 8 characters")
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

	u, err := h.userStore.GetByEmail(r.Context(), req.Email)
	if err != nil {
		slog.Error("get user by email", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to process login")
		return
	}
	if u == nil {
		response.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		response.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

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
		"exp":   now.Add(tokenExpiration).Unix(),
		"iat":   now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.jwtSecret)
}
