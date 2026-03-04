package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL    string
	ListenAddr     string
	LogLevel       string
	APIToken       string
	EncryptionKey  string
	WebAuthnRPID   string
	WebAuthnOrigin string
	SetupToken        string   // optional: required for first WebAuthn registration
	JWTSecret         string   // required: HMAC-SHA256 secret for user JWT tokens
	RegistrationToken string   // optional: if set, registration requires X-Registration-Token header
	CORSOrigins    []string // optional: allowed CORS origins (default: localhost dev ports)
	WGHubPublicKey  string // optional: WireGuard hub public key
	WGHubEndpoint   string // optional: WireGuard hub endpoint (host:port)
	WGNetworkCIDR   string // optional: WireGuard network CIDR (default: 10.10.0.0/24)
	CaddyAdminURL   string // optional: Caddy Admin API URL (e.g. http://172.17.0.1:2019)
	CaddyServerName string // optional: Caddy server name (default: srv1)
	CaddyDomain     string // optional: domain for tenant routes (default: freeradio.app)
	PollerInterval  time.Duration // optional: station status poll interval (default: 10s)
	SSHKeyPath      string        // optional: path to SSH key for pct exec (default: /root/.ssh/id_ed25519)
}

// Load reads configuration from environment variables.
// DATABASE_URL, API_TOKEN, and ENCRYPTION_KEY are required.
func Load() (*Config, error) {
	dbURL, err := requireEnv("DATABASE_URL")
	if err != nil {
		return nil, err
	}
	apiToken, err := requireEnv("API_TOKEN")
	if err != nil {
		return nil, err
	}
	encKey, err := requireEnv("ENCRYPTION_KEY")
	if err != nil {
		return nil, err
	}
	jwtSecret, err := requireEnv("JWT_SECRET")
	if err != nil {
		return nil, err
	}

	corsOrigins := parseCORSOrigins(os.Getenv("CORS_ORIGINS"))

	return &Config{
		DatabaseURL:    dbURL,
		ListenAddr:     getEnv("LISTEN_ADDR", "127.0.0.1:8080"),
		LogLevel:       getEnv("LOG_LEVEL", "info"),
		APIToken:       apiToken,
		EncryptionKey:  encKey,
		WebAuthnRPID:   os.Getenv("WEBAUTHN_RPID"),
		WebAuthnOrigin: os.Getenv("WEBAUTHN_ORIGIN"),
		SetupToken:     os.Getenv("SETUP_TOKEN"),
		JWTSecret:      jwtSecret,
		CORSOrigins:    corsOrigins,
		RegistrationToken: os.Getenv("REGISTRATION_TOKEN"),
		WGHubPublicKey:  os.Getenv("WG_HUB_PUBLIC_KEY"),
		WGHubEndpoint:   os.Getenv("WG_HUB_ENDPOINT"),
		WGNetworkCIDR:   getEnv("WG_NETWORK_CIDR", "10.10.0.0/24"),
		CaddyAdminURL:   os.Getenv("CADDY_ADMIN_URL"),
		CaddyServerName: getEnv("CADDY_SERVER_NAME", "srv1"),
		CaddyDomain:     getEnv("CADDY_DOMAIN", "freeradio.app"),
		PollerInterval:  parseDuration("POLLER_INTERVAL", 10*time.Second),
		SSHKeyPath:      getEnv("SSH_KEY_PATH", "/root/.ssh/id_ed25519"),
	}, nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return v, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseCORSOrigins parses CORS_ORIGINS env var (comma-separated).
// Falls back to localhost dev ports if not set.
func parseCORSOrigins(raw string) []string {
	if raw == "" {
		return []string{
			"http://localhost:5173",
			"http://localhost:5174",
		}
	}
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

// parseDuration parses a duration from an env var.
// Accepts both Go duration format ("30s", "1m") and plain seconds ("30").
// Falls back to the default if not set or invalid.
func parseDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}

	// Сначала пробуем Go-формат (30s, 1m, 500ms)
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}

	// Фолбэк: простые секунды (для обратной совместимости)
	if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
		return time.Duration(sec) * time.Second
	}

	return fallback
}
