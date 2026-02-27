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

	"controlplane/internal/admin"
	"controlplane/internal/audit"
	"controlplane/internal/config"
	"controlplane/internal/health"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/provisioner"
	"controlplane/internal/response"
	"controlplane/internal/tenant"
)

// New creates and configures the HTTP server with all routes.
// The returned Provisioner should be shut down via Shutdown() during graceful shutdown.
func New(pool *pgxpool.Pool, cfg *config.Config) (http.Handler, *provisioner.Provisioner, error) {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	// Shared stores
	nodeStore := node.NewStore(pool)
	projectStore := project.NewStore(pool)
	tenantStore := tenant.NewStore(pool)
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

	slog.Info("routes registered")
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
