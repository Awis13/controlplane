package sshexec

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Client executes commands on remote hosts via SSH.
type Client struct {
	keyPath string
	user    string
}

// NewClient creates a new SSH exec client.
func NewClient(keyPath string) *Client {
	return &Client{
		keyPath: keyPath,
		user:    "root",
	}
}

// shellEscape escapes a string for safe use inside single-quoted bash arguments.
// Replaces each ' with '\'' (end quote, escaped quote, start quote).
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// ExecInContainer runs a command inside an LXC container via pct exec.
// sshHost is the Proxmox node SSH address (extracted from proxmox_url).
// vmid is the LXC container ID.
// command is the shell command to execute inside the container.
func (c *Client) ExecInContainer(ctx context.Context, sshHost string, vmid int, command string) error {
	cmd := fmt.Sprintf("pct exec %d -- bash -c '%s'", vmid, shellEscape(command))
	if err := c.execCommand(ctx, sshHost, cmd); err != nil {
		return fmt.Errorf("pct exec: %w", err)
	}
	return nil
}

// ExecOnHost runs a command directly on the Proxmox node (not inside a container).
// sshHost is the Proxmox node SSH address (extracted from proxmox_url).
// command is the shell command to execute on the host.
// NOTE: no escaping is performed — the caller is responsible for command safety.
func (c *Client) ExecOnHost(ctx context.Context, sshHost string, command string) error {
	return c.execCommand(ctx, sshHost, command)
}

// execCommand is a shared helper for executing an arbitrary command via SSH.
func (c *Client) execCommand(ctx context.Context, sshHost string, fullCommand string) error {
	keyBytes, err := os.ReadFile(c.keyPath)
	if err != nil {
		return fmt.Errorf("read ssh key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return fmt.Errorf("parse ssh key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		// InsecureIgnoreHostKey is acceptable here — SSH traffic goes over WireGuard
		// trusted network (10.10.0.0/24). Proxmox nodes are not reachable without WG.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	addr := net.JoinHostPort(sshHost, "22")

	// Use context for connection timeout
	var conn net.Conn
	dialer := &net.Dialer{}
	conn, err = dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return fmt.Errorf("ssh handshake: %w", err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Close session on context cancellation
	done := make(chan error, 1)
	go func() {
		done <- session.Run(fullCommand)
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done // wait for goroutine to finish
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// ExtractHost parses a URL and returns only the hostname (without port).
// "https://10.10.0.2:8006" → "10.10.0.2"
func ExtractHost(rawURL string) (string, error) {
	// Add scheme if missing
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("empty host in url: %s", rawURL)
	}
	return host, nil
}
