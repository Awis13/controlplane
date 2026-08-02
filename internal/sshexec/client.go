package sshexec

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Defaults are the values this client used before they became configurable.
const (
	defaultSSHUser = "root"
	defaultSSHPort = "22"
)

// dialAddr is the address this client connects to for a given host.
func (c *Client) dialAddr(host string) string {
	return net.JoinHostPort(host, c.port)
}

// Client executes commands on remote hosts via SSH.
type Client struct {
	signer ssh.Signer
	user   string
	port   string
}

// NewClient creates a new SSH exec client, reading and parsing the key up
// front. A missing or malformed key is a configuration error, and reporting it
// at startup beats discovering it on the first provision, where it surfaces as
// a failed tenant rather than a failed boot.
func NewClient(keyPath string) (*Client, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key %s: %w", keyPath, err)
	}

	return &Client{
		signer: signer,
		user:   defaultSSHUser,
		port:   defaultSSHPort,
	}, nil
}

// WithUser overrides the login user. Empty keeps the default.
func (c *Client) WithUser(user string) *Client {
	if user != "" {
		c.user = user
	}
	return c
}

// WithPort overrides the SSH port. Empty keeps the default.
func (c *Client) WithPort(port string) *Client {
	if port != "" {
		c.port = port
	}
	return c
}

// shellEscape escapes a string for safe use inside single-quoted bash arguments.
// Each embedded single quote becomes: end-quote, backslash-escaped quote, start-quote.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// shellSafe matches values that carry no meaning to a shell, so they can be
// interpolated as they are. Anything outside this set has to be quoted.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Quote renders a value safe to interpolate into a shell command.
//
// A value made only of ordinary path and identifier characters is returned
// unchanged, so commands built from well-formed configuration read exactly as
// they did before. Anything else, including the empty string, comes back
// single-quoted with embedded quotes escaped, so a value carrying a space, a
// semicolon or a quote becomes one argument instead of changing the shape of
// the command.
//
// Callers building a command from values they did not author should pass each
// one through this. ExecOnHost cannot do it for them: by the time it sees the
// command, the pieces have already been joined and the damage is done.
func Quote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + shellEscape(s) + "'"
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
//
// The command runs as given: it is a shell line, not an argument vector, so
// operators and redirection in it are meant to work. Any value interpolated
// into it that the caller did not author must be passed through Quote first.
func (c *Client) ExecOnHost(ctx context.Context, sshHost string, command string) error {
	return c.execCommand(ctx, sshHost, command)
}

// execCommand is a shared helper for executing an arbitrary command via SSH.
func (c *Client) execCommand(ctx context.Context, sshHost string, fullCommand string) error {
	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(c.signer),
		},
		// InsecureIgnoreHostKey is acceptable here — SSH traffic goes over WireGuard
		// trusted network (10.10.0.0/24). Proxmox nodes are not reachable without WG.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	addr := c.dialAddr(sshHost)

	// Use context for connection timeout
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
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
