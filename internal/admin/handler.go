package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/justinas/nosurf"

	"controlplane/internal/audit"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/tenant"
)

// Store interfaces — concrete *node.Store etc. satisfy these automatically.

type NodeStore interface {
	List(ctx context.Context) ([]node.Node, error)
	GetByID(ctx context.Context, id string) (*node.Node, error)
	Create(ctx context.Context, req node.CreateNodeRequest) (*node.Node, error)
	Update(ctx context.Context, id string, req node.UpdateNodeRequest) (*node.Node, error)
	Delete(ctx context.Context, id string) error
	CountTenants(ctx context.Context, nodeID string) (int, error)
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

type ProjectStore interface {
	List(ctx context.Context) ([]project.Project, error)
	GetByID(ctx context.Context, id string) (*project.Project, error)
	Create(ctx context.Context, req project.CreateProjectRequest) (*project.Project, error)
	Update(ctx context.Context, id string, req project.UpdateProjectRequest) (*project.Project, error)
	Delete(ctx context.Context, id string) error
	CountTenants(ctx context.Context, projectID string) (int, error)
}

type TenantStore interface {
	List(ctx context.Context) ([]tenant.Tenant, error)
	GetByID(ctx context.Context, id string) (*tenant.Tenant, error)
	Create(ctx context.Context, req tenant.CreateTenantRequest) (*tenant.Tenant, error)
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
	SetSuspended(ctx context.Context, id string) error
	SetResumed(ctx context.Context, id string) error
}

type Provisioner interface {
	Provision(tenantID, nodeID, projectID, subdomain string, ramMB int)
	Deprovision(ctx context.Context, tenantID, nodeID, subdomain string, lxcID, ramMB int) error
	Suspend(ctx context.Context, tenantID, nodeID string, lxcID int) error
	Resume(ctx context.Context, tenantID, nodeID string, lxcID int) error
	InvalidateClient(nodeID string)
}

// Handler serves the admin UI.
type Handler struct {
	tmpl             *Templates
	nodes            NodeStore
	projects         ProjectStore
	tenants          TenantStore
	auditStore       *audit.Store
	provisioner      Provisioner
	encryptionKey    string
	setupToken       string
	webauthn         *webauthn.WebAuthn
	webauthnStore    *WebAuthnStore
	webauthnSessions *webauthnSessions
	wgService        WireGuardService
	wgStore          WireGuardStore
}

// NewHandler creates a new admin Handler. Returns error if templates fail to parse.
func NewHandler(nodes NodeStore, projects ProjectStore, tenants TenantStore, auditStore *audit.Store, provisioner Provisioner, encryptionKey string, setupToken string, wa *webauthn.WebAuthn, waStore *WebAuthnStore) (*Handler, error) {
	tmpl, err := ParseTemplates()
	if err != nil {
		return nil, err
	}
	return &Handler{
		tmpl:             tmpl,
		nodes:            nodes,
		projects:         projects,
		tenants:          tenants,
		auditStore:       auditStore,
		provisioner:      provisioner,
		encryptionKey:    encryptionKey,
		setupToken:       setupToken,
		webauthn:         wa,
		webauthnStore:    waStore,
		webauthnSessions: newWebAuthnSessions(),
	}, nil
}

// SetWireGuard attaches WireGuard service and store to the admin handler.
func (h *Handler) SetWireGuard(svc WireGuardService, store WireGuardStore) {
	h.wgService = svc
	h.wgStore = store
}

// Routes returns a chi.Router with all admin routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	// CSRF protection for all non-GET/HEAD/OPTIONS routes
	r.Use(func(next http.Handler) http.Handler {
		csrf := nosurf.New(next)
		csrf.SetBaseCookie(http.Cookie{
			Path:     "/admin",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
		})
		// Exempt WebAuthn JSON endpoints (they have their own challenge-response)
		csrf.ExemptPaths(
			"/admin/webauthn/register/begin",
			"/admin/webauthn/register/finish",
			"/admin/webauthn/login/begin",
			"/admin/webauthn/login/finish",
		)
		return csrf
	})

	// Public routes (no auth)
	r.Handle("/static/*", http.StripPrefix("/admin/static/", http.FileServer(StaticFS())))
	r.Get("/login", h.loginPage)
	r.Post("/logout", h.logout)
	r.With(httprate.LimitByIP(5, time.Minute)).Post("/webauthn/register/begin", h.registerBegin)
	r.With(httprate.LimitByIP(5, time.Minute)).Post("/webauthn/register/finish", h.registerFinish)
	r.With(httprate.LimitByIP(10, time.Minute)).Post("/webauthn/login/begin", h.loginBegin)
	r.With(httprate.LimitByIP(10, time.Minute)).Post("/webauthn/login/finish", h.loginFinish)

	// Protected routes (require auth)
	r.Group(func(r chi.Router) {
		r.Use(requireAuth(h.encryptionKey))
		r.Use(maxBodySize(1 << 20)) // 1MB limit on all admin forms

		// Pages
		r.Get("/", h.dashboard)
		r.Get("/nodes", h.nodesList)
		r.Get("/nodes/{id}", h.nodeDetail)
		r.Get("/projects", h.projectsList)
		r.Get("/projects/{id}", h.projectDetail)
		r.Get("/tenants", h.tenantsList)
		r.Get("/tenants/{id}", h.tenantDetail)
		r.Get("/audit", h.auditPage)
		r.Get("/settings", h.settingsPage)

		// Node actions
		r.Post("/nodes", h.createNode)
		r.Put("/nodes/{id}", h.updateNodeAdmin)
		r.Delete("/nodes/{id}", h.deleteNodeAdmin)

		// Project actions
		r.Post("/projects", h.createProject)
		r.Put("/projects/{id}", h.updateProjectAdmin)
		r.Delete("/projects/{id}", h.deleteProjectAdmin)

		// Tenant actions
		r.Post("/tenants", h.createTenant)
		r.Delete("/tenants/{id}", h.deleteTenant)
		r.Post("/tenants/{id}/suspend", h.suspendTenant)
		r.Post("/tenants/{id}/resume", h.resumeTenant)
		r.Post("/tenants/{id}/wg-peer", h.createTenantPeer)
		r.Get("/tenants/{id}/row", h.tenantRow)

		// Network (WireGuard peers)
		r.Get("/network", h.networkPage)
		r.Post("/network/peers", h.createPeerAdmin)
		r.Get("/network/peers/{id}", h.peerDetail)
		r.Put("/network/peers/{id}", h.updatePeerAdmin)
		r.Delete("/network/peers/{id}", h.deletePeerAdmin)
		r.Post("/network/peers/{id}/enable", h.enablePeerAdmin)
		r.Post("/network/peers/{id}/disable", h.disablePeerAdmin)

		// WebAuthn credential management
		r.Delete("/webauthn/credentials/{id}", h.deleteCredential)
	})

	return r
}

// --- Shared types ---

type breadcrumb struct {
	Label string
	URL   string
}

type pageData struct {
	Title       string
	Nav         string
	Breadcrumbs []breadcrumb
	CSRFToken   string
}

// newPage creates pageData with CSRF token from the request.
func newPage(r *http.Request, title, nav string, crumbs []breadcrumb) pageData {
	return pageData{
		Title:       title,
		Nav:         nav,
		Breadcrumbs: crumbs,
		CSRFToken:   nosurf.Token(r),
	}
}

type enrichedTenant struct {
	tenant.Tenant
	ProjectName string
	NodeName    string
}

// --- Shared validation ---

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)
var subdomainRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

var reservedSubdomains = map[string]bool{
	"www": true, "api": true, "admin": true, "app": true,
	"mail": true, "smtp": true, "ftp": true, "ns1": true, "ns2": true,
	"cdn": true, "static": true, "assets": true, "media": true,
}

// --- Shared helpers ---

func (h *Handler) renderFlash(w http.ResponseWriter, name string, msg string) {
	w.Header().Set("HX-Retarget", "#flash")
	w.Header().Set("HX-Reswap", "innerHTML")
	if err := h.tmpl.RenderPartial(w, name, msg); err != nil {
		slog.Error("admin: render flash", "error", err)
	}
}

// triggerToast sets an HX-Trigger header to display a toast notification via Alpine.js.
func (h *Handler) triggerToast(w http.ResponseWriter, msg, toastType string) {
	trigger := map[string]any{
		"showToast": map[string]string{"message": msg, "type": toastType},
	}
	b, _ := json.Marshal(trigger)
	w.Header().Set("HX-Trigger", string(b))
}

func buildMaps(nodes []node.Node, projects []project.Project) (map[string]string, map[string]string) {
	nodeMap := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n.Name
	}
	projectMap := make(map[string]string, len(projects))
	for _, p := range projects {
		projectMap[p.ID] = p.Name
	}
	return nodeMap, projectMap
}

func enrichTenant(t tenant.Tenant, nodeMap, projectMap map[string]string) enrichedTenant {
	pName := projectMap[t.ProjectID]
	if pName == "" {
		pName = truncID(t.ProjectID)
	}
	nName := nodeMap[t.NodeID]
	if nName == "" {
		nName = truncID(t.NodeID)
	}
	return enrichedTenant{Tenant: t, ProjectName: pName, NodeName: nName}
}

// enrichSingle resolves node/project names for a single tenant via GetByID (not List).
func (h *Handler) enrichSingle(ctx context.Context, t tenant.Tenant) enrichedTenant {
	var pName, nName string
	if p, err := h.projects.GetByID(ctx, t.ProjectID); err != nil {
		slog.Error("admin: get project for enrich", "error", err)
	} else if p != nil {
		pName = p.Name
	}
	if n, err := h.nodes.GetByID(ctx, t.NodeID); err != nil {
		slog.Error("admin: get node for enrich", "error", err)
	} else if n != nil {
		nName = n.Name
	}
	if pName == "" {
		pName = truncID(t.ProjectID)
	}
	if nName == "" {
		nName = truncID(t.NodeID)
	}
	return enrichedTenant{Tenant: t, ProjectName: pName, NodeName: nName}
}

func truncID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	if id == "" {
		return "?"
	}
	return id
}

// maxBodySize limits request body size for all routes in the group.
func maxBodySize(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
