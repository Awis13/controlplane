package tenant

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"controlplane/internal/database"
	"controlplane/internal/node"
	"controlplane/internal/project"
	"controlplane/internal/user"
)

// TestBillingDeletedIntegration exercises the billing store queries against a
// real PostgreSQL, pinning that soft-deleted tenants stay reachable by the
// billing paths that must manage a subscription that outlived the studio:
// the portal/status lookup includes deleted rows, the webhook lookup by
// customer includes deleted rows, and the non-deleted owner lookup still
// excludes them so checkout and studio listings never see a deleted tenant.
//
// It needs a database. In CI the postgres service provides DATABASE_URL; locally
// it spins up a disposable postgres:17 container. If neither is available the
// test skips rather than failing.
func TestBillingDeletedIntegration(t *testing.T) {
	url := integrationDBURL(t)
	if url == "" {
		t.Skip("no DATABASE_URL and docker unavailable; skipping billing integration test")
	}

	if err := database.Migrate(url); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	nodeStore := node.NewStore(pool)
	projectStore := project.NewStore(pool)
	userStore := user.NewStore(pool)
	store := NewStore(pool)

	// The owner_id column references users, so create the owner first.
	owner := &user.User{Email: "billing-owner@example.com", PasswordHash: "x", DisplayName: "Billing Owner"}
	if err := userStore.Create(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	ownerID := owner.ID.String()

	n, err := nodeStore.Create(ctx, node.CreateNodeRequest{
		Name: "billing-node", TailscaleIP: "10.0.0.1", ProxmoxURL: "https://pve",
		APIToken: "token", TotalRAMMB: 3072,
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	proj, err := projectStore.Create(ctx, project.CreateProjectRequest{
		Name: "billing-project", TemplateID: 100, RAMMB: 1536,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Two owner-scoped tenants: one stays active, the other is soft-deleted.
	active, err := store.CreateWithOwnerWithReservation(ctx, CreateTenantRequest{
		Name: "billing-active", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "billing-active",
	}, ownerID, 1536)
	if err != nil {
		t.Fatalf("create active tenant: %v", err)
	}
	deleted, err := store.CreateWithOwnerWithReservation(ctx, CreateTenantRequest{
		Name: "billing-deleted", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "billing-deleted",
	}, ownerID, 1536)
	if err != nil {
		t.Fatalf("create deleted tenant: %v", err)
	}

	// Give both a Stripe customer and subscription.
	if err := store.UpdateBilling(ctx, active.ID, "cus_active", "sub_active", "pro"); err != nil {
		t.Fatalf("set active billing: %v", err)
	}
	if err := store.UpdateBilling(ctx, deleted.ID, "cus_deleted", "sub_deleted", "studio"); err != nil {
		t.Fatalf("set deleted billing: %v", err)
	}

	// Soft-delete the second tenant through the same transitions the lifecycle
	// uses: provisioning -> active -> deleting -> deleted. The first tenant is
	// also moved to active so the test reflects a real, running studio.
	if err := store.SetActive(ctx, active.ID, 100); err != nil {
		t.Fatalf("activate active tenant: %v", err)
	}
	if err := store.SetActive(ctx, deleted.ID, 101); err != nil {
		t.Fatalf("activate deleted tenant: %v", err)
	}
	if err := store.SetDeleting(ctx, deleted.ID); err != nil {
		t.Fatalf("set deleting: %v", err)
	}
	if err := store.SetDeleted(ctx, deleted.ID); err != nil {
		t.Fatalf("set deleted: %v", err)
	}

	// GetBillingByOwnerID excludes the deleted tenant.
	byOwner, err := store.GetBillingByOwnerID(ctx, ownerID)
	if err != nil {
		t.Fatalf("get billing by owner: %v", err)
	}
	if len(byOwner) != 1 || byOwner[0].ID != active.ID {
		t.Fatalf("GetBillingByOwnerID = %+v, want only the active tenant", byOwner)
	}
	if byOwner[0].Status != "active" {
		t.Errorf("active tenant status = %q, want %q", byOwner[0].Status, "active")
	}

	// GetBillingByOwnerIDIncludingDeleted returns both, with the deleted one marked.
	byOwnerInc, err := store.GetBillingByOwnerIDIncludingDeleted(ctx, ownerID)
	if err != nil {
		t.Fatalf("get billing by owner including deleted: %v", err)
	}
	if len(byOwnerInc) != 2 {
		t.Fatalf("GetBillingByOwnerIDIncludingDeleted = %d rows, want 2", len(byOwnerInc))
	}
	statuses := map[string]string{}
	for _, t := range byOwnerInc {
		statuses[t.ID] = t.Status
	}
	if statuses[active.ID] != "active" {
		t.Errorf("active tenant status = %q, want %q", statuses[active.ID], "active")
	}
	if statuses[deleted.ID] != "deleted" {
		t.Errorf("deleted tenant status = %q, want %q", statuses[deleted.ID], "deleted")
	}

	// GetByStripeCustomerID finds the deleted tenant by its customer ID.
	got, err := store.GetByStripeCustomerID(ctx, "cus_deleted")
	if err != nil {
		t.Fatalf("get by stripe customer: %v", err)
	}
	if got == nil {
		t.Fatal("GetByStripeCustomerID returned nil for a deleted tenant's customer")
	}
	if got.ID != deleted.ID {
		t.Errorf("GetByStripeCustomerID = %s, want %s", got.ID, deleted.ID)
	}
	if got.Status != "deleted" {
		t.Errorf("deleted tenant status = %q, want %q", got.Status, "deleted")
	}

	// The active tenant is still found by its own customer ID.
	gotActive, err := store.GetByStripeCustomerID(ctx, "cus_active")
	if err != nil {
		t.Fatalf("get active by stripe customer: %v", err)
	}
	if gotActive == nil || gotActive.ID != active.ID {
		t.Fatalf("GetByStripeCustomerID(active) = %+v, want the active tenant", gotActive)
	}
}
