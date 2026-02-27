package server

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/cors"

	"controlplane/internal/admin"
	"controlplane/internal/audit"
	"controlplane/internal/auth"
	"controlplane/internal/config"
	"controlplane/internal/health"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/provisioner"
	"controlplane/internal/response"
	"controlplane/internal/station"
	"controlplane/internal/tenant"
	"controlplane/internal/user"
)

// New creates and configures the HTTP server with all routes.
// The returned Provisioner should be shut down via Shutdown() during graceful shutdown.
func New(pool *pgxpool.Pool, cfg *config.Config) (http.Handler, *provisioner.Provisioner, error) {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.New(cors.Options{
		AllowedOrigins: cfg.CORSOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         300,
	}).Handler)
	r.Use(securityHeaders)

	// Shared stores
	nodeStore := node.NewStore(pool)
	projectStore := project.NewStore(pool)
	tenantStore := tenant.NewStore(pool)
	stationStore := station.NewStore(pool)
	userStore := user.NewStore(pool)
	auditStore := audit.NewStore(pool)
	prov := provisioner.New(nodeStore, tenantStore, projectStore, cfg.EncryptionKey)

	// Health (public, no auth)
	healthHandler := health.NewHandler(pool)
	r.Get("/healthz", healthHandler.Healthz)

	// WebAuthn setup
	rpID := cfg.WebAuthnRPID
	if rpID == "" {
		rpID = "localhost"
		slog.Warn("WEBAUTHN_RPID not set, defaulting to localhost — do not use in production")
	}
	rpOrigin := cfg.WebAuthnOrigin
	if rpOrigin == "" {
		rpOrigin = "https://" + rpID
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "Control Plane",
		RPOrigins:     []string{rpOrigin},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("webauthn: %w", err)
	}
	waStore := admin.NewWebAuthnStore(pool)

	// Admin UI (auth via WebAuthn)
	adminHandler, err := admin.NewHandler(nodeStore, projectStore, tenantStore, auditStore, prov, cfg.EncryptionKey, cfg.SetupToken, wa, waStore)
	if err != nil {
		return nil, nil, fmt.Errorf("admin handler: %w", err)
	}
	r.Mount("/admin", adminHandler.Routes())

	// Stations — public read endpoints (no auth)
	stationHandler := station.NewHandler(stationStore, auditStore)
	r.Route("/api/v1/stations", func(r chi.Router) {
		r.Use(httprate.LimitByIP(100, time.Minute))

		// Public (no auth)
		r.Get("/", stationHandler.List)
		r.Get("/{slug}", stationHandler.GetBySlug)

		// Protected (auth required)
		r.Group(func(r chi.Router) {
			r.Use(bearerAuth(cfg.APIToken))
			r.Post("/", stationHandler.Create)
			r.Put("/{stationID}", stationHandler.Update)
			r.Delete("/{stationID}", stationHandler.Delete)
		})
	})

	// User auth (JWT-based, separate from admin WebAuthn and API Bearer token)
	authHandler := auth.NewHandler(userStore, cfg.JWTSecret)
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, time.Minute))
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)

		// Protected by JWT
		r.Group(func(r chi.Router) {
			r.Use(auth.JWTAuth(userStore, cfg.JWTSecret))
			r.Get("/me", authHandler.Me)
		})
	})

	// User tenant management (JWT-protected, auto-select project+node)
	userTenantHandler := tenant.NewUserHandler(tenantStore, nodeStore, projectStore, prov)
	r.Route("/api/v1/user/tenants", func(r chi.Router) {
		r.Use(httprate.LimitByIP(20, time.Minute))
		r.Use(auth.JWTAuth(userStore, cfg.JWTSecret))
		r.Get("/", userTenantHandler.List)
		r.Post("/", userTenantHandler.Create)
		r.Get("/{tenantID}", userTenantHandler.Get)
	})

	// API v1 (protected by Bearer token auth)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(httprate.LimitByIP(100, time.Minute))
		r.Use(bearerAuth(cfg.APIToken))

		// Nodes
		nodeHandler := node.NewHandler(nodeStore, auditStore, cfg.EncryptionKey, prov)
		r.Route("/nodes", func(r chi.Router) {
			r.Get("/", nodeHandler.List)
			r.Post("/", nodeHandler.Create)
			r.Get("/{nodeID}", nodeHandler.Get)
			r.Put("/{nodeID}", nodeHandler.Update)
			r.Delete("/{nodeID}", nodeHandler.Delete)
		})

		// Projects
		projectHandler := project.NewHandler(projectStore, auditStore)
		r.Route("/projects", func(r chi.Router) {
			r.Get("/", projectHandler.List)
			r.Post("/", projectHandler.Create)
			r.Get("/{projectID}", projectHandler.Get)
			r.Put("/{projectID}", projectHandler.Update)
			r.Delete("/{projectID}", projectHandler.Delete)
		})

		// Tenants
		tenantHandler := tenant.NewHandler(tenantStore, nodeStore, projectStore, prov, auditStore)
		r.Route("/tenants", func(r chi.Router) {
			r.Get("/", tenantHandler.List)
			r.Post("/", tenantHandler.Create)
			r.Get("/{tenantID}", tenantHandler.Get)
			r.Put("/{tenantID}", tenantHandler.Update)
			r.Delete("/{tenantID}", tenantHandler.Delete)
			r.Post("/{tenantID}/suspend", tenantHandler.Suspend)
			r.Post("/{tenantID}/resume", tenantHandler.Resume)
		})
	})

	slog.Info("routes registered", "cors_origins", cfg.CORSOrigins)
	return r, prov, nil
}

// securityHeaders adds standard security headers to all responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

// bearerAuth returns middleware that validates Authorization: Bearer <token> header.
func bearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				response.Error(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				response.Error(w, http.StatusUnauthorized, "invalid authorization header format")
				return
			}

			provided := strings.TrimPrefix(authHeader, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				response.Error(w, http.StatusUnauthorized, "invalid token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
