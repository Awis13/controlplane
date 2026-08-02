package tenant

import "testing"

// The route reconciler and the station poller read the same rows and project
// different columns out of them. These pin the projections, which is the part
// of the shared listing that can be checked without a database: the predicate
// itself only runs in Postgres.

var sharedRows = []activeWithIP{
	{ID: "id-1", Subdomain: "alpha", LXCIP: "10.10.10.2"},
	{ID: "id-2", Subdomain: "beta", LXCIP: "10.10.10.3"},
}

func TestToPollableTenants_TakesIDAndAddress(t *testing.T) {
	got := toPollableTenants(sharedRows)

	if len(got) != 2 {
		t.Fatalf("got %d tenants, want 2", len(got))
	}
	if got[0].ID != "id-1" || got[0].LXCIP != "10.10.10.2" {
		t.Errorf("first = %+v, want the id and address of row one", got[0])
	}
	if got[1].ID != "id-2" || got[1].LXCIP != "10.10.10.3" {
		t.Errorf("second = %+v, want the id and address of row two", got[1])
	}
}

func TestToActiveTenants_TakesSubdomainAndAddress(t *testing.T) {
	got := toActiveTenants(sharedRows)

	if len(got) != 2 {
		t.Fatalf("got %d tenants, want 2", len(got))
	}
	if got[0].Subdomain != "alpha" || got[0].LXCIP != "10.10.10.2" {
		t.Errorf("first = %+v, want the subdomain and address of row one", got[0])
	}
	if got[1].Subdomain != "beta" || got[1].LXCIP != "10.10.10.3" {
		t.Errorf("second = %+v, want the subdomain and address of row two", got[1])
	}
}

// Both callers range over the result, and both used to get a nil slice from an
// empty table. Keep that: an empty non-nil slice would be a silent change in
// what the two listings return when no tenant is reachable.
func TestProjections_EmptyInputStaysNil(t *testing.T) {
	if got := toPollableTenants(nil); got != nil {
		t.Errorf("toPollableTenants(nil) = %v, want nil", got)
	}
	if got := toActiveTenants(nil); got != nil {
		t.Errorf("toActiveTenants(nil) = %v, want nil", got)
	}
}
