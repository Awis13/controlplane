package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/audit"
	"controlplane/internal/node"
	"controlplane/internal/project"
)

// --- Helpers ---

var errBoom = errors.New("store unavailable")

type lifecycleFixture struct {
	service   *LifecycleService
	store     *mockTenantStore
	provision *mockProvisioner
}

func newLifecycleFixture() *lifecycleFixture {
	store := newMockTenantStore()
	prov := newMockProvisioner()

	return &lifecycleFixture{
		// The audit store is a concrete type backed by a database, so it stays
		// nil here; the lifecycle skips auditing when it is absent.
		service:   NewLifecycleService(store, prov, nil),
		store:     store,
		provision: prov,
	}
}

func testProject() *project.Project {
	return &project.Project{ID: "proj-1", Name: "test", RAMMB: 1536}
}

func testNodeRecord() *node.Node {
	return &node.Node{ID: "node-1", Name: "node", Status: "active"}
}

func lxcPtr(id int) *int { return &id }

// --- Audit logging ---

type auditEntry struct {
	Action     string
	EntityType string
	EntityID   string
	Details    any
}

type mockAuditLogger struct {
	entries []auditEntry
}

func (m *mockAuditLogger) Log(_ context.Context, action, entityType, entityID string, details any) {
	m.entries = append(m.entries, auditEntry{Action: action, EntityType: entityType, EntityID: entityID, Details: details})
}

func TestLifecycleAudit_Create(t *testing.T) {
	tests := []struct {
		name        string
		actor       Actor
		ownerID     string
		wantDetails map[string]string
	}{
		{
			name:        "without an actor, as the API and admin paths call it",
			wantDetails: map[string]string{"name": "app", "subdomain": "myapp"},
		},
		{
			name:        "with an actor, as the user path calls it",
			actor:       Actor{UserID: "owner-1"},
			ownerID:     "owner-1",
			wantDetails: map[string]string{"name": "app", "subdomain": "myapp", "user_id": "owner-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &mockAuditLogger{}
			var store LifecycleTenantStore = newMockTenantStore()
			if tt.ownerID != "" {
				store = newMockUserTenantStore()
			}
			svc := NewLifecycleService(store, newMockProvisioner(), logger)

			tn, err := svc.Create(context.Background(), CreateParams{
				Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(), OwnerID: tt.ownerID,
			}, tt.actor)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if len(logger.entries) != 1 {
				t.Fatalf("audit entries = %d, want 1", len(logger.entries))
			}
			got := logger.entries[0]
			if got.Action != "create" || got.EntityType != "tenant" || got.EntityID != tn.ID {
				t.Errorf("entry = %+v, want a create entry for tenant %s", got, tn.ID)
			}
			details, ok := got.Details.(map[string]string)
			if !ok {
				t.Fatalf("details = %T, want map[string]string", got.Details)
			}
			if len(details) != len(tt.wantDetails) {
				t.Fatalf("details = %v, want %v", details, tt.wantDetails)
			}
			for k, v := range tt.wantDetails {
				if details[k] != v {
					t.Errorf("details[%q] = %q, want %q", k, details[k], v)
				}
			}
		})
	}
}

func TestLifecycleAudit_Delete(t *testing.T) {
	tests := []struct {
		name        string
		actor       Actor
		wantDetails map[string]string
	}{
		{name: "without an actor the details stay nil", actor: Actor{}},
		{name: "with an actor the user is recorded", actor: Actor{UserID: "owner-1"}, wantDetails: map[string]string{"user_id": "owner-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &mockAuditLogger{}
			store := newMockTenantStore()
			svc := NewLifecycleService(store, newMockProvisioner(), logger)

			tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
			store.tenants[tn.ID] = tn

			if _, err := svc.Delete(context.Background(), tn, tt.actor); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			if len(logger.entries) != 1 {
				t.Fatalf("audit entries = %d, want 1", len(logger.entries))
			}
			got := logger.entries[0]
			if got.Action != "delete" || got.EntityType != "tenant" || got.EntityID != "t-1" {
				t.Errorf("entry = %+v", got)
			}
			if tt.wantDetails == nil {
				if got.Details != nil {
					t.Errorf("details = %v, want nil so the entry matches what these paths logged before", got.Details)
				}
				return
			}
			details, ok := got.Details.(map[string]string)
			if !ok {
				t.Fatalf("details = %T, want map[string]string", got.Details)
			}
			if details["user_id"] != tt.wantDetails["user_id"] {
				t.Errorf("details = %v, want %v", details, tt.wantDetails)
			}
		})
	}
}

// TestLifecycleAudit_FailedOperationsAreNotLogged pins that only completed
// actions reach the audit log.
func TestLifecycleAudit_FailedOperationsAreNotLogged(t *testing.T) {
	logger := &mockAuditLogger{}
	store := newMockTenantStore()
	store.createWithReservationErr = errBoom
	svc := NewLifecycleService(store, newMockProvisioner(), logger)

	if _, err := svc.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{}); err == nil {
		t.Fatal("expected an error")
	}

	if len(logger.entries) != 0 {
		t.Errorf("audit entries = %d, want 0", len(logger.entries))
	}
}

// TestLifecycleAudit_NilLoggers pins that both an absent logger and a nil
// *audit.Store are skipped rather than panicking. The second case is the trap:
// a nil pointer stored in an interface is not itself nil.
func TestLifecycleAudit_NilLoggers(t *testing.T) {
	tests := []struct {
		name   string
		logger AuditLogger
	}{
		{name: "no logger at all", logger: nil},
		{name: "a nil audit store", logger: (*audit.Store)(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockTenantStore()
			svc := NewLifecycleService(store, newMockProvisioner(), tt.logger)

			tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
			store.tenants[tn.ID] = tn

			// The operation must still complete: auditing is not load-bearing.
			if _, err := svc.Delete(context.Background(), tn, Actor{UserID: "owner-1"}); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if got := store.tenants["t-1"].Status; got != "deleted" {
				t.Errorf("status = %q, want deleted", got)
			}
		})
	}
}

// --- ValidateSubdomain ---

func TestValidateSubdomain(t *testing.T) {
	tests := []struct {
		name        string
		subdomain   string
		wantErr     bool
		wantMessage string
	}{
		{name: "simple", subdomain: "myapp"},
		{name: "with hyphen", subdomain: "my-app"},
		{name: "with digits", subdomain: "app123"},
		{name: "two characters", subdomain: "ab"},
		{
			name: "single character", subdomain: "a",
			wantErr: true, wantMessage: "invalid subdomain: must be lowercase alphanumeric with hyphens, 2-63 chars",
		},
		{name: "uppercase", subdomain: "MyApp", wantErr: true},
		{name: "leading hyphen", subdomain: "-app", wantErr: true},
		{name: "trailing hyphen", subdomain: "app-", wantErr: true},
		{name: "underscore", subdomain: "my_app", wantErr: true},
		{name: "dot", subdomain: "my.app", wantErr: true},
		{name: "empty", subdomain: "", wantErr: true},
		{name: "exactly 63 characters", subdomain: strings.Repeat("a", 63)},
		{name: "64 characters is too long", subdomain: strings.Repeat("a", 64), wantErr: true},
		{name: "reserved www", subdomain: "www", wantErr: true, wantMessage: "subdomain is reserved"},
		{name: "reserved api", subdomain: "api", wantErr: true, wantMessage: "subdomain is reserved"},
		{name: "reserved admin", subdomain: "admin", wantErr: true, wantMessage: "subdomain is reserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubdomain(tt.subdomain)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateSubdomain(%q) = nil, want an error", tt.subdomain)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("ValidateSubdomain(%q) = %v, want nil", tt.subdomain, err)
				}
				return
			}
			if err.Kind != FailureInvalid {
				t.Errorf("kind = %v, want FailureInvalid", err.Kind)
			}
			if tt.wantMessage != "" && err.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", err.Message, tt.wantMessage)
			}
		})
	}
}

// TestReservedSubdomains_EveryEntryIsRefused walks the whole reserved list
// rather than the three names spot-checked in TestValidateSubdomain, which is
// where the charset and length boundaries are pinned.
func TestReservedSubdomains_EveryEntryIsRefused(t *testing.T) {
	if len(reservedSubdomains) == 0 {
		t.Fatal("the reserved list is empty, so nothing is protected")
	}

	for name := range reservedSubdomains {
		t.Run(name, func(t *testing.T) {
			err := ValidateSubdomain(name)
			if err == nil {
				t.Fatalf("ValidateSubdomain(%q) = nil, want a refusal", name)
			}
			if err.Message != "subdomain is reserved" {
				t.Errorf("message = %q, want the reserved refusal", err.Message)
			}
		})
	}
}

// TestReservedSubdomains_AreReachable guards a way this list can rot: the
// charset check runs first, so a reserved entry that cannot pass it would never
// be consulted, and the caller would see a confusing message about the charset
// instead of learning the name is taken.
func TestReservedSubdomains_AreReachable(t *testing.T) {
	for name := range reservedSubdomains {
		if len(name) > 63 || !subdomainRegexp.MatchString(name) {
			t.Errorf("reserved entry %q cannot pass the format check, so it is dead configuration", name)
		}
	}
}

// --- Create ---

func TestLifecycleCreate_WithoutOwner(t *testing.T) {
	f := newLifecycleFixture()

	tn, err := f.service.Create(context.Background(), CreateParams{
		Name:      "app",
		Subdomain: "myapp",
		Project:   testProject(),
		Node:      testNodeRecord(),
	}, Actor{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if tn.Subdomain != "myapp" || tn.Name != "app" {
		t.Errorf("tenant = %+v, want name app and subdomain myapp", tn)
	}
	if tn.ProjectID != "proj-1" || tn.NodeID != "node-1" {
		t.Errorf("tenant references = %q/%q, want proj-1/node-1", tn.ProjectID, tn.NodeID)
	}
	if !f.provision.wasProvisionCalled() {
		t.Error("expected provisioning to start")
	}
}

func TestLifecycleCreate_WithOwner(t *testing.T) {
	store := newMockUserTenantStore()
	prov := newMockProvisioner()
	svc := NewLifecycleService(store, prov, nil)

	tn, err := svc.Create(context.Background(), CreateParams{
		Name:      "app",
		Subdomain: "myapp",
		Project:   testProject(),
		Node:      testNodeRecord(),
		OwnerID:   "owner-1",
	}, Actor{UserID: "owner-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if tn.OwnerID == nil || *tn.OwnerID != "owner-1" {
		t.Errorf("owner = %v, want owner-1", tn.OwnerID)
	}
	if !prov.wasProvisionCalled() {
		t.Error("expected provisioning to start")
	}
}

func TestLifecycleCreate_RejectsBadSubdomainBeforeReserving(t *testing.T) {
	f := newLifecycleFixture()

	_, err := f.service.Create(context.Background(), CreateParams{
		Name:      "app",
		Subdomain: "WWW",
		Project:   testProject(),
		Node:      testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInvalid {
		t.Errorf("kind = %v, want FailureInvalid", err.Kind)
	}
	if f.provision.wasProvisionCalled() {
		t.Error("provisioning must not start for an invalid subdomain")
	}
	if len(f.store.createWithReservationCalls) != 0 {
		t.Error("a reservation must not be created for an invalid subdomain")
	}
}

func TestLifecycleCreate_InsufficientCapacity(t *testing.T) {
	f := newLifecycleFixture()
	f.store.createWithReservationErr = node.ErrInsufficientCapacity

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureConflict {
		t.Errorf("kind = %v, want FailureConflict", err.Kind)
	}
	if err.Message != "insufficient capacity on node" {
		t.Errorf("message = %q", err.Message)
	}
	if f.provision.wasProvisionCalled() {
		t.Error("provisioning must not start when capacity was refused")
	}
}

func TestLifecycleCreate_ReserveFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.store.createWithReservationErr = errBoom

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if err.Message != "failed to create tenant" {
		t.Errorf("message = %q", err.Message)
	}
}

// TestLifecycleCreate_FailedInsertDoesNotReleaseReservation pins the new
// semantics: the reservation and the insert happen in one transaction, so a
// failed insert rolls the reservation back and there is no separate release to
// perform. The lifecycle must not hand the reservation back a second time.
func TestLifecycleCreate_FailedInsertDoesNotReleaseReservation(t *testing.T) {
	tests := []struct {
		name        string
		createErr   error
		wantKind    FailureKind
		wantMessage string
	}{
		{
			name:        "duplicate name or subdomain",
			createErr:   &pgconn.PgError{Code: "23505"},
			wantKind:    FailureConflict,
			wantMessage: "name or subdomain already exists",
		},
		{
			name:        "store failure",
			createErr:   errBoom,
			wantKind:    FailureInternal,
			wantMessage: "failed to create tenant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture()
			f.store.createWithReservationErr = tt.createErr
			proj := testProject()

			_, err := f.service.Create(context.Background(), CreateParams{
				Name: "app", Subdomain: "myapp", Project: proj, Node: testNodeRecord(),
			}, Actor{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Kind != tt.wantKind {
				t.Errorf("kind = %v, want %v", err.Kind, tt.wantKind)
			}
			if err.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", err.Message, tt.wantMessage)
			}

			// The reservation was attempted with the project's RAM figure, but
			// the failed insert must not trigger a release: the transaction
			// rolled the reservation back itself.
			if len(f.store.createWithReservationCalls) != 1 {
				t.Fatalf("reservation calls = %d, want exactly one", len(f.store.createWithReservationCalls))
			}
			if got := f.store.createWithReservationCalls[0].RAMMB; got != proj.RAMMB {
				t.Errorf("reserved %d MB, want %d", got, proj.RAMMB)
			}
			if len(f.store.releaseReservationCalls) != 0 {
				t.Errorf("release calls = %v, want none: the transaction rolled the reservation back", f.store.releaseReservationCalls)
			}
			if f.provision.wasProvisionCalled() {
				t.Error("provisioning must not start when the record was not created")
			}
		})
	}
}

// TestLifecycleCreate_StoreFailureWrapsCause keeps the underlying error
// reachable for logging even though the client sees a generic message.
func TestLifecycleCreate_StoreFailureWrapsCause(t *testing.T) {
	f := newLifecycleFixture()
	f.store.createWithReservationErr = errBoom

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("expected the cause to be wrapped, got %v", err)
	}
}

// TestLifecycleCreate_SuccessKeepsTheReservation pins the other side: a tenant
// that was created must keep its RAM.
func TestLifecycleCreate_SuccessKeepsTheReservation(t *testing.T) {
	f := newLifecycleFixture()
	proj := testProject()

	if _, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: proj, Node: testNodeRecord(),
	}, Actor{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(f.store.createWithReservationCalls) != 1 {
		t.Fatalf("reservation calls = %d, want exactly one", len(f.store.createWithReservationCalls))
	}
	if got := f.store.createWithReservationCalls[0].RAMMB; got != proj.RAMMB {
		t.Errorf("reserved %d MB, want %d", got, proj.RAMMB)
	}
	if len(f.store.releaseReservationCalls) != 0 {
		t.Errorf("release calls = %v, want none: the tenant exists and owns that RAM", f.store.releaseReservationCalls)
	}
}

// TestLifecycleCreate_RefusedReservationReleasesNothing pins that a reservation
// that never succeeded is not handed back a second time.
func TestLifecycleCreate_RefusedReservationReleasesNothing(t *testing.T) {
	f := newLifecycleFixture()
	f.store.createWithReservationErr = node.ErrInsufficientCapacity

	if _, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{}); err == nil {
		t.Fatal("expected an error")
	}

	if len(f.store.releaseReservationCalls) != 0 {
		t.Errorf("release calls = %v, want none", f.store.releaseReservationCalls)
	}
}

// TestLifecycleCreate_OwnerWithoutCapableStore pins the programming-error path:
// asking for an owner-scoped tenant from a store that cannot create one fails
// rather than silently creating an unowned tenant.
func TestLifecycleCreate_OwnerWithoutCapableStore(t *testing.T) {
	f := newLifecycleFixture() // mockTenantStore does not implement OwnerCreator

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(), OwnerID: "owner-1",
	}, Actor{UserID: "owner-1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if f.provision.wasProvisionCalled() {
		t.Error("provisioning must not start")
	}
}

// --- Delete ---

// TestLifecycleDelete_DeletableStatuses pins which states a delete may start
// from, including suspended.
func TestLifecycleDelete_DeletableStatuses(t *testing.T) {
	tests := []struct {
		status     string
		wantDelete bool
	}{
		{status: "active", wantDelete: true},
		{status: "error", wantDelete: true},
		{status: "suspended", wantDelete: true},
		{status: "provisioning"},
		{status: "deleting"},
		{status: "deleted"},
		{status: ""},
	}

	for _, tt := range tests {
		t.Run("status="+tt.status, func(t *testing.T) {
			f := newLifecycleFixture()
			tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Subdomain: "myapp", Status: tt.status}
			f.store.tenants[tn.ID] = tn

			_, err := f.service.Delete(context.Background(), tn, Actor{})

			if !tt.wantDelete {
				if err == nil {
					t.Fatalf("expected a refusal for status %q", tt.status)
				}
				if err.Kind != FailureConflict {
					t.Errorf("kind = %v, want FailureConflict", err.Kind)
				}
				if err.Message != "tenant cannot be deleted in current status: "+tt.status {
					t.Errorf("message = %q", err.Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if got := f.store.tenants["t-1"].Status; got != "deleted" {
				t.Errorf("status after delete = %q, want deleted", got)
			}
		})
	}
}

// TestLifecycleDelete_WithContainerDeprovisions pins that a tenant carrying an
// LXC ID goes through the provisioner rather than the direct status path.
func TestLifecycleDelete_WithContainerDeprovisions(t *testing.T) {
	f := newLifecycleFixture()
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Subdomain: "myapp", Status: "active", LXCID: lxcPtr(105)}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !f.provision.wasDeprovisionCalled() {
		t.Error("expected the container to be deprovisioned")
	}
}

func TestLifecycleDelete_DeprovisionConflict(t *testing.T) {
	f := newLifecycleFixture()
	f.provision.deprovisionErr = ErrStateConflict
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active", LXCID: lxcPtr(105)}
	f.store.tenants[tn.ID] = tn

	_, err := f.service.Delete(context.Background(), tn, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureConflict {
		t.Errorf("kind = %v, want FailureConflict", err.Kind)
	}
	if err.Message != "tenant is already being deleted" {
		t.Errorf("message = %q", err.Message)
	}
}

func TestLifecycleDelete_DeprovisionFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.provision.deprovisionErr = errBoom
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active", LXCID: lxcPtr(105)}
	f.store.tenants[tn.ID] = tn

	_, err := f.service.Delete(context.Background(), tn, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if err.Message != "failed to deprovision tenant" {
		t.Errorf("message = %q", err.Message)
	}
}

func TestLifecycleDelete_SetDeletingConflict(t *testing.T) {
	f := newLifecycleFixture()
	f.store.setDeletingErr = ErrStateConflict
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	_, err := f.service.Delete(context.Background(), tn, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureConflict {
		t.Errorf("kind = %v, want FailureConflict", err.Kind)
	}
}

// TestLifecycleDelete_ReleasesReservation is the delete-side half of the
// capacity guard: removing a tenant that has no container must hand its
// reservation back to the node, or the node keeps accounting for a tenant that
// no longer exists.
func TestLifecycleDelete_ReleasesReservation(t *testing.T) {
	f := newLifecycleFixture()
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if len(f.store.releaseReservationCalls) != 1 {
		t.Fatalf("release calls = %v, want exactly one: the tenant's reservation leaked", f.store.releaseReservationCalls)
	}
	if f.store.releaseReservationCalls[0] != "t-1" {
		t.Errorf("released %q, want t-1", f.store.releaseReservationCalls[0])
	}
}

// TestLifecycleDelete_ContainerDeleteDoesNotReleaseReservation pins the
// division of labour: when there is a container, Deprovision releases the
// reservation itself, so the service must not release it a second time.
func TestLifecycleDelete_ContainerDeleteDoesNotReleaseReservation(t *testing.T) {
	f := newLifecycleFixture()
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active", LXCID: lxcPtr(105)}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.store.releaseReservationCalls) != 0 {
		t.Errorf("release calls = %v, want none: Deprovision owns that release", f.store.releaseReservationCalls)
	}
}

// TestLifecycleDelete_NoProjectStillDeletes pins that a tenant whose project
// has vanished is still removed: the reservation amount comes from the tenant's
// own record, so no project lookup is needed.
func TestLifecycleDelete_NoProjectStillDeletes(t *testing.T) {
	f := newLifecycleFixture()
	tn := &Tenant{ID: "t-1", ProjectID: "gone", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := f.store.tenants["t-1"].Status; got != "deleted" {
		t.Errorf("status = %q, want deleted", got)
	}
	if len(f.store.releaseReservationCalls) != 1 {
		t.Errorf("release calls = %v, want exactly one", f.store.releaseReservationCalls)
	}
}

// TestLifecycleDelete_ReleaseFailureDoesNotBlock pins that a failing release is
// attempted, logged, and does not stop the delete.
func TestLifecycleDelete_ReleaseFailureDoesNotBlock(t *testing.T) {
	f := newLifecycleFixture()
	f.store.releaseReservationErr = errBoom
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.store.releaseReservationCalls) != 1 {
		t.Errorf("release calls = %v, want exactly one attempt", f.store.releaseReservationCalls)
	}
	if got := f.store.tenants["t-1"].Status; got != "deleted" {
		t.Errorf("status = %q, want deleted", got)
	}
}

func TestLifecycleDelete_SetDeletedFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.store.setDeletedErr = errBoom
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	_, err := f.service.Delete(context.Background(), tn, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if err.Message != "failed to delete tenant" {
		t.Errorf("message = %q", err.Message)
	}
}

// TestLifecycleDelete_ReturnsRefreshedTenant pins that the caller gets the
// post-delete record rather than the one it passed in.
func TestLifecycleDelete_ReturnsRefreshedTenant(t *testing.T) {
	f := newLifecycleFixture()
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	updated, err := f.service.Delete(context.Background(), tn, Actor{})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if updated == nil {
		t.Fatal("expected a tenant record back")
	}
	if updated.Status != "deleted" {
		t.Errorf("returned status = %q, want deleted", updated.Status)
	}
}

// --- Error plumbing ---

// TestLifecycleDelete_DoesNotCancelStripe pins SEC-05: deleting a tenant is a
// soft delete that must not cancel its Stripe subscription. The lifecycle has
// no Stripe dependency at all, so the only external side effect is deprovisioning
// the container; the tenant's Stripe customer and subscription references are
// left untouched so the billing portal can still manage the subscription that
// outlived the studio.
func TestLifecycleDelete_DoesNotCancelStripe(t *testing.T) {
	f := newLifecycleFixture()
	customer := "cus_123"
	sub := "sub_123"
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Subdomain: "myapp",
		Status:           "active",
		StripeCustomerID: &customer, StripeSubscriptionID: &sub, Tier: "studio"}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got := f.store.tenants["t-1"]
	if got.Status != "deleted" {
		t.Fatalf("status = %q, want deleted", got.Status)
	}
	// The Stripe references must survive the delete: no cancel, no clearing.
	if got.StripeCustomerID == nil || *got.StripeCustomerID != customer {
		t.Errorf("stripe customer = %v, want %q preserved", got.StripeCustomerID, customer)
	}
	if got.StripeSubscriptionID == nil || *got.StripeSubscriptionID != sub {
		t.Errorf("stripe subscription = %v, want %q preserved", got.StripeSubscriptionID, sub)
	}
	if got.Tier != "studio" {
		t.Errorf("tier = %q, want %q preserved", got.Tier, "studio")
	}
}

func TestLifecycleError_UnwrapsCause(t *testing.T) {
	err := internal("failed to delete tenant", errBoom)

	if !errors.Is(err, errBoom) {
		t.Error("expected errors.Is to find the cause")
	}
	if got := err.Error(); got != "failed to delete tenant: store unavailable" {
		t.Errorf("Error() = %q", got)
	}
}

func TestLifecycleError_WithoutCause(t *testing.T) {
	err := invalid("subdomain is reserved")

	if got := err.Error(); got != "subdomain is reserved" {
		t.Errorf("Error() = %q", got)
	}
	if errors.Unwrap(err) != nil {
		t.Error("expected no wrapped cause")
	}
}
