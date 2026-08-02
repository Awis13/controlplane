package tenant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/node"
	"controlplane/internal/project"
)

// The lifecycle service owns tenant creation and deletion so the user, API and
// admin entry points share one implementation. Each entry point keeps what is
// genuinely its own -- authentication, request decoding, how a node and project
// are chosen, and how a result is rendered -- and delegates the rest.

// LifecycleTenantStore is the tenant store surface the lifecycle needs. It is
// deliberately narrow so every caller's store satisfies it.
type LifecycleTenantStore interface {
	Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error)
	GetByID(ctx context.Context, id string) (*Tenant, error)
	SetDeleting(ctx context.Context, id string) error
	SetDeleted(ctx context.Context, id string) error
}

// LifecycleNodeStore covers the RAM accounting the lifecycle performs.
type LifecycleNodeStore interface {
	ReserveRAM(ctx context.Context, nodeID string, ramMB int) error
	ReleaseRAM(ctx context.Context, nodeID string, ramMB int) error
}

// LifecycleProjectStore is used to look up how much RAM to release on delete.
type LifecycleProjectStore interface {
	GetByID(ctx context.Context, id string) (*project.Project, error)
}

// LifecycleProvisioner is the provisioning surface the lifecycle drives.
type LifecycleProvisioner interface {
	Provision(tenantID, nodeID, projectID, subdomain string, ramMB int)
	Deprovision(ctx context.Context, tenantID, nodeID, subdomain string, lxcID, ramMB int) error
}

// AuditLogger records lifecycle actions. *audit.Store implements it; tests
// substitute their own recorder.
type AuditLogger interface {
	Log(ctx context.Context, action, entityType, entityID string, details any)
}

// normalizeAuditLogger collapses a nil *audit.Store into a nil interface. An
// interface holding a nil pointer is itself non-nil, so without this a caller
// passing a nil store would defeat the nil check and panic inside Log.
func normalizeAuditLogger(a AuditLogger) AuditLogger {
	if a == nil {
		return nil
	}
	if store, ok := a.(*audit.Store); ok && store == nil {
		return nil
	}
	return a
}

// OwnerCreator is implemented by stores that can create owner-scoped tenants.
// Only the user path needs it; the real store implements it, so a caller that
// passes an owner without an owner-capable store is a programming error.
type OwnerCreator interface {
	CreateWithOwner(ctx context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error)
}

// FailureKind classifies a lifecycle failure so each entry point can translate
// it into whatever it speaks: an HTTP status for the APIs, a flash message for
// the admin UI.
type FailureKind int

const (
	FailureInternal FailureKind = iota
	FailureInvalid
	FailureConflict
)

// LifecycleError carries the client-facing message alongside the underlying
// cause, which stays on the server side for logging.
type LifecycleError struct {
	Kind    FailureKind
	Message string
	Err     error
}

func (e *LifecycleError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *LifecycleError) Unwrap() error { return e.Err }

func invalid(message string) *LifecycleError {
	return &LifecycleError{Kind: FailureInvalid, Message: message}
}

func conflict(message string, err error) *LifecycleError {
	return &LifecycleError{Kind: FailureConflict, Message: message, Err: err}
}

func internal(message string, err error) *LifecycleError {
	return &LifecycleError{Kind: FailureInternal, Message: message, Err: err}
}

// Actor identifies who triggered a lifecycle action. A user-initiated action
// carries the user ID into the audit metadata; admin and API actions do not
// have one, and their audit entries stay as they are.
type Actor struct {
	UserID string
}

// deletableStatuses are the tenant states a delete may start from. Suspended is
// included: a suspended tenant is still billable, so the owner must be able to
// get rid of it.
var deletableStatuses = map[string]bool{
	"active":    true,
	"error":     true,
	"suspended": true,
}

// LifecycleService creates and deletes tenants.
type LifecycleService struct {
	store        LifecycleTenantStore
	nodeStore    LifecycleNodeStore
	projectStore LifecycleProjectStore
	provisioner  LifecycleProvisioner
	auditStore   AuditLogger
}

// NewLifecycleService creates a lifecycle service. auditStore may be nil, in
// which case actions are not audited.
func NewLifecycleService(store LifecycleTenantStore, nodeStore LifecycleNodeStore, projectStore LifecycleProjectStore, provisioner LifecycleProvisioner, auditStore AuditLogger) *LifecycleService {
	return &LifecycleService{
		store:        store,
		nodeStore:    nodeStore,
		projectStore: projectStore,
		provisioner:  provisioner,
		auditStore:   normalizeAuditLogger(auditStore),
	}
}

// ValidateSubdomain applies the subdomain rules shared by every entry point.
func ValidateSubdomain(subdomain string) *LifecycleError {
	if len(subdomain) > 63 || !subdomainRegexp.MatchString(subdomain) {
		return invalid("invalid subdomain: must be lowercase alphanumeric with hyphens, 2-63 chars")
	}
	if reservedSubdomains[subdomain] {
		return invalid("subdomain is reserved")
	}
	return nil
}

// CreateParams describes a tenant to create. The caller resolves the project
// and node first, whether from an explicit request or by auto-selection.
type CreateParams struct {
	Name      string
	Subdomain string
	Project   *project.Project
	Node      *node.Node
	OwnerID   string // empty for tenants created without an owner
}

// Create reserves capacity, records the tenant and starts provisioning. On a
// failed insert the reserved RAM is released before the error is returned.
func (s *LifecycleService) Create(ctx context.Context, params CreateParams, actor Actor) (*Tenant, *LifecycleError) {
	if err := ValidateSubdomain(params.Subdomain); err != nil {
		return nil, err
	}

	if err := s.nodeStore.ReserveRAM(ctx, params.Node.ID, params.Project.RAMMB); err != nil {
		if errors.Is(err, node.ErrInsufficientCapacity) {
			return nil, conflict("insufficient capacity on node", err)
		}
		return nil, internal("failed to reserve resources", err)
	}

	req := CreateTenantRequest{
		Name:      params.Name,
		ProjectID: params.Project.ID,
		NodeID:    params.Node.ID,
		Subdomain: params.Subdomain,
	}

	t, err := s.createRecord(ctx, req, params.OwnerID)
	if err != nil {
		if releaseErr := s.nodeStore.ReleaseRAM(ctx, params.Node.ID, params.Project.RAMMB); releaseErr != nil {
			slog.Error("release ram after tenant creation failure", "error", releaseErr)
		}

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, conflict("name or subdomain already exists", err)
		}
		return nil, internal("failed to create tenant", err)
	}

	// Provisioning runs asynchronously and reports progress through the tenant
	// status, so a failure here cannot be surfaced to the caller.
	s.provisioner.Provision(t.ID, params.Node.ID, params.Project.ID, params.Subdomain, params.Project.RAMMB)

	meta := map[string]string{"name": params.Name, "subdomain": params.Subdomain}
	if actor.UserID != "" {
		meta["user_id"] = actor.UserID
	}
	s.audit(ctx, "create", t.ID, meta)

	return t, nil
}

func (s *LifecycleService) createRecord(ctx context.Context, req CreateTenantRequest, ownerID string) (*Tenant, error) {
	if ownerID == "" {
		return s.store.Create(ctx, req)
	}
	creator, ok := s.store.(OwnerCreator)
	if !ok {
		return nil, fmt.Errorf("tenant store cannot create owner-scoped tenants")
	}
	return creator.CreateWithOwner(ctx, req, ownerID)
}

// Delete tears a tenant down and returns its refreshed record. The caller is
// responsible for having established that this tenant may be touched at all.
func (s *LifecycleService) Delete(ctx context.Context, t *Tenant, actor Actor) (*Tenant, *LifecycleError) {
	if !deletableStatuses[t.Status] {
		return nil, conflict("tenant cannot be deleted in current status: "+t.Status, nil)
	}

	// The project is only needed for the RAM figure, so a missing project is
	// not fatal: the tenant is still removed, just without releasing RAM.
	proj, err := s.projectStore.GetByID(ctx, t.ProjectID)
	if err != nil {
		return nil, internal("failed to get project", err)
	}
	ramMB := 0
	if proj != nil {
		ramMB = proj.RAMMB
	}

	if t.LXCID != nil {
		// Deprovision performs the status transition and releases RAM itself.
		if err := s.provisioner.Deprovision(ctx, t.ID, t.NodeID, t.Subdomain, *t.LXCID, ramMB); err != nil {
			if errors.Is(err, ErrStateConflict) {
				return nil, conflict("tenant is already being deleted", err)
			}
			return nil, internal("failed to deprovision tenant", err)
		}
	} else {
		// No container to remove: transition directly, releasing RAM in between.
		if err := s.store.SetDeleting(ctx, t.ID); err != nil {
			if errors.Is(err, ErrStateConflict) {
				return nil, conflict("tenant is already being deleted", err)
			}
			return nil, internal("failed to delete tenant", err)
		}
		if ramMB > 0 {
			// Best effort: a leaked reservation is better than a stuck delete.
			if err := s.nodeStore.ReleaseRAM(ctx, t.NodeID, ramMB); err != nil {
				slog.Error("release ram on tenant deletion", "error", err)
			}
		}
		if err := s.store.SetDeleted(ctx, t.ID); err != nil {
			return nil, internal("failed to delete tenant", err)
		}
	}

	var meta map[string]string
	if actor.UserID != "" {
		meta = map[string]string{"user_id": actor.UserID}
	}
	s.audit(ctx, "delete", t.ID, meta)

	updated, err := s.store.GetByID(ctx, t.ID)
	if err != nil {
		return nil, internal("failed to get tenant", err)
	}
	return updated, nil
}

func (s *LifecycleService) audit(ctx context.Context, action, tenantID string, meta map[string]string) {
	if s.auditStore == nil {
		return
	}
	if meta == nil {
		// Log takes an any, and a nil map placed in one is not a nil any. Pass
		// an untyped nil so the details column stays NULL instead of holding
		// the JSON literal null.
		s.auditStore.Log(ctx, action, "tenant", tenantID, nil)
		return
	}
	s.auditStore.Log(ctx, action, "tenant", tenantID, meta)
}
