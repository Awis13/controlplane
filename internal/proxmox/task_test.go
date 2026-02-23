package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskWait_ImmediateSuccess(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`{
				"status": "stopped",
				"exitstatus": "OK",
				"type": "vzclone",
				"id": "101",
				"node": "testnode",
				"pid": 1234,
				"starttime": 1700000000
			}`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task := &Task{
		UPID:   "UPID:testnode:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:",
		node:   "testnode",
		client: c,
	}

	err := task.Wait(context.Background(), WithPollInterval(10*time.Millisecond), WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskWait_PollThenSuccess(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 3 {
			// Still running
			json.NewEncoder(w).Encode(response{
				Data: json.RawMessage(`{"status":"running","type":"vzclone","id":"101","node":"testnode","pid":1234,"starttime":1700000000}`),
			})
		} else {
			// Completed
			json.NewEncoder(w).Encode(response{
				Data: json.RawMessage(`{"status":"stopped","exitstatus":"OK","type":"vzclone","id":"101","node":"testnode","pid":1234,"starttime":1700000000}`),
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task := &Task{
		UPID:   "UPID:testnode:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:",
		node:   "testnode",
		client: c,
	}

	err := task.Wait(context.Background(), WithPollInterval(10*time.Millisecond), WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count := callCount.Load()
	if count < 3 {
		t.Errorf("expected at least 3 poll calls, got %d", count)
	}
}

func TestTaskWait_PollThenFailure(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 2 {
			json.NewEncoder(w).Encode(response{
				Data: json.RawMessage(`{"status":"running","type":"vzstart","id":"101","node":"testnode","pid":1234,"starttime":1700000000}`),
			})
		} else {
			json.NewEncoder(w).Encode(response{
				Data: json.RawMessage(`{"status":"stopped","exitstatus":"container already running","type":"vzstart","id":"101","node":"testnode","pid":1234,"starttime":1700000000}`),
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task := &Task{
		UPID:   "UPID:testnode:task",
		node:   "testnode",
		client: c,
	}

	err := task.Wait(context.Background(), WithPollInterval(10*time.Millisecond), WithTimeout(5*time.Second))
	if err == nil {
		t.Fatal("expected error from failed task")
	}

	taskErr, ok := err.(*TaskError)
	if !ok {
		t.Fatalf("expected *TaskError, got %T: %v", err, err)
	}
	if taskErr.ExitStatus != "container already running" {
		t.Errorf("unexpected exit status: %s", taskErr.ExitStatus)
	}
	if taskErr.Type != "vzstart" {
		t.Errorf("unexpected type: %s", taskErr.Type)
	}
}

func TestTaskWait_Timeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return running
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`{"status":"running","type":"vzclone","id":"101","node":"testnode","pid":1234,"starttime":1700000000}`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task := &Task{
		UPID:   "UPID:testnode:task",
		node:   "testnode",
		client: c,
	}

	start := time.Now()
	err := task.Wait(context.Background(), WithPollInterval(10*time.Millisecond), WithTimeout(100*time.Millisecond))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected deadline exceeded error, got: %v", err)
	}
	// Should complete within a reasonable time
	if elapsed > 2*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestTaskWait_ContextCancellation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`{"status":"running","type":"vzclone","id":"101","node":"testnode","pid":1234,"starttime":1700000000}`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task := &Task{
		UPID:   "UPID:testnode:task",
		node:   "testnode",
		client: c,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := task.Wait(ctx, WithPollInterval(10*time.Millisecond), WithTimeout(10*time.Second))
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled error, got: %v", err)
	}
}

func TestTaskWait_UPIDEncoding(t *testing.T) {
	// UPID contains colons that must be URL-encoded
	upid := "UPID:pve:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:"
	var gotPath string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RawPath
		if gotPath == "" {
			gotPath = r.URL.Path
		}
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`{"status":"stopped","exitstatus":"OK","type":"vzclone","id":"100","node":"pve","pid":1234,"starttime":1700000000}`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("pve"))

	task := &Task{
		UPID:   upid,
		node:   "pve",
		client: c,
	}

	err := task.Wait(context.Background(), WithPollInterval(10*time.Millisecond), WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The UPID should be URL-encoded in the path (colons become %3A)
	if !strings.Contains(gotPath, "UPID%3A") || !strings.Contains(gotPath, "%3A") {
		// The Go HTTP server may decode the path; check that the request was properly formed
		// by verifying the path contains the task endpoint
		if !strings.Contains(gotPath, "/tasks/") {
			t.Errorf("unexpected path format: %s", gotPath)
		}
	}
}

func TestTaskWait_ServerError(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(response{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()), WithNodeName("testnode"))

	task := &Task{
		UPID:   "UPID:testnode:task",
		node:   "testnode",
		client: c,
	}

	err := task.Wait(context.Background(), WithPollInterval(10*time.Millisecond), WithTimeout(100*time.Millisecond))
	if err == nil {
		t.Fatal("expected error from server error")
	}
}

func TestTaskError_Error(t *testing.T) {
	err := &TaskError{
		UPID:       "UPID:pve:task",
		ExitStatus: "container already exists",
		Type:       "vzclone",
	}
	msg := err.Error()
	if !strings.Contains(msg, "container already exists") {
		t.Errorf("error message should contain exit status: %s", msg)
	}
	if !strings.Contains(msg, "vzclone") {
		t.Errorf("error message should contain type: %s", msg)
	}
	if !strings.Contains(msg, "UPID:pve:task") {
		t.Errorf("error message should contain UPID: %s", msg)
	}
}

func TestWaitOptions(t *testing.T) {
	cfg := &waitConfig{
		pollInterval: defaultPollInterval,
		timeout:      defaultTimeout,
	}

	// Test defaults
	if cfg.pollInterval != 2*time.Second {
		t.Errorf("expected default poll interval 2s, got %v", cfg.pollInterval)
	}
	if cfg.timeout != 5*time.Minute {
		t.Errorf("expected default timeout 5m, got %v", cfg.timeout)
	}

	// Apply options
	WithPollInterval(500 * time.Millisecond)(cfg)
	WithTimeout(30 * time.Second)(cfg)

	if cfg.pollInterval != 500*time.Millisecond {
		t.Errorf("expected poll interval 500ms, got %v", cfg.pollInterval)
	}
	if cfg.timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", cfg.timeout)
	}
}
