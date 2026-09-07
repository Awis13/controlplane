package tenant

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"controlplane/internal/database"
	"controlplane/internal/node"
	"controlplane/internal/project"
)

// TestReservationIntegration exercises the tenant-owned RAM reservation against
// a real PostgreSQL. It is the guard for the double-release bug: a reservation
// must be released exactly once, driven by the tenant's own record, and a
// legacy tenant with an unknown reservation must never be guessed at.
//
// It needs a database. In CI the postgres service provides DATABASE_URL; locally
// it spins up a disposable postgres:17 container. If neither is available the
// test skips rather than failing, so a machine without Docker still builds and
// runs the unit suite.
func TestReservationIntegration(t *testing.T) {
	url := integrationDBURL(t)
	if url == "" {
		t.Skip("no DATABASE_URL and docker unavailable; skipping reservation integration test")
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
	store := NewStore(pool)

	// A node with room for two studios.
	n, err := nodeStore.Create(ctx, node.CreateNodeRequest{
		Name: "reservation-node", TailscaleIP: "10.0.0.1", ProxmoxURL: "https://pve",
		APIToken: "token", TotalRAMMB: 3072,
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	proj, err := projectStore.Create(ctx, project.CreateProjectRequest{
		Name: "reservation-project", TemplateID: 100, RAMMB: 1536,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	allocated := func() int {
		t.Helper()
		got, err := nodeStore.GetByID(ctx, n.ID)
		if err != nil {
			t.Fatalf("get node: %v", err)
		}
		return got.AllocatedRAMMB
	}

	// --- Studio A reserves 1536 MB. ---
	_, err = store.CreateWithReservation(ctx, CreateTenantRequest{
		Name: "studio-a", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "studio-a",
	}, 1536)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	if got := allocated(); got != 1536 {
		t.Fatalf("after A: allocated = %d, want 1536", got)
	}

	// --- Studio B reserves another 1536 MB (3072 total), then its provisioning
	// fails and the user deletes B without a container. The delete must release
	// B's reservation exactly once, leaving A's 1536 intact. ---
	b, err := store.CreateWithReservation(ctx, CreateTenantRequest{
		Name: "studio-b", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "studio-b",
	}, 1536)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if got := allocated(); got != 3072 {
		t.Fatalf("after B: allocated = %d, want 3072", got)
	}

	// Simulate the failed provisioning: B is left with its reservation held.
	if err := store.ReleaseReservation(ctx, b.ID); err != nil {
		t.Fatalf("release B: %v", err)
	}
	if got := allocated(); got != 1536 {
		t.Fatalf("after releasing B: allocated = %d, want 1536 (A must survive)", got)
	}

	// --- A second release of B is a no-op: the counter must not move. ---
	if err := store.ReleaseReservation(ctx, b.ID); err != nil {
		t.Fatalf("second release of B: %v", err)
	}
	if got := allocated(); got != 1536 {
		t.Fatalf("after second release of B: allocated = %d, want 1536", got)
	}

	// --- Two concurrent releases of the same tenant decrement once. ---
	c, err := store.CreateWithReservation(ctx, CreateTenantRequest{
		Name: "studio-c", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "studio-c",
	}, 1536)
	if err != nil {
		t.Fatalf("create C: %v", err)
	}
	if got := allocated(); got != 3072 {
		t.Fatalf("after C: allocated = %d, want 3072", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.ReleaseReservation(ctx, c.ID)
		}()
	}
	wg.Wait()
	if got := allocated(); got != 1536 {
		t.Fatalf("after concurrent releases of C: allocated = %d, want 1536", got)
	}

	// --- A failed insert rolls the reservation back: the counter must not move. ---
	before := allocated()
	_, err = store.CreateWithReservation(ctx, CreateTenantRequest{
		Name: "studio-a", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "studio-a",
	}, 1536)
	if err == nil {
		t.Fatal("expected a duplicate-subdomain insert to fail")
	}
	if got := allocated(); got != before {
		t.Fatalf("after failed insert: allocated = %d, want %d (reservation must roll back)", got, before)
	}

	// --- A legacy tenant (reserved_ram_mb NULL) is refused, not guessed at. ---
	legacy, err := store.CreateWithReservation(ctx, CreateTenantRequest{
		Name: "studio-legacy", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "studio-legacy",
	}, 1536)
	if err != nil {
		t.Fatalf("create legacy: %v", err)
	}
	// Null out the reservation to simulate a tenant that predates the column.
	if _, err := pool.Exec(ctx, `UPDATE tenants SET reserved_ram_mb = NULL WHERE id = $1`, legacy.ID); err != nil {
		t.Fatalf("null out legacy reservation: %v", err)
	}
	before = allocated()
	err = store.ReleaseReservation(ctx, legacy.ID)
	if err == nil {
		t.Fatal("expected a diagnostic error for a legacy reservation")
	}
	if !strings.Contains(err.Error(), "manual reconciliation") {
		t.Errorf("legacy error = %q, want it to mention manual reconciliation", err)
	}
	if got := allocated(); got != before {
		t.Fatalf("after legacy release: allocated = %d, want %d (legacy must not touch the counter)", got, before)
	}

	// A is still alive and still owns its reservation, and the legacy tenant
	// still holds its (unreleasable) reservation: 1536 + 1536.
	if got := allocated(); got != 3072 {
		t.Fatalf("final allocated = %d, want 3072 (studio A and the legacy tenant must survive)", got)
	}
}

// TestSetLXCIDIntegration verifies that SetLXCID records the container ID while
// the tenant stays in 'provisioning' and does not change its status. The ID is
// what lets a later cleanup find the container even when a deploy step fails.
func TestSetLXCIDIntegration(t *testing.T) {
	url := integrationDBURL(t)
	if url == "" {
		t.Skip("no DATABASE_URL and docker unavailable; skipping SetLXCID integration test")
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
	store := NewStore(pool)

	n, err := nodeStore.Create(ctx, node.CreateNodeRequest{
		Name: "setlxcid-node", TailscaleIP: "10.0.0.1", ProxmoxURL: "https://pve",
		APIToken: "token", TotalRAMMB: 3072,
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	proj, err := projectStore.Create(ctx, project.CreateProjectRequest{
		Name: "setlxcid-project", TemplateID: 100, RAMMB: 1536,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	tn, err := store.CreateWithReservation(ctx, CreateTenantRequest{
		Name: "setlxcid-studio", ProjectID: proj.ID, NodeID: n.ID, Subdomain: "setlxcid-studio",
	}, 1536)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if tn.Status != "provisioning" {
		t.Fatalf("expected status 'provisioning', got %q", tn.Status)
	}

	if err := store.SetLXCID(ctx, tn.ID, 105); err != nil {
		t.Fatalf("set lxc id: %v", err)
	}

	got, err := store.GetByID(ctx, tn.ID)
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if got.LXCID == nil || *got.LXCID != 105 {
		t.Fatalf("expected lxc_id 105, got %v", got.LXCID)
	}
	// The status must be unchanged — SetLXCID only records the ID.
	if got.Status != "provisioning" {
		t.Fatalf("expected status 'provisioning' after SetLXCID, got %q", got.Status)
	}
}

// integrationDBURL returns a reachable postgres URL, or "" if none is
// available. It prefers DATABASE_URL (set by CI's postgres service) and falls
// back to a disposable docker container.
func integrationDBURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return ""
	}

	name := fmt.Sprintf("cp-reservation-%d", time.Now().UnixNano())
	run := exec.Command("docker", "run", "-d", "--name", name,
		"-e", "POSTGRES_PASSWORD=test", "-e", "POSTGRES_DB=controlplane",
		"-p", "127.0.0.1::5432", "postgres:17")
	if out, err := run.CombinedOutput(); err != nil {
		t.Logf("could not start postgres container: %v: %s", err, out)
		return ""
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	portOut, err := exec.Command("docker", "port", name, "5432").Output()
	if err != nil {
		t.Logf("could not read postgres port: %v", err)
		return ""
	}
	hostPort := strings.TrimSpace(string(portOut)) // e.g. "127.0.0.1:54321"
	url := "postgres://postgres:test@" + hostPort + "/controlplane?sslmode=disable"

	// Wait for the server to accept connections. pgxpool.New is lazy, so ping
	// the pool to force a real connection attempt.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pool, err := pgxpool.New(ctx, url)
		if err == nil {
			pingErr := pool.Ping(ctx)
			pool.Close()
			cancel()
			if pingErr == nil {
				return url
			}
		} else {
			pool.Close()
			cancel()
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Log("postgres container did not become ready in time")
	return ""
}
