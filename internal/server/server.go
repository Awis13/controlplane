package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"os"
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
	"controlplane/internal/caddy"
	"controlplane/internal/config"
	"controlplane/internal/health"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/provisioner"
	"controlplane/internal/sshexec"
	"controlplane/internal/response"
	"controlplane/internal/station"
	"controlplane/internal/tenant"
	"controlplane/internal/user"
	"controlplane/internal/wireguard"
)

// New creates and configures the HTTP server with all routes.
// The returned Provisioner should be shut down via Shutdown() during graceful shutdown.
// The returned Poller should be started in a goroutine and stopped via context cancellation.
func New(pool *pgxpool.Pool, cfg *config.Config) (http.Handler, *provisioner.Provisioner, *station.Poller, error) {
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

	// Station auto-creation on provisioning
	stationCreator := station.NewCreator(stationStore)
	prov.WithStationCreator(stationCreator, cfg.CaddyDomain)

	// SSH exec for writing dashboard token into container
	if cfg.SSHKeyPath != "" {
		if _, err := os.Stat(cfg.SSHKeyPath); err != nil {
			slog.Warn("SSH key not found, dashboard token provisioning disabled", "path", cfg.SSHKeyPath, "error", err)
		} else {
			sshClient := sshexec.NewClient(cfg.SSHKeyPath)
			prov.WithSSHClient(sshClient)
			slog.Info("sshexec: enabled", "key_path", cfg.SSHKeyPath)
		}
	}

	// Station status poller
	pollerTenantAdapter := &pollerTenantStoreAdapter{store: tenantStore}
	poller := station.NewPoller(pollerTenantAdapter, stationStore, cfg.PollerInterval)

	// Caddy dynamic routing (optional)
	if cfg.CaddyAdminURL != "" {
		caddyClient := caddy.NewClient(cfg.CaddyAdminURL, cfg.CaddyServerName, cfg.CaddyDomain)
		prov.WithCaddyClient(caddyClient)

		// Reconcile routes on startup (background goroutine)
		go func() {
			adapter := &tenantStoreAdapter{store: tenantStore}
			result, err := caddy.Reconcile(context.Background(), caddyClient, adapter)
			if err != nil {
				slog.Warn("caddy: reconciliation failed", "error", err)
			} else {
				slog.Info("caddy: route reconciliation complete",
					"success", result.Success, "failed", result.Failed)
			}
		}()

		slog.Info("caddy: dynamic routing enabled",
			"admin_url", cfg.CaddyAdminURL, "server", cfg.CaddyServerName, "domain", cfg.CaddyDomain)
	} else {
		slog.Info("caddy: dynamic routing disabled (CADDY_ADMIN_URL not set)")
	}

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
		return nil, nil, nil, fmt.Errorf("webauthn: %w", err)
	}
	waStore := admin.NewWebAuthnStore(pool)

	// Admin UI (auth via WebAuthn)
	adminHandler, err := admin.NewHandler(nodeStore, projectStore, tenantStore, auditStore, prov, cfg.EncryptionKey, cfg.SetupToken, wa, waStore)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("admin handler: %w", err)
	}
	// WireGuard peer management (optional — requires WG_HUB_PUBLIC_KEY)
	wgStore := wireguard.NewStore(pool)
	if cfg.WGHubPublicKey != "" {
		wgService := wireguard.NewService(wgStore, cfg.EncryptionKey, cfg.WGHubPublicKey, cfg.WGHubEndpoint, cfg.WGNetworkCIDR)
		adminHandler.SetWireGuard(wgService, wgStore)

		// Register WireGuard API handlers
		wgHandler := wireguard.NewHandler(wgService, auditStore)
		r.Route("/api/v1/wireguard", func(r chi.Router) {
			r.Use(bearerAuth(cfg.APIToken))
			r.Get("/peers", wgHandler.ListPeers)
			r.Post("/peers", wgHandler.CreatePeer)
			r.Get("/peers/{id}", wgHandler.GetPeer)
			r.Put("/peers/{id}", wgHandler.UpdatePeer)
			r.Delete("/peers/{id}", wgHandler.DeletePeer)
			r.Post("/peers/{id}/enable", wgHandler.EnablePeer)
			r.Post("/peers/{id}/disable", wgHandler.DisablePeer)
			r.Get("/peers/{id}/config", wgHandler.GetPeerConfig)
			r.Get("/peers/{id}/qr", wgHandler.GetPeerQR)
		})

		// Sync peers with wg0 on startup
		go func() {
			if err := wgService.SyncPeers(context.Background()); err != nil {
				slog.Warn("wireguard: initial sync failed", "error", err)
			} else {
				slog.Info("wireguard: initial peer sync complete")
			}
		}()

		slog.Info("wireguard: module enabled", "network", cfg.WGNetworkCIDR, "hub_endpoint", cfg.WGHubEndpoint)
	} else {
		slog.Info("wireguard: module disabled (WG_HUB_PUBLIC_KEY not set)")
	}

	r.Mount("/admin", adminHandler.Routes())

	// Stations — public read endpoints (no auth)
	stationHandler := station.NewHandler(stationStore, auditStore)
	stationHandler.WithPoller(poller)
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
	tokenStore := auth.NewTokenStore(pool)
	authHandler := auth.NewHandler(userStore, tokenStore, cfg.JWTSecret, cfg.RegistrationToken)
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, time.Minute))
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)

		// Protected by JWT
		r.Group(func(r chi.Router) {
			r.Use(auth.JWTAuth(userStore, tokenStore, cfg.JWTSecret))
			r.Get("/me", authHandler.Me)
			r.Post("/logout", authHandler.Logout)
		})
	})

	// User tenant management (JWT-protected, auto-select project+node)
	userTenantHandler := tenant.NewUserHandler(tenantStore, nodeStore, projectStore, prov, auditStore, cfg.SSODomain)
	r.Route("/api/v1/user/tenants", func(r chi.Router) {
		r.Use(httprate.LimitByIP(20, time.Minute))
		r.Use(auth.JWTAuth(userStore, tokenStore, cfg.JWTSecret))
		r.Get("/", userTenantHandler.List)
		r.Post("/", userTenantHandler.Create)
		r.Get("/{tenantID}", userTenantHandler.Get)
		r.Delete("/{tenantID}", userTenantHandler.Delete)
		r.Post("/{tenantID}/suspend", userTenantHandler.Suspend)
		r.Post("/{tenantID}/resume", userTenantHandler.Resume)
		r.Post("/{tenantID}/sso-token", userTenantHandler.SSOToken)
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
	return r, prov, poller, nil
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

// tenantStoreAdapter wraps *tenant.Store to satisfy caddy.TenantLister
// without creating a circular import (tenant doesn't import caddy).
type tenantStoreAdapter struct {
	store *tenant.Store
}

func (a *tenantStoreAdapter) ListActiveWithIP(ctx context.Context) ([]caddy.TenantRoute, error) {
	tenants, err := a.store.ListActiveWithIP(ctx)
	if err != nil {
		return nil, err
	}
	routes := make([]caddy.TenantRoute, len(tenants))
	for i, t := range tenants {
		routes[i] = caddy.TenantRoute{Subdomain: t.Subdomain, LXCIP: t.LXCIP}
	}
	return routes, nil
}

// pollerTenantStoreAdapter wraps *tenant.Store to satisfy station.PollerTenantLister.
type pollerTenantStoreAdapter struct {
	store *tenant.Store
}

func (a *pollerTenantStoreAdapter) ListPollable(ctx context.Context) ([]tenant.PollableTenant, error) {
	return a.store.ListPollable(ctx)
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
