package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"controlplane/internal/health"
	"controlplane/internal/node"
	"controlplane/internal/tenant"
)

// New creates and configures the HTTP server with all routes.
func New(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health
	healthHandler := health.NewHandler(pool)
	r.Get("/healthz", healthHandler.Healthz)

	// API v1
	r.Route("/api/v1", func(r chi.Router) {
		// Nodes
		nodeStore := node.NewStore(pool)
		nodeHandler := node.NewHandler(nodeStore)
		r.Route("/nodes", func(r chi.Router) {
			r.Get("/", nodeHandler.List)
			r.Post("/", nodeHandler.Create)
			r.Get("/{nodeID}", nodeHandler.Get)
			r.Delete("/{nodeID}", nodeHandler.Delete)
		})

		// Projects
		projectStore := tenant.NewProjectStore(pool)
		projectHandler := tenant.NewProjectHandler(projectStore)
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
