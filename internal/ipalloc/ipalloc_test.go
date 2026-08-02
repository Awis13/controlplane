package ipalloc

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// used builds the set of taken addresses from a list.
func used(addrs ...string) map[string]bool {
	m := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		m[a] = true
	}
	return m
}

// --- Sequential allocation ---

// TestNext_SkipsNetworkGatewayAndBroadcast pins the skip set both original
// implementations used: the network address, the gateway after it, and the
// broadcast address at the end.
func TestNext_SkipsNetworkGatewayAndBroadcast(t *testing.T) {
	got, err := Next("10.10.0.0/24", nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.0.2" {
		t.Errorf("first allocation = %q, want 10.10.0.2: .0 is the network and .1 the gateway", got)
	}
}

func TestNext_AllocatesSequentially(t *testing.T) {
	taken := used()
	for i := 2; i <= 10; i++ {
		want := fmt.Sprintf("10.10.0.%d", i)
		got, err := Next("10.10.0.0/24", taken)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Fatalf("allocation %d = %q, want %q", i-1, got, want)
		}
		taken[got] = true
	}
}

// TestNext_ReusesAGap pins that a released address is handed out again rather
// than the subnet marching forward until it is exhausted.
func TestNext_ReusesAGap(t *testing.T) {
	taken := used("10.10.0.2", "10.10.0.3", "10.10.0.4", "10.10.0.5")

	if got, err := Next("10.10.0.0/24", taken); err != nil || got != "10.10.0.6" {
		t.Fatalf("Next = %q, %v; want 10.10.0.6", got, err)
	}

	delete(taken, "10.10.0.3")

	got, err := Next("10.10.0.0/24", taken)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.0.3" {
		t.Errorf("Next = %q, want the released 10.10.0.3", got)
	}
}

// --- Exhaustion ---

func TestNext_ExhaustedSubnet(t *testing.T) {
	taken := used()
	for i := 2; i <= 254; i++ {
		taken[fmt.Sprintf("10.10.0.%d", i)] = true
	}

	_, err := Next("10.10.0.0/24", taken)
	if err == nil {
		t.Fatal("expected an error when every host is taken")
	}
	if got := err.Error(); got != "no available IPs in 10.10.0.0/24" {
		t.Errorf("error = %q", got)
	}
}

// TestNext_TinySubnets covers the edges where there is barely any host space:
// a /30 has exactly one usable address under this skip set, and a /31 and /32
// have none.
func TestNext_TinySubnets(t *testing.T) {
	tests := []struct {
		cidr    string
		want    string
		wantErr bool
	}{
		{cidr: "10.10.0.0/30", want: "10.10.0.2"},
		{cidr: "10.10.0.0/31", wantErr: true},
		{cidr: "10.10.0.0/32", wantErr: true},
		// At the very top of the address space the walk runs off the end:
		// Next on 255.255.255.255 yields the zero Addr, which formats as
		// "invalid IP". These must be refusals, not that string.
		{cidr: "255.255.255.252/30", want: "255.255.255.254"},
		{cidr: "255.255.255.254/31", wantErr: true},
		{cidr: "255.255.255.255/32", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			got, err := Next(tt.cidr, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Next(%s) = %q, want an error: no host space", tt.cidr, got)
				}
				return
			}
			if got == "invalid IP" {
				t.Fatalf("Next(%s) returned the zero address formatted as a string", tt.cidr)
			}
			if err != nil {
				t.Fatalf("Next(%s): %v", tt.cidr, err)
			}
			if got != tt.want {
				t.Errorf("Next(%s) = %q, want %q", tt.cidr, got, tt.want)
			}
		})
	}
}

// --- Prefixes the old implementations could not handle ---

// TestNext_SubnetNotOnAZeroBoundary is the case the old code allocated nothing
// in: it wrote candidates into the last octet starting at .2, which is outside
// 10.10.0.128/25, so the containment check failed on the first attempt.
func TestNext_SubnetNotOnAZeroBoundary(t *testing.T) {
	got, err := Next("10.10.0.128/25", nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.0.130" {
		t.Errorf("Next = %q, want 10.10.0.130: .128 is the network and .129 the gateway", got)
	}
}

func TestNext_QuarterSubnetStopsAtItsOwnBroadcast(t *testing.T) {
	taken := used()
	for i := 130; i <= 254; i++ {
		taken[fmt.Sprintf("10.10.0.%d", i)] = true
	}

	if _, err := Next("10.10.0.128/25", taken); err == nil {
		t.Error("expected exhaustion at the end of the /25, not a spill into the next subnet")
	}

	// The same addresses in a /24 leave the lower half free.
	got, err := Next("10.10.0.0/24", taken)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.0.2" {
		t.Errorf("Next = %q, want the /24 to still have its lower half", got)
	}
}

// TestNext_WideSubnetGoesPastTheFirstOctet is the ceiling this replaces: the
// old loop only varied the last octet, so a /16 ran out after 253 addresses
// with 65,000 still free.
func TestNext_WideSubnetGoesPastTheFirstOctet(t *testing.T) {
	taken := used()
	for i := 2; i <= 255; i++ {
		taken[fmt.Sprintf("10.10.0.%d", i)] = true
	}

	got, err := Next("10.10.0.0/16", taken)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.1.0" {
		t.Errorf("Next = %q, want 10.10.1.0: the third octet must advance", got)
	}
}

func TestNext_WideSubnetBroadcastIsTheLastAddress(t *testing.T) {
	// Everything except the final host of the /16.
	taken := used()
	for third := 0; third <= 255; third++ {
		for fourth := 0; fourth <= 255; fourth++ {
			if third == 255 && fourth == 254 {
				continue
			}
			taken[fmt.Sprintf("10.10.%d.%d", third, fourth)] = true
		}
	}

	got, err := Next("10.10.0.0/16", taken)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.255.254" {
		t.Errorf("Next = %q, want the last host before the broadcast", got)
	}

	taken["10.10.255.254"] = true
	if _, err := Next("10.10.0.0/16", taken); err == nil {
		t.Error("expected exhaustion: 10.10.255.255 is the broadcast and must not be handed out")
	}
}

// --- Input handling ---

func TestNext_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		cidr string
	}{
		{name: "empty", cidr: ""},
		{name: "not a cidr", cidr: "10.10.0.0"},
		{name: "nonsense", cidr: "not-a-cidr"},
		{name: "bad mask", cidr: "10.10.0.0/33"},
		{name: "ipv6", cidr: "fd00::/64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Next(tt.cidr, nil); err == nil {
				t.Errorf("Next(%q) = %q, want an error", tt.cidr, got)
			}
		})
	}
}

// TestNext_AcceptsAnUnmaskedPrefix pins that a subnet written with a host bit
// set, which ParseCIDR tolerates, is still allocated from its own network.
func TestNext_AcceptsAnUnmaskedPrefix(t *testing.T) {
	got, err := Next("10.10.0.37/24", nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.10.0.2" {
		t.Errorf("Next = %q, want the allocation to start from the masked network", got)
	}
}

// --- Broadcast arithmetic ---

func TestLastAddr(t *testing.T) {
	tests := []struct {
		cidr string
		want string
	}{
		{cidr: "10.10.0.0/24", want: "10.10.0.255"},
		{cidr: "10.10.0.128/25", want: "10.10.0.255"},
		{cidr: "10.10.0.0/25", want: "10.10.0.127"},
		{cidr: "10.10.0.0/16", want: "10.10.255.255"},
		{cidr: "10.10.0.0/30", want: "10.10.0.3"},
		{cidr: "10.10.0.0/32", want: "10.10.0.0"},
		{cidr: "192.168.50.64/26", want: "192.168.50.127"},
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			prefix := mustPrefix(t, tt.cidr)
			if got := lastAddr(prefix).String(); got != tt.want {
				t.Errorf("lastAddr(%s) = %q, want %q", tt.cidr, got, tt.want)
			}
		})
	}
}

func mustPrefix(t *testing.T, cidr string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		t.Fatalf("parse %q: %v", cidr, err)
	}
	return prefix.Masked()
}

// --- Concurrency contract ---

// TestNext_IsDeterministicWhichIsWhyTheLockExists states the boundary of what
// this package can guarantee on its own. Next is a pure function of the subnet
// and the set of taken addresses: two callers holding the same snapshot get the
// same answer. Nothing here prevents two transactions from both reading a
// snapshot without the other's pending row and then both allocating it. That
// is what the advisory lock in Allocate is for, and it cannot be asserted
// without a live Postgres.
func TestNext_IsDeterministicWhichIsWhyTheLockExists(t *testing.T) {
	taken := used("10.10.0.2")

	first, err := Next("10.10.0.0/24", taken)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	second, err := Next("10.10.0.0/24", taken)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	if first != second {
		t.Fatalf("Next returned %q then %q for one snapshot; it must be deterministic", first, second)
	}
	if first != "10.10.0.3" {
		t.Errorf("Next = %q, want 10.10.0.3", first)
	}
}

// TestNext_IsSafeForConcurrentReaders pins that the function itself holds no
// state, so callers behind the lock cannot corrupt each other. Run under -race
// this also proves it does not mutate the set it is given.
func TestNext_IsSafeForConcurrentReaders(t *testing.T) {
	taken := used("10.10.0.2", "10.10.0.3")

	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			addr, err := Next("10.10.0.0/24", taken)
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- addr
		}()
	}

	for i := 0; i < 8; i++ {
		if got := <-results; got != "10.10.0.4" {
			t.Errorf("concurrent Next = %q, want 10.10.0.4", got)
		}
	}
	if len(taken) != 2 {
		t.Errorf("Next mutated the set it was given: %v", taken)
	}
}

// TestAdvisoryLockKey_IsTheKeyTheTenantStoreAlreadyUses pins the value so
// cutting the stores over cannot silently change which lock they contend on.
// Two allocators taking different keys would not serialize against each other.
func TestAdvisoryLockKey_IsTheKeyTheTenantStoreAlreadyUses(t *testing.T) {
	if AdvisoryLockKey != 0x4C584349 {
		t.Errorf("AdvisoryLockKey = %#x, want 0x4C584349", AdvisoryLockKey)
	}
}

// --- CollectIPs ---

// fakeRows is the smallest pgx.Rows that CollectIPs actually exercises. The
// rest of the interface panics: if a future change starts calling one of them,
// the test should fail loudly rather than quietly return something plausible.
type fakeRows struct {
	values   []string
	pos      int
	scanErr  error
	finalErr error
	closed   bool
}

func (r *fakeRows) Next() bool {
	if r.pos >= len(r.values) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	*(dest[0].(*string)) = r.values[r.pos-1]
	return nil
}

func (r *fakeRows) Err() error             { return r.finalErr }
func (r *fakeRows) Close()                 { r.closed = true }
func (r *fakeRows) Values() ([]any, error) { panic("unexpected call to Values") }
func (r *fakeRows) RawValues() [][]byte    { panic("unexpected call to RawValues") }
func (r *fakeRows) Conn() *pgx.Conn        { panic("unexpected call to Conn") }
func (r *fakeRows) CommandTag() pgconn.CommandTag {
	panic("unexpected call to CommandTag")
}
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	panic("unexpected call to FieldDescriptions")
}

func TestCollectIPs(t *testing.T) {
	rows := &fakeRows{values: []string{"10.10.0.2", "10.10.0.5", "10.10.0.2"}}

	got, err := CollectIPs(rows)
	if err != nil {
		t.Fatalf("CollectIPs: %v", err)
	}

	// Duplicates collapse: this is a set, not a count.
	if len(got) != 2 || !got["10.10.0.2"] || !got["10.10.0.5"] {
		t.Errorf("CollectIPs = %v, want the two distinct addresses", got)
	}
	if !rows.closed {
		t.Error("rows must be closed")
	}
}

func TestCollectIPs_Empty(t *testing.T) {
	rows := &fakeRows{}

	got, err := CollectIPs(rows)
	if err != nil {
		t.Fatalf("CollectIPs: %v", err)
	}
	if got == nil {
		t.Fatal("expected an empty set rather than nil, so callers can index it")
	}
	if len(got) != 0 {
		t.Errorf("CollectIPs = %v, want empty", got)
	}
}

func TestCollectIPs_Errors(t *testing.T) {
	t.Run("scan failure", func(t *testing.T) {
		rows := &fakeRows{values: []string{"10.10.0.2"}, scanErr: errors.New("bad column")}
		if _, err := CollectIPs(rows); err == nil {
			t.Error("expected a scan failure to surface")
		} else if !strings.Contains(err.Error(), "scan ip") {
			t.Errorf("error = %v, want it wrapped with context", err)
		}
	})

	t.Run("iteration failure", func(t *testing.T) {
		// A connection dropping mid-iteration shows up in Err, not in Scan.
		rows := &fakeRows{values: []string{"10.10.0.2"}, finalErr: errors.New("connection reset")}
		if _, err := CollectIPs(rows); err == nil {
			t.Error("expected an iteration failure to surface")
		} else if !strings.Contains(err.Error(), "iterate ips") {
			t.Errorf("error = %v, want it wrapped with context", err)
		}
	})
}
