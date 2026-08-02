package sshexec

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"it's", `it'\''s`},
		{"a'b'c", `a'\''b'\''c`},
		{"abc123def", "abc123def"},
	}
	for _, tt := range tests {
		got := shellEscape(tt.input)
		if got != tt.want {
			t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{
			name:   "https with port",
			rawURL: "https://10.10.0.2:8006",
			want:   "10.10.0.2",
		},
		{
			name:   "https without port",
			rawURL: "https://10.10.0.2",
			want:   "10.10.0.2",
		},
		{
			name:   "http with port",
			rawURL: "http://192.168.1.1:8006",
			want:   "192.168.1.1",
		},
		{
			name:   "hostname with port",
			rawURL: "https://proxmox.example.com:8006",
			want:   "proxmox.example.com",
		},
		{
			name:   "no scheme",
			rawURL: "10.10.0.2:8006",
			want:   "10.10.0.2",
		},
		{
			name:    "empty string",
			rawURL:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractHost(tt.rawURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("/some/path/id_ed25519")
	if c.keyPath != "/some/path/id_ed25519" {
		t.Errorf("keyPath = %q, want %q", c.keyPath, "/some/path/id_ed25519")
	}
	if c.user != "root" {
		t.Errorf("user = %q, want %q", c.user, "root")
	}
}

func TestExecInContainer_InvalidKeyPath(t *testing.T) {
	c := NewClient("/nonexistent/path/id_ed25519")
	err := c.ExecInContainer(t.Context(), "10.10.0.2", 100, "echo hello")
	if err == nil {
		t.Error("expected error for invalid key path")
	}
}

func TestExecInContainer_InvalidKeyContent(t *testing.T) {
	// Write garbage instead of a valid key
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "bad_key")
	if err := os.WriteFile(keyPath, []byte("not a valid key"), 0600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(keyPath)
	err := c.ExecInContainer(t.Context(), "10.10.0.2", 100, "echo hello")
	if err == nil {
		t.Error("expected error for invalid key content")
	}
}

func TestExecOnHost_InvalidKeyPath(t *testing.T) {
	c := NewClient("/nonexistent/path/id_ed25519")
	err := c.ExecOnHost(t.Context(), "10.10.0.2", "echo hello")
	if err == nil {
		t.Error("expected error for invalid key path")
	}
}

func TestExecOnHost_InvalidKeyContent(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "bad_key")
	if err := os.WriteFile(keyPath, []byte("not a valid key"), 0600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(keyPath)
	err := c.ExecOnHost(t.Context(), "10.10.0.2", "echo hello")
	if err == nil {
		t.Error("expected error for invalid key content")
	}
}

func TestExecOnHost_ValidKeyConnectionRefused(t *testing.T) {
	// Generate a real ED25519 key
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(keyPath)
	err = c.ExecOnHost(t.Context(), "127.0.0.1", "echo hello")
	if err == nil {
		t.Error("expected connection error")
	}
}

func TestExecInContainer_ValidKeyConnectionRefused(t *testing.T) {
	// Generate a real ED25519 key
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(keyPath)
	// Connect to localhost on a port that is not listening
	err = c.ExecInContainer(t.Context(), "127.0.0.1", 100, "echo hello")
	if err == nil {
		t.Error("expected connection error")
	}
}

// --- Topology ---

// TestClientTopology_Defaults pins the login user and port this client used
// before they became configurable.
func TestClientTopology_Defaults(t *testing.T) {
	c := NewClient("/path/to/key")

	if c.user != "root" {
		t.Errorf("user = %q, want the previous default", c.user)
	}
	if c.port != "22" {
		t.Errorf("port = %q, want the previous default", c.port)
	}
	if got := c.dialAddr("10.0.0.1"); got != "10.0.0.1:22" {
		t.Errorf("dialAddr = %q, want the default port", got)
	}
}

// TestClientTopology_Overrides pins that configured values are kept and reach
// the address actually dialled.
func TestClientTopology_Overrides(t *testing.T) {
	c := NewClient("/path/to/key").WithUser("operator").WithPort("2222")

	if c.user != "operator" {
		t.Errorf("user = %q, want the configured user", c.user)
	}
	if c.port != "2222" {
		t.Errorf("port = %q, want the configured port", c.port)
	}
	if got := c.dialAddr("10.0.0.1"); got != "10.0.0.1:2222" {
		t.Errorf("dialAddr = %q, want the configured port", got)
	}
}

// TestClientTopology_EmptyOverridesKeepDefaults pins that a partially
// configured deployment does not lose the defaults, which is what an empty
// environment variable would otherwise do.
func TestClientTopology_EmptyOverridesKeepDefaults(t *testing.T) {
	c := NewClient("/path/to/key").WithUser("").WithPort("")

	if c.user != "root" || c.port != "22" {
		t.Errorf("empty overrides clobbered the defaults: user=%q port=%q", c.user, c.port)
	}
	if got := c.dialAddr("10.0.0.1"); got != "10.0.0.1:22" {
		t.Errorf("dialAddr = %q, want the default port", got)
	}
}

// TestClientTopology_IPv6HostIsBracketed pins that the address stays valid for
// an IPv6 host, which is why this uses net.JoinHostPort rather than a format
// string.
func TestClientTopology_IPv6HostIsBracketed(t *testing.T) {
	c := NewClient("/path/to/key").WithPort("2222")

	if got := c.dialAddr("fd00::1"); got != "[fd00::1]:2222" {
		t.Errorf("dialAddr = %q, want the host bracketed", got)
	}
}
