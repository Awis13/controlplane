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
	// Записываем мусор вместо ключа
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

func TestExecInContainer_ValidKeyConnectionRefused(t *testing.T) {
	// Генерируем реальный ED25519 ключ
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
	// Подключаемся к localhost на порт, который не слушает
	err = c.ExecInContainer(t.Context(), "127.0.0.1", 100, "echo hello")
	if err == nil {
		t.Error("expected connection error")
	}
}
