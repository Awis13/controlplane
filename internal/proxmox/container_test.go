package proxmox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListContainers(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api2/json/nodes/testnode/lxc" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[
				{"vmid":100,"name":"template-debian","status":"stopped","cpu":0,"mem":0,"maxmem":536870912,"disk":0,"maxdisk":8589934592,"uptime":0,"template":1},
				{"vmid":101,"name":"tenant-abc","status":"running","cpu":0.05,"mem":268435456,"maxmem":536870912,"disk":1073741824,"maxdisk":8589934592,"uptime":3600,"template":0}
			]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	containers, err := c.ListContainers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	// Check template
	if containers[0].VMID != 100 {
		t.Errorf("expected VMID 100, got %d", containers[0].VMID)
	}
	if containers[0].Name != "template-debian" {
		t.Errorf("expected name 'template-debian', got %q", containers[0].Name)
	}
	if containers[0].Template != 1 {
		t.Errorf("expected template=1, got %d", containers[0].Template)
	}
	if containers[0].Status != "stopped" {
		t.Errorf("expected status 'stopped', got %q", containers[0].Status)
	}

	// Check running container
	if containers[1].VMID != 101 {
		t.Errorf("expected VMID 101, got %d", containers[1].VMID)
	}
	if containers[1].Status != "running" {
		t.Errorf("expected status 'running', got %q", containers[1].Status)
	}
	if containers[1].Uptime != 3600 {
		t.Errorf("expected uptime 3600, got %d", containers[1].Uptime)
	}
	if containers[1].Mem != 268435456 {
		t.Errorf("expected mem 268435456, got %d", containers[1].Mem)
	}
}

func TestListContainers_Empty(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	containers, err := c.ListContainers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("expected 0 containers, got %d", len(containers))
	}
}

func TestGetContainerStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api2/json/nodes/testnode/lxc/101/status/current" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`{
				"vmid":101,"name":"tenant-abc","status":"running",
				"cpu":0.12,"cpus":2,"mem":268435456,"maxmem":536870912,
				"disk":1073741824,"maxdisk":8589934592,
				"swap":0,"maxswap":536870912,"uptime":7200,"pid":12345
			}`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	status, err := c.GetContainerStatus(context.Background(), 101)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.VMID != 101 {
		t.Errorf("expected VMID 101, got %d", status.VMID)
	}
	if status.Name != "tenant-abc" {
		t.Errorf("expected name 'tenant-abc', got %q", status.Name)
	}
	if status.Status != "running" {
		t.Errorf("expected status 'running', got %q", status.Status)
	}
	if status.CPUs != 2 {
		t.Errorf("expected 2 CPUs, got %d", status.CPUs)
	}
	if status.PID != 12345 {
		t.Errorf("expected PID 12345, got %d", status.PID)
	}
	if status.Swap != 0 {
		t.Errorf("expected swap 0, got %d", status.Swap)
	}
	if status.MaxSwap != 536870912 {
		t.Errorf("expected maxswap 536870912, got %d", status.MaxSwap)
	}
}

func TestGetContainerStatus_NotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(response{
			Errors: map[string]string{"vmid": "no such container"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.GetContainerStatus(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for non-existent container")
	}
	apiErr, ok := err.(*APIError)
	if ok && apiErr.Errors["vmid"] != "no such container" {
		t.Errorf("unexpected error details: %v", apiErr.Errors)
	}
}

func TestCloneContainer(t *testing.T) {
	var gotPath string
	var gotParams string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotParams = string(body)

		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task, err := c.CloneContainer(context.Background(), 100, CloneOptions{
		NewID:       101,
		Hostname:    "tenant-abc",
		Description: "Test tenant",
		Full:        true,
		Storage:     "local-lvm",
		Pool:        "tenants",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != "/api2/json/nodes/testnode/lxc/100/clone" {
		t.Errorf("unexpected path: %s", gotPath)
	}

	// Check form params
	if !strings.Contains(gotParams, "newid=101") {
		t.Errorf("missing newid param in %q", gotParams)
	}
	if !strings.Contains(gotParams, "hostname=tenant-abc") {
		t.Errorf("missing hostname param in %q", gotParams)
	}
	if !strings.Contains(gotParams, "full=1") {
		t.Errorf("missing full=1 param in %q", gotParams)
	}
	if !strings.Contains(gotParams, "storage=local-lvm") {
		t.Errorf("missing storage param in %q", gotParams)
	}
	if !strings.Contains(gotParams, "pool=tenants") {
		t.Errorf("missing pool param in %q", gotParams)
	}

	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.UPID != "UPID:testnode:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:" {
		t.Errorf("unexpected UPID: %s", task.UPID)
	}
	if task.node != "testnode" {
		t.Errorf("unexpected node: %s", task.node)
	}
}

func TestCloneContainer_MinimalOptions(t *testing.T) {
	var gotParams string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotParams = string(body)
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:clone"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.CloneContainer(context.Background(), 100, CloneOptions{
		NewID: 102,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have newid, not hostname/description/full/storage/pool
	if !strings.Contains(gotParams, "newid=102") {
		t.Errorf("missing newid param in %q", gotParams)
	}
	if strings.Contains(gotParams, "hostname") {
		t.Errorf("unexpected hostname param in %q", gotParams)
	}
	if strings.Contains(gotParams, "full") {
		t.Errorf("unexpected full param in %q (Full=false should not set it)", gotParams)
	}
	if strings.Contains(gotParams, "storage") {
		t.Errorf("unexpected storage param in %q", gotParams)
	}
}

func TestCloneContainer_Error(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(response{
			Errors: map[string]string{"newid": "VM 102 already exists"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.CloneContainer(context.Background(), 100, CloneOptions{NewID: 102})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStartContainer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api2/json/nodes/testnode/lxc/101/status/start" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:start101"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task, err := c.StartContainer(context.Background(), 101)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.UPID != "UPID:testnode:start101" {
		t.Errorf("unexpected UPID: %s", task.UPID)
	}
}

func TestStopContainer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/testnode/lxc/101/status/stop" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:stop101"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task, err := c.StopContainer(context.Background(), 101)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.UPID != "UPID:testnode:stop101" {
		t.Errorf("unexpected UPID: %s", task.UPID)
	}
}

func TestShutdownContainer(t *testing.T) {
	var gotParams string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/testnode/lxc/101/status/shutdown" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotParams = string(body)
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:shutdown101"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task, err := c.ShutdownContainer(context.Background(), 101, 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.UPID != "UPID:testnode:shutdown101" {
		t.Errorf("unexpected UPID: %s", task.UPID)
	}
	if !strings.Contains(gotParams, "timeout=30") {
		t.Errorf("missing timeout param in %q", gotParams)
	}
}

func TestShutdownContainer_NoTimeout(t *testing.T) {
	var gotParams string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotParams = string(body)
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:shutdown"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.ShutdownContainer(context.Background(), 101, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// timeout=0 should not be sent
	if strings.Contains(gotParams, "timeout") {
		t.Errorf("unexpected timeout param in %q", gotParams)
	}
}

func TestDeleteContainer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/api2/json/nodes/testnode/lxc/101" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("force") != "1" {
			t.Errorf("expected force=1 query param, got %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:delete101"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task, err := c.DeleteContainer(context.Background(), 101, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.UPID != "UPID:testnode:delete101" {
		t.Errorf("unexpected UPID: %s", task.UPID)
	}
}

func TestDeleteContainer_NoForce(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("force") != "" {
			t.Errorf("force should not be set, got %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:testnode:del"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	_, err := c.DeleteContainer(context.Background(), 101, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigureNetwork(t *testing.T) {
	var gotPath string
	var gotMethod string
	var gotParams string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		gotParams = string(body)
		_ = json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`null`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	net0 := "name=eth0,bridge=vmbr0,ip=10.10.10.5/24,gw=10.10.10.1,firewall=1,type=veth"
	err := c.ConfigureNetwork(context.Background(), 105, net0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	if gotPath != "/api2/json/nodes/testnode/lxc/105/config" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if !strings.Contains(gotParams, "net0=") {
		t.Errorf("missing net0 param in %q", gotParams)
	}
}

func TestConfigureNetwork_Error(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(response{
			Errors: map[string]string{"net0": "invalid network config"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	err := c.ConfigureNetwork(context.Background(), 105, "invalid")
	if err == nil {
		t.Fatal("expected error for invalid network config")
	}
}

func TestCloneContainer_InvalidNewID(t *testing.T) {
	c := NewClient("https://host:8006", "token", WithNodeName("testnode"))

	// NewID = 0 (zero value)
	_, err := c.CloneContainer(context.Background(), 100, CloneOptions{NewID: 0})
	if err == nil {
		t.Fatal("expected error for NewID=0")
	}
	if !strings.Contains(err.Error(), "NewID must be >= 100") {
		t.Errorf("unexpected error message: %v", err)
	}

	// NewID = 99 (below minimum)
	_, err = c.CloneContainer(context.Background(), 100, CloneOptions{NewID: 99})
	if err == nil {
		t.Fatal("expected error for NewID=99")
	}
}
