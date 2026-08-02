package sshexec

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
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

// writeKey writes a usable ED25519 private key and returns its path.
func writeKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

func TestNewClient_GoodKey(t *testing.T) {
	c, err := NewClient(writeKey(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.signer == nil {
		t.Error("expected the key to be parsed at construction")
	}
	if c.user != "root" || c.port != "22" {
		t.Errorf("user = %q, port = %q, want the defaults", c.user, c.port)
	}
}

// TestNewClient_BadKeyFailsAtConstruction covers the change in when a bad key
// is reported. It used to surface on the first exec, which meant a
// misconfigured key looked like a failed provision rather than a failed start.
func TestNewClient_BadKeyFailsAtConstruction(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantMsg string
	}{
		{
			name:    "missing file",
			setup:   func(t *testing.T) string { return "/nonexistent/path/id_ed25519" },
			wantMsg: "read ssh key",
		},
		{
			name: "not a key",
			setup: func(t *testing.T) string {
				keyPath := filepath.Join(t.TempDir(), "bad_key")
				if err := os.WriteFile(keyPath, []byte("not a valid key"), 0600); err != nil {
					t.Fatal(err)
				}
				return keyPath
			},
			wantMsg: "parse ssh key",
		},
		{
			name: "empty file",
			setup: func(t *testing.T) string {
				keyPath := filepath.Join(t.TempDir(), "empty_key")
				if err := os.WriteFile(keyPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
				return keyPath
			},
			wantMsg: "parse ssh key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(tt.setup(t))
			if err == nil {
				t.Fatal("expected an error at construction")
			}
			if c != nil {
				t.Error("expected no client when the key is unusable")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestExecOnHost_ValidKeyConnectionRefused(t *testing.T) {
	c, err := NewClient(writeKey(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.ExecOnHost(t.Context(), "127.0.0.1", "echo hello"); err == nil {
		t.Error("expected connection error")
	}
}

func TestExecInContainer_ValidKeyConnectionRefused(t *testing.T) {
	c, err := NewClient(writeKey(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Connect to localhost on a port that is not listening.
	if err := c.ExecInContainer(t.Context(), "127.0.0.1", 100, "echo hello"); err == nil {
		t.Error("expected connection error")
	}
}

// --- Topology ---

// TestClientTopology_Defaults pins the login user and port this client used
// before they became configurable.
func TestClientTopology_Defaults(t *testing.T) {
	c := mustClient(t)

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
	c := mustClient(t).WithUser("operator").WithPort("2222")

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
	c := mustClient(t).WithUser("").WithPort("")

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
	c := mustClient(t).WithPort("2222")

	if got := c.dialAddr("fd00::1"); got != "[fd00::1]:2222" {
		t.Errorf("dialAddr = %q, want the host bracketed", got)
	}
}

// mustClient builds a client with a throwaway key, for tests about everything
// except the key itself.
func mustClient(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(writeKey(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// --- Quote ---

// TestQuote_LeavesWellFormedValuesAlone is what keeps this change invisible to
// existing commands: a path made of ordinary characters comes back untouched,
// so a command built from sane configuration reads exactly as it did.
func TestQuote_LeavesWellFormedValuesAlone(t *testing.T) {
	unchanged := []string{
		"/mnt/tenants",
		"/root/freeRadio",
		"/opt/app",
		"vmbr0",
		"dev",
		"https://github.com/example/freeRadio.git",
		"10.10.0.0/24",
		"user@example.com",
		"a-b_c.d",
		"105",
	}

	for _, v := range unchanged {
		if got := Quote(v); got != v {
			t.Errorf("Quote(%q) = %q, want it unchanged", v, got)
		}
	}
}

// TestQuote_ContainsValuesThatWouldChangeTheCommand covers the point of the
// function: a value carrying shell syntax becomes one argument instead of
// several, or of a second command.
func TestQuote_ContainsValuesThatWouldChangeTheCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "space splits an argument", input: "/mnt/my tenants", want: `'/mnt/my tenants'`},
		{name: "semicolon starts a command", input: "/mnt/t; rm -rf /", want: `'/mnt/t; rm -rf /'`},
		{name: "ampersand backgrounds", input: "/mnt/t && reboot", want: `'/mnt/t && reboot'`},
		{name: "pipe redirects", input: "/mnt/t | tee /x", want: `'/mnt/t | tee /x'`},
		{name: "command substitution", input: "/mnt/$(id)", want: `'/mnt/$(id)'`},
		{name: "backtick substitution", input: "/mnt/`id`", want: "'/mnt/`id`'"},
		{name: "variable expansion", input: "/mnt/$HOME", want: `'/mnt/$HOME'`},
		{name: "newline", input: "/mnt/t\nreboot", want: "'/mnt/t\nreboot'"},
		{name: "double quote", input: `/mnt/"t"`, want: `'/mnt/"t"'`},
		{name: "empty string", input: "", want: `''`},
		{name: "glob", input: "/mnt/*", want: `'/mnt/*'`},
		{name: "tilde", input: "~/tenants", want: `'~/tenants'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Quote(tt.input); got != tt.want {
				t.Errorf("Quote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestQuote_SingleQuoteCannotEscapeTheQuoting is the case that breaks naive
// quoting: a value containing a single quote must not be able to close the
// quoted string and continue as shell syntax.
func TestQuote_SingleQuoteCannotEscapeTheQuoting(t *testing.T) {
	got := Quote(`/mnt/t'; rm -rf /; echo '`)

	want := `'/mnt/t'\''; rm -rf /; echo '\'''`
	if got != want {
		t.Errorf("Quote = %s, want %s", got, want)
	}

	// Every quote in the result is either the wrapping pair or part of the
	// '\'' sequence, so nothing in the value is ever read as shell syntax.
	if strings.Contains(strings.ReplaceAll(got[1:len(got)-1], `'\''`, ""), "'") {
		t.Errorf("Quote = %s, leaves an unescaped quote in the body", got)
	}
}
