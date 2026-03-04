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

// Client выполняет команды на удалённых хостах через SSH.
type Client struct {
	keyPath string
	user    string
}

// NewClient создаёт новый SSH exec клиент.
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

// ExecInContainer выполняет команду внутри LXC контейнера через pct exec.
// sshHost — SSH адрес Proxmox ноды (извлекается из proxmox_url).
// vmid — ID LXC контейнера.
// command — shell команда для выполнения внутри контейнера.
func (c *Client) ExecInContainer(ctx context.Context, sshHost string, vmid int, command string) error {
	cmd := fmt.Sprintf("pct exec %d -- bash -c '%s'", vmid, shellEscape(command))
	if err := c.execCommand(ctx, sshHost, cmd); err != nil {
		return fmt.Errorf("pct exec: %w", err)
	}
	return nil
}

// ExecOnHost выполняет команду непосредственно на Proxmox ноде (не внутри контейнера).
// sshHost — SSH адрес Proxmox ноды (извлекается из proxmox_url).
// command — shell команда для выполнения на хосте.
// ВАЖНО: экранирование не выполняется — вызывающий код отвечает за безопасность команды.
func (c *Client) ExecOnHost(ctx context.Context, sshHost string, command string) error {
	return c.execCommand(ctx, sshHost, command)
}

// execCommand — общий хелпер для выполнения произвольной команды через SSH.
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

	// Используем контекст для таймаута подключения
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

	// Закрываем сессию при отмене контекста
	done := make(chan error, 1)
	go func() {
		done <- session.Run(fullCommand)
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done // дожидаемся завершения горутины
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// ExtractHost парсит URL и возвращает только hostname (без порта).
// "https://10.10.0.2:8006" → "10.10.0.2"
func ExtractHost(rawURL string) (string, error) {
	// Добавляем схему если отсутствует
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
