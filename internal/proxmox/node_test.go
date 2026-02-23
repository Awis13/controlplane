package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeName_Discovery(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[{"node":"proxmox-ve"}]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	name, err := c.NodeName(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "proxmox-ve" {
		t.Errorf("expected 'proxmox-ve', got %q", name)
	}

	// Subsequent calls should use the cached name (no additional HTTP requests)
	name2, err := c.NodeName(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if name2 != "proxmox-ve" {
		t.Errorf("expected cached 'proxmox-ve', got %q", name2)
	}
}

func TestNodeName_ExplicitlySet(t *testing.T) {
	c := NewClient("https://host:8006", "token", WithNodeName("my-pve"))

	name, err := c.NodeName(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-pve" {
		t.Errorf("expected 'my-pve', got %q", name)
	}
}

func TestNodeName_NoNodesFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	_, err := c.NodeName(context.Background())
	if err == nil {
		t.Fatal("expected error when no nodes found")
	}
}

func TestNodeName_DiscoveryCached(t *testing.T) {
	callCount := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[{"node":"pve"}]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	// Call multiple times
	for i := 0; i < 5; i++ {
		_, err := c.NodeName(context.Background())
		if err != nil {
			t.Fatalf("unexpected error on call %d: %v", i, err)
		}
	}

	if callCount != 1 {
		t.Errorf("expected exactly 1 HTTP call for discovery, got %d", callCount)
	}
}

func TestGetNodeStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/testnode/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`{
				"cpu": 0.25,
				"memory": {"total": 17179869184, "used": 8589934592, "free": 8589934592},
				"uptime": 86400,
				"kversion": "6.2.16-3-pve",
				"pveversion": "pve-manager/8.1.3/bbf3993334bfa916"
			}`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	status, err := c.GetNodeStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.CPUUsage != 0.25 {
		t.Errorf("expected CPU 0.25, got %f", status.CPUUsage)
	}
	if status.MemoryTotal != 17179869184 {
		t.Errorf("expected MemoryTotal 17179869184, got %d", status.MemoryTotal)
	}
	if status.MemoryUsed != 8589934592 {
		t.Errorf("expected MemoryUsed 8589934592, got %d", status.MemoryUsed)
	}
	if status.MemoryFree != 8589934592 {
		t.Errorf("expected MemoryFree 8589934592, got %d", status.MemoryFree)
	}
	if status.Uptime != 86400 {
		t.Errorf("expected Uptime 86400, got %d", status.Uptime)
	}
	if status.KernelVersion != "6.2.16-3-pve" {
		t.Errorf("unexpected KernelVersion: %s", status.KernelVersion)
	}
	if status.PVEVersion != "pve-manager/8.1.3/bbf3993334bfa916" {
		t.Errorf("unexpected PVEVersion: %s", status.PVEVersion)
	}
}

func TestGetNodeStatus_Error(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(response{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.GetNodeStatus(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetNextID(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/cluster/nextid" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`105`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	id, err := c.GetNextID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 105 {
		t.Errorf("expected ID 105, got %d", id)
	}
}

func TestGetNextID_StringResponse(t *testing.T) {
	// Proxmox returns nextid as a string (e.g. "106")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"106"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	id, err := c.GetNextID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 106 {
		t.Errorf("expected ID 106, got %d", id)
	}
}

func TestGetNextID_Error(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(response{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.GetNextID(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNodeName_ConcurrentDiscovery(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// Simulate slow response to increase chance of concurrent access
		time.Sleep(10 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[{"node":"pve-concurrent"}]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errs := make([]error, goroutines)
	names := make([]string, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			name, err := c.NodeName(context.Background())
			names[idx] = name
			errs[idx] = err
		}(i)
	}

	wg.Wait()

	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, errs[i])
		}
		if names[i] != "pve-concurrent" {
			t.Errorf("goroutine %d: expected 'pve-concurrent', got %q", i, names[i])
		}
	}

	// Should have made very few HTTP calls (ideally 1, at most a few due to race timing)
	count := callCount.Load()
	if count > 3 {
		t.Errorf("expected at most a few HTTP calls, got %d (lock not working)", count)
	}
}
