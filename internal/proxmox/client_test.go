package proxmox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_DefaultTLS(t *testing.T) {
	c := NewClient("https://example.com:8006", "PVEAPIToken=test@pam!tok=secret")
	if c.baseURL != "https://example.com:8006" {
		t.Errorf("unexpected baseURL: %s", c.baseURL)
	}
	if c.apiToken != "PVEAPIToken=test@pam!tok=secret" {
		t.Errorf("unexpected apiToken: %s", c.apiToken)
	}
	if c.httpClient == nil {
		t.Fatal("httpClient should not be nil")
	}
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport should be *http.Transport")
	}
	if !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true by default")
	}
}

func TestNewClient_WithOptions(t *testing.T) {
	customClient := &http.Client{}
	c := NewClient("https://host:8006/", "token",
		WithHTTPClient(customClient),
		WithNodeName("mynode"),
	)
	if c.httpClient != customClient {
		t.Error("custom HTTP client not applied")
	}
	if c.nodeName != "mynode" {
		t.Errorf("node name not set: got %q", c.nodeName)
	}
	// Trailing slash should be trimmed
	if c.baseURL != "https://host:8006" {
		t.Errorf("trailing slash not trimmed: %s", c.baseURL)
	}
}

func TestClient_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(response{Data: json.RawMessage(`[]`)})
	}))
	defer srv.Close()

	token := "PVEAPIToken=user@pam!mytoken=abc-123"
	c := NewClient(srv.URL, token, WithHTTPClient(srv.Client()), WithNodeName("test"))

	_ = c.get(context.Background(), "nodes", &[]nodeListEntry{})

	if gotAuth != token {
		t.Errorf("expected Authorization %q, got %q", token, gotAuth)
	}
}

func TestClient_Get_Success(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api2/json/nodes" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`[{"node":"pve"}]`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	var nodes []nodeListEntry
	err := c.get(context.Background(), "nodes", &nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Node != "pve" {
		t.Errorf("unexpected nodes: %+v", nodes)
	}
}

func TestClient_Get_APIError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(response{
			Errors: map[string]string{"permission": "denied"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "badtoken", WithHTTPClient(srv.Client()))

	err := c.get(context.Background(), "nodes", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", apiErr.StatusCode)
	}
	if apiErr.Errors["permission"] != "denied" {
		t.Errorf("unexpected errors: %v", apiErr.Errors)
	}
}

func TestClient_Get_FieldLevelErrors(t *testing.T) {
	// Proxmox can return 200 with errors in the body
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response{
			Errors: map[string]string{"newid": "VM 101 already exists"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	err := c.get(context.Background(), "some/path", nil)
	if err == nil {
		t.Fatal("expected error from field-level errors")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Errors["newid"] != "VM 101 already exists" {
		t.Errorf("unexpected errors: %v", apiErr.Errors)
	}
}

func TestClient_Post_FormEncoded(t *testing.T) {
	var gotContentType string
	var gotBody string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:pve:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	params := make(map[string][]string)
	params["newid"] = []string{"101"}
	params["full"] = []string{"1"}

	upid, err := c.post(context.Background(), "nodes/pve/lxc/100/clone", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("expected form-urlencoded, got %q", gotContentType)
	}
	// Check that form params were sent
	if gotBody == "" {
		t.Error("expected non-empty body")
	}
	if upid != "UPID:pve:001234AB:00ABCDEF:12345678:vzclone:100:root@pam:" {
		t.Errorf("unexpected UPID: %s", upid)
	}
}

func TestClient_Post_NilParams(t *testing.T) {
	var gotContentType string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:pve:task"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	_, err := c.post(context.Background(), "nodes/pve/lxc/100/status/start", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Content-Type should NOT be set when there are no params
	if gotContentType != "" {
		t.Errorf("Content-Type should be empty for nil params, got %q", gotContentType)
	}
}

func TestClient_Delete_WithParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:pve:delete"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	params := make(map[string][]string)
	params["force"] = []string{"1"}

	upid, err := c.delete(context.Background(), "nodes/pve/lxc/101", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != "force=1" {
		t.Errorf("expected query 'force=1', got %q", gotQuery)
	}
	if upid != "UPID:pve:delete" {
		t.Errorf("unexpected UPID: %s", upid)
	}
}

func TestClient_Delete_NilParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(response{
			Data: json.RawMessage(`"UPID:pve:del"`),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	_, err := c.delete(context.Background(), "nodes/pve/lxc/102", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("expected empty query, got %q", gotQuery)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response — the context should cancel before this returns
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token", WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := c.get(ctx, "nodes", nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestAPIError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      APIError
		contains string
	}{
		{
			name: "with field errors",
			err: APIError{
				StatusCode: 400,
				Status:     "400 Bad Request",
				Errors:     map[string]string{"vmid": "required"},
			},
			contains: "vmid: required",
		},
		{
			name: "without field errors",
			err: APIError{
				StatusCode: 500,
				Status:     "500 Internal Server Error",
			},
			contains: "500 Internal Server Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			if msg == "" {
				t.Error("error message should not be empty")
			}
			found := false
			if len(msg) >= len(tt.contains) {
				for i := 0; i <= len(msg)-len(tt.contains); i++ {
					if msg[i:i+len(tt.contains)] == tt.contains {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("error message %q should contain %q", msg, tt.contains)
			}
		})
	}
}

func TestClient_APIURLConstruction(t *testing.T) {
	c := NewClient("https://host:8006", "token")

	got := c.apiURL("nodes/pve/lxc")
	expected := "https://host:8006/api2/json/nodes/pve/lxc"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}

	// With leading slash
	got = c.apiURL("/nodes/pve/lxc")
	if got != expected {
		t.Errorf("expected %q, got %q (with leading slash)", expected, got)
	}
}
