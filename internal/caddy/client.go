package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	routeIDPrefix = "tenant_"
	maxBodySnippet = 200
)

// Route represents a tenant route in Caddy.
type Route struct {
	Subdomain string
	TargetIP  string
}

// Client is an HTTP client for the Caddy Admin API.
type Client struct {
	baseURL    string
	serverName string
	domain     string
	http       *http.Client
}

// NewClient creates a new Caddy Admin API client.
// serverName defaults to "srv1", domain defaults to "freeradio.app".
func NewClient(baseURL, serverName, domain string) *Client {
	if serverName == "" {
		serverName = "srv1"
	}
	if domain == "" {
		domain = "freeradio.app"
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		serverName: serverName,
		domain:     domain,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// routeID returns the Caddy route @id for a subdomain.
func routeID(subdomain string) string {
	return routeIDPrefix + subdomain
}

// caddyRoute is the JSON structure for a Caddy route.
type caddyRoute struct {
	ID       string         `json:"@id"`
	Match    []caddyMatch   `json:"match"`
	Handle   []caddyHandler `json:"handle"`
	Terminal bool           `json:"terminal"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyHandler struct {
	Handler   string          `json:"handler"`
	Upstreams []caddyUpstream `json:"upstreams"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

// buildRouteJSON creates the Caddy route JSON for a tenant.
func (c *Client) buildRouteJSON(subdomain, targetIP string) ([]byte, error) {
	route := caddyRoute{
		ID: routeID(subdomain),
		Match: []caddyMatch{
			{Host: []string{subdomain + "." + c.domain}},
		},
		Handle: []caddyHandler{
			{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: targetIP + ":80"}},
			},
		},
		Terminal: true,
	}
	return json.Marshal(route)
}

// AddRoute adds a reverse proxy route for a tenant subdomain.
func (c *Client) AddRoute(ctx context.Context, subdomain, targetIP string) error {
	body, err := c.buildRouteJSON(subdomain, targetIP)
	if err != nil {
		return fmt.Errorf("caddy: marshal route: %w", err)
	}

	url := fmt.Sprintf("%s/config/apps/http/servers/%s/routes", c.baseURL, c.serverName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy: add route %q: %w", subdomain, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("caddy: add route %q: %s", subdomain, readErrorBody(resp))
	}

	return nil
}

// RemoveRoute removes a tenant route by subdomain. Returns nil if the route does not exist (idempotent).
func (c *Client) RemoveRoute(ctx context.Context, subdomain string) error {
	url := fmt.Sprintf("%s/id/%s", c.baseURL, routeID(subdomain))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("caddy: create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy: remove route %q: %w", subdomain, err)
	}
	defer resp.Body.Close()

	// 404 means route doesn't exist — that's fine (idempotent)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("caddy: remove route %q: %s", subdomain, readErrorBody(resp))
	}

	return nil
}

// ListRoutes returns all tenant routes currently configured in Caddy.
// Only routes with @id prefixed with "tenant_" are returned.
func (c *Client) ListRoutes(ctx context.Context) ([]Route, error) {
	url := fmt.Sprintf("%s/config/apps/http/servers/%s/routes", c.baseURL, c.serverName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("caddy: create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy: list routes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("caddy: list routes: %s", readErrorBody(resp))
	}

	var routes []caddyRoute
	if err := json.NewDecoder(resp.Body).Decode(&routes); err != nil {
		return nil, fmt.Errorf("caddy: decode routes: %w", err)
	}

	var result []Route
	for _, r := range routes {
		if !strings.HasPrefix(r.ID, routeIDPrefix) {
			continue
		}
		subdomain := strings.TrimPrefix(r.ID, routeIDPrefix)

		var targetIP string
		if len(r.Handle) > 0 && len(r.Handle[0].Upstreams) > 0 {
			dial := r.Handle[0].Upstreams[0].Dial
			// Strip :80 port suffix if present
			if idx := strings.LastIndex(dial, ":"); idx > 0 {
				targetIP = dial[:idx]
			} else {
				targetIP = dial
			}
		}

		result = append(result, Route{
			Subdomain: subdomain,
			TargetIP:  targetIP,
		})
	}

	return result, nil
}

// UpsertRoute ensures a route exists for the subdomain by removing any existing one first (DELETE+POST).
// This is idempotent: if the route does not exist, the DELETE returns 404 (which RemoveRoute treats as success).
func (c *Client) UpsertRoute(ctx context.Context, subdomain, targetIP string) error {
	if err := c.RemoveRoute(ctx, subdomain); err != nil {
		return fmt.Errorf("caddy: upsert route %q: delete failed: %w", subdomain, err)
	}
	if err := c.AddRoute(ctx, subdomain, targetIP); err != nil {
		return fmt.Errorf("caddy: upsert route %q: add failed: %w", subdomain, err)
	}
	return nil
}

// readErrorBody reads the response body and returns a formatted error string.
func readErrorBody(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxBodySnippet)))
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}
	return fmt.Sprintf("status %d: %s", resp.StatusCode, snippet)
}
