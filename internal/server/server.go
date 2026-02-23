package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"controlplane/internal/config"
	"controlplane/internal/health"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/response"
	"controlplane/internal/tenant"
)

// New creates and configures the HTTP server with all routes.
func New(pool *pgxpool.Pool, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health (public, no auth)
	healthHandler := health.NewHandler(pool)
	r.Get("/healthz", healthHandler.Healthz)

	// API v1 (protected by Bearer token auth)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(bearerAuth(cfg.APIToken))

		// Nodes
		nodeStore := node.NewStore(pool)
		nodeHandler := node.NewHandler(nodeStore, cfg.EncryptionKey)
		r.Route("/nodes", func(r chi.Router) {
			r.Get("/", nodeHandler.List)
			r.Post("/", nodeHandler.Create)
			r.Get("/{nodeID}", nodeHandler.Get)
			r.Delete("/{nodeID}", nodeHandler.Delete)
		})

		// Projects
		projectStore := project.NewStore(pool)
		projectHandler := project.NewHandler(projectStore)
		r.Route("/projects", func(r chi.Router) {
			r.Get("/", projectHandler.List)
			r.Post("/", projectHandler.Create)
		})

		// Tenants
		tenantStore := tenant.NewStore(pool)
		tenantHandler := tenant.NewHandler(tenantStore)
		r.Route("/tenants", func(r chi.Router) {
			r.Get("/", tenantHandler.List)
			r.Post("/", tenantHandler.Create)
			r.Get("/{tenantID}", tenantHandler.Get)
			r.Delete("/{tenantID}", tenantHandler.Delete)
		})
	})

	slog.Info("routes registered")
	return r
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
