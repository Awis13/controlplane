package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Client is an HTTP client for the Proxmox VE REST API.
type Client struct {
	baseURL    string       // e.g. "https://100.72.87.45:8006"
	apiToken   string       // full "PVEAPIToken=user@realm!tokenid=secret"
	httpClient *http.Client
	nodeName   string       // Proxmox node name (e.g. "proxmox-ve"), discovered or set
	mu         sync.RWMutex // guards lazy node name discovery
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets a custom HTTP client (useful for testing).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithNodeName sets the Proxmox node name explicitly.
// If not set, the client discovers it via the API on first use.
func WithNodeName(name string) Option {
	return func(c *Client) {
		c.nodeName = name
	}
}

// NewClient creates a new Proxmox API client.
// baseURL should be the Proxmox host (e.g. "https://host:8006").
// apiToken is the full authentication string (e.g. "PVEAPIToken=user@pam!mytoken=uuid-secret").
func NewClient(baseURL, apiToken string, opts ...Option) *Client {
	c := &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiToken: apiToken,
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // Proxmox uses self-signed certs; traffic over Tailscale
				},
			},
		}
	}

	return c
}

// apiURL builds a full URL for a Proxmox API path.
// path should NOT include /api2/json/ prefix.
func (c *Client) apiURL(path string) string {
	return c.baseURL + "/api2/json/" + strings.TrimLeft(path, "/")
}

// get performs an authenticated GET request and decodes the response data into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(path), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	return c.decodeResponse(resp, out)
}

// post performs an authenticated POST request with form-encoded params.
// Returns the response data as a raw string (often a UPID for async tasks).
func (c *Client) post(ctx context.Context, path string, params url.Values) (string, error) {
	var body io.Reader
	if len(params) > 0 {
		body = strings.NewReader(params.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(path), body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", c.apiToken)
	if len(params) > 0 {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	var data string
	if err := c.decodeResponse(resp, &data); err != nil {
		return "", err
	}
	return data, nil
}

// delete performs an authenticated DELETE request with optional form-encoded params.
// Returns the response data as a raw string (often a UPID for async tasks).
func (c *Client) delete(ctx context.Context, path string, params url.Values) (string, error) {
	reqURL := c.apiURL(path)
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	var data string
	if err := c.decodeResponse(resp, &data); err != nil {
		return "", err
	}
	return data, nil
}

// decodeResponse parses the Proxmox response envelope and extracts data into out.
func (c *Client) decodeResponse(resp *http.Response, out any) error {
	const maxResponseSize = 10 * 1024 * 1024 // 10MB
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	slog.Debug("proxmox api response",
		"status", resp.StatusCode,
		"url", resp.Request.URL.String(),
		"body_len", len(bodyBytes),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope response
		// Try to parse errors from the body; ignore parse failures for non-JSON responses.
		_ = json.Unmarshal(bodyBytes, &envelope)
		return &APIError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Errors:     envelope.Errors,
		}
	}

	var envelope response
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return fmt.Errorf("decode response envelope: %w", err)
	}

	// Check for field-level errors even on 2xx responses.
	if len(envelope.Errors) > 0 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Errors:     envelope.Errors,
		}
	}

	if out != nil && envelope.Data != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode response data: %w", err)
		}
	}

	return nil
}

// resolveNode returns the node name, discovering it lazily if needed.
// Uses double-checked locking with RWMutex to avoid blocking concurrent
// callers after the initial discovery.
func (c *Client) resolveNode(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.nodeName != "" {
		name := c.nodeName
		c.mu.RUnlock()
		return name, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check after acquiring write lock (another goroutine may have discovered).
	if c.nodeName != "" {
		return c.nodeName, nil
	}

	name, err := c.discoverNodeName(ctx)
	if err != nil {
		return "", err
	}
	c.nodeName = name
	return name, nil
}
