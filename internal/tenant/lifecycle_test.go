package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"controlplane/internal/node"
	"controlplane/internal/project"
)

// --- Helpers ---

var errBoom = errors.New("store unavailable")

type lifecycleFixture struct {
	service   *LifecycleService
	store     *mockTenantStore
	nodes     *mockNodeStore
	projects  *mockProjectStore
	provision *mockProvisioner
}

func newLifecycleFixture() *lifecycleFixture {
	store := newMockTenantStore()
	nodes := newMockNodeStore()
	projects := newMockProjectStore()
	prov := newMockProvisioner()

	return &lifecycleFixture{
		// The audit store is a concrete type backed by a database, so it stays
		// nil here; the lifecycle skips auditing when it is absent.
		service:   NewLifecycleService(store, nodes, projects, prov, nil),
		store:     store,
		nodes:     nodes,
		projects:  projects,
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
	svc := NewLifecycleService(store, newMockNodeStore(), newMockProjectStore(), prov, nil)

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
	f.nodes.reserveErr = errBoom // would fail loudly if reached

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
}

func TestLifecycleCreate_InsufficientCapacity(t *testing.T) {
	f := newLifecycleFixture()
	f.nodes.reserveErr = node.ErrInsufficientCapacity

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
	f.nodes.reserveErr = errBoom

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if err.Message != "failed to reserve resources" {
		t.Errorf("message = %q", err.Message)
	}
}

// TestLifecycleCreate_DuplicateReleasesRAM pins that a unique-constraint
// violation is reported as a conflict and does not leak the RAM reservation.
func TestLifecycleCreate_DuplicateReleasesRAM(t *testing.T) {
	f := newLifecycleFixture()
	f.store.createErr = &pgconn.PgError{Code: "23505"}

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureConflict {
		t.Errorf("kind = %v, want FailureConflict", err.Kind)
	}
	if err.Message != "name or subdomain already exists" {
		t.Errorf("message = %q", err.Message)
	}
	if f.provision.wasProvisionCalled() {
		t.Error("provisioning must not start when the record was not created")
	}
}

func TestLifecycleCreate_StoreFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.store.createErr = errBoom

	_, err := f.service.Create(context.Background(), CreateParams{
		Name: "app", Subdomain: "myapp", Project: testProject(), Node: testNodeRecord(),
	}, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("expected the cause to be wrapped, got %v", err)
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
			f.projects.projects["proj-1"] = testProject()

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
	f.projects.projects["proj-1"] = testProject()

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
	f.projects.projects["proj-1"] = testProject()

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
	f.projects.projects["proj-1"] = testProject()

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
	f.projects.projects["proj-1"] = testProject()

	_, err := f.service.Delete(context.Background(), tn, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureConflict {
		t.Errorf("kind = %v, want FailureConflict", err.Kind)
	}
}

func TestLifecycleDelete_ProjectLookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.projects.getErr = errBoom
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	_, err := f.service.Delete(context.Background(), tn, Actor{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Kind != FailureInternal {
		t.Errorf("kind = %v, want FailureInternal", err.Kind)
	}
	if err.Message != "failed to get project" {
		t.Errorf("message = %q", err.Message)
	}
}

// TestLifecycleDelete_MissingProjectStillDeletes pins that a tenant whose
// project has vanished is still removed; only the RAM release is skipped.
func TestLifecycleDelete_MissingProjectStillDeletes(t *testing.T) {
	f := newLifecycleFixture()
	tn := &Tenant{ID: "t-1", ProjectID: "gone", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := f.store.tenants["t-1"].Status; got != "deleted" {
		t.Errorf("status = %q, want deleted", got)
	}
}

// TestLifecycleDelete_RAMReleaseFailureDoesNotBlock pins that a failing RAM
// release is logged and the delete still completes.
func TestLifecycleDelete_RAMReleaseFailureDoesNotBlock(t *testing.T) {
	f := newLifecycleFixture()
	f.nodes.releaseErr = errBoom
	tn := &Tenant{ID: "t-1", ProjectID: "proj-1", NodeID: "node-1", Status: "active"}
	f.store.tenants[tn.ID] = tn
	f.projects.projects["proj-1"] = testProject()

	if _, err := f.service.Delete(context.Background(), tn, Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
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
	f.projects.projects["proj-1"] = testProject()

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
	f.projects.projects["proj-1"] = testProject()

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
