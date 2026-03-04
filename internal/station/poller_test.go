package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"controlplane/internal/tenant"
)

// --- Mock tenant lister ---

type mockPollerTenantLister struct {
	tenants []tenant.PollableTenant
}

func (m *mockPollerTenantLister) ListPollable(_ context.Context) ([]tenant.PollableTenant, error) {
	return m.tenants, nil
}

// --- Mock station updater ---

type mockPollerStationUpdater struct {
	mu      sync.Mutex
	updates map[string]bool // tenantID -> is_online
}

func newMockPollerStationUpdater() *mockPollerStationUpdater {
	return &mockPollerStationUpdater{
		updates: make(map[string]bool),
	}
}

func (m *mockPollerStationUpdater) SetOnline(_ context.Context, tenantID string, online bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates[tenantID] = online
	return nil
}

func (m *mockPollerStationUpdater) getUpdate(tenantID string) (bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.updates[tenantID]
	return v, ok
}

// --- Tests ---

func TestPoller_GetStatus_Empty(t *testing.T) {
	p := NewPoller(&mockPollerTenantLister{}, newMockPollerStationUpdater(), 10*time.Second)
	status := p.GetStatus("nonexistent")
	if status != nil {
		t.Errorf("expected nil, got %+v", status)
	}
}

func TestPoller_PollOnce_LiveStation(t *testing.T) {
	// Create mock dashboard API server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := dashboardStatusResponse{}
		resp.Audio.Title = "DJ Mika - Hard Beat"
		resp.Icecast.Listeners = 42
		resp.StreamMode.Mode = "live"
		resp.BPM.Current = 150.5
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Extract host:port from test server URL (strip "http://")
	addr := srv.Listener.Addr().String()

	tenantLister := &mockPollerTenantLister{
		tenants: []tenant.PollableTenant{
			{ID: "tenant-1", LXCIP: addr}, // LXCIP includes port for test
		},
	}
	stationUpdater := newMockPollerStationUpdater()

	p := NewPoller(tenantLister, stationUpdater, 10*time.Second)
	p.WithSkipIPCheck()
	// Override HTTP client to use test server's port (URL is http://addr:port/api/status)
	// We need to intercept the URL building. Instead, use a custom transport.
	p.WithHTTPClient(&http.Client{
		Timeout: 5 * time.Second,
		Transport: &rewriteTransport{
			base:    http.DefaultTransport,
			testURL: srv.URL,
		},
	})

	p.PollOnce(context.Background())

	// Verify cache
	status := p.GetStatus("tenant-1")
	if status == nil {
		t.Fatal("expected status, got nil")
	}
	if !status.IsOnline {
		t.Error("expected is_online=true")
	}
	if status.ListenersCount != 42 {
		t.Errorf("listeners_count = %d, want 42", status.ListenersCount)
	}
	if status.NowPlaying != "DJ Mika - Hard Beat" {
		t.Errorf("now_playing = %q", status.NowPlaying)
	}
	if status.BPM != 150.5 {
		t.Errorf("bpm = %f, want 150.5", status.BPM)
	}

	// Verify DB update
	online, ok := stationUpdater.getUpdate("tenant-1")
	if !ok {
		t.Error("expected SetOnline to be called")
	}
	if !online {
		t.Error("expected SetOnline(true)")
	}
}

func TestPoller_PollOnce_StandbyStation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := dashboardStatusResponse{}
		resp.StreamMode.Mode = "standby"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	tenantLister := &mockPollerTenantLister{
		tenants: []tenant.PollableTenant{{ID: "tenant-2", LXCIP: addr}},
	}
	stationUpdater := newMockPollerStationUpdater()

	p := NewPoller(tenantLister, stationUpdater, 10*time.Second)
	p.WithSkipIPCheck()
	p.WithHTTPClient(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &rewriteTransport{base: http.DefaultTransport, testURL: srv.URL},
	})

	p.PollOnce(context.Background())

	status := p.GetStatus("tenant-2")
	if status == nil {
		t.Fatal("expected status, got nil")
	}
	if status.IsOnline {
		t.Error("expected is_online=false for standby mode")
	}

	online, ok := stationUpdater.getUpdate("tenant-2")
	if !ok {
		t.Error("expected SetOnline to be called")
	}
	if online {
		t.Error("expected SetOnline(false)")
	}
}

func TestPoller_PollOnce_UnreachableStation(t *testing.T) {
	tenantLister := &mockPollerTenantLister{
		tenants: []tenant.PollableTenant{{ID: "tenant-3", LXCIP: "10.10.10.99"}}, // allowed IP but unreachable
	}
	stationUpdater := newMockPollerStationUpdater()

	p := NewPoller(tenantLister, stationUpdater, 10*time.Second)
	p.WithHTTPClient(&http.Client{
		Timeout: 100 * time.Millisecond, // fast timeout for test
	})

	p.PollOnce(context.Background())

	// Should still cache a result (offline)
	status := p.GetStatus("tenant-3")
	if status == nil {
		t.Fatal("expected status, got nil")
	}
	if status.IsOnline {
		t.Error("expected is_online=false for unreachable station")
	}
	if status.ListenersCount != 0 {
		t.Errorf("listeners_count = %d, want 0", status.ListenersCount)
	}
}

func TestPoller_PollOnce_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	tenantLister := &mockPollerTenantLister{
		tenants: []tenant.PollableTenant{{ID: "tenant-4", LXCIP: addr}},
	}
	stationUpdater := newMockPollerStationUpdater()

	p := NewPoller(tenantLister, stationUpdater, 10*time.Second)
	p.WithSkipIPCheck()
	p.WithHTTPClient(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &rewriteTransport{base: http.DefaultTransport, testURL: srv.URL},
	})

	p.PollOnce(context.Background())

	status := p.GetStatus("tenant-4")
	if status == nil {
		t.Fatal("expected status, got nil")
	}
	if status.IsOnline {
		t.Error("expected is_online=false for bad json")
	}
}

func TestPoller_PollOnce_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	tenantLister := &mockPollerTenantLister{
		tenants: []tenant.PollableTenant{{ID: "tenant-5", LXCIP: addr}},
	}
	stationUpdater := newMockPollerStationUpdater()

	p := NewPoller(tenantLister, stationUpdater, 10*time.Second)
	p.WithSkipIPCheck()
	p.WithHTTPClient(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &rewriteTransport{base: http.DefaultTransport, testURL: srv.URL},
	})

	p.PollOnce(context.Background())

	status := p.GetStatus("tenant-5")
	if status == nil {
		t.Fatal("expected status, got nil")
	}
	if status.IsOnline {
		t.Error("expected is_online=false for HTTP 500")
	}
}

func TestPoller_ContextCancellation(t *testing.T) {
	tenantLister := &mockPollerTenantLister{
		tenants: []tenant.PollableTenant{{ID: "tenant-6", LXCIP: "10.10.10.99"}},
	}
	stationUpdater := newMockPollerStationUpdater()

	p := NewPoller(tenantLister, stationUpdater, 50*time.Millisecond)
	p.WithHTTPClient(&http.Client{
		Timeout: 100 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Let it run a couple cycles
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not stop after context cancellation")
	}
}

func TestPoller_SSRF_RejectsLoopback(t *testing.T) {
	if isAllowedIP("127.0.0.1") {
		t.Error("loopback should be rejected")
	}
}

func TestPoller_SSRF_RejectsLinkLocal(t *testing.T) {
	if isAllowedIP("169.254.1.1") {
		t.Error("link-local should be rejected")
	}
}

func TestPoller_SSRF_RejectsUnspecified(t *testing.T) {
	if isAllowedIP("0.0.0.0") {
		t.Error("unspecified should be rejected")
	}
}

func TestPoller_SSRF_RejectsPublicIP(t *testing.T) {
	if isAllowedIP("8.8.8.8") {
		t.Error("public IP should be rejected")
	}
}

func TestPoller_SSRF_Allows10Network(t *testing.T) {
	if !isAllowedIP("10.10.10.2") {
		t.Error("10.10.10.2 should be allowed")
	}
}

func TestPoller_SSRF_RejectsInvalidIP(t *testing.T) {
	if isAllowedIP("not-an-ip") {
		t.Error("invalid IP should be rejected")
	}
}

func TestPoller_SSRF_AllowsIPWithPort(t *testing.T) {
	if !isAllowedIP("10.10.10.2:80") {
		t.Error("10.10.10.2:80 should be allowed")
	}
}

// rewriteTransport rewrites all requests to point at the test server URL.
// This is needed because the poller constructs URLs from LXC IP + port 80,
// but we need to redirect to the httptest server.
type rewriteTransport struct {
	base    http.RoundTripper
	testURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = t.testURL[len("http://"):]
	return t.base.RoundTrip(req)
}
