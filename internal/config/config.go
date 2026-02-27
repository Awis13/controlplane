package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL    string
	ListenAddr     string
	LogLevel       string
	APIToken       string
	EncryptionKey  string
	WebAuthnRPID   string
	WebAuthnOrigin string
	SetupToken     string   // optional: required for first WebAuthn registration
	JWTSecret      string   // required: HMAC-SHA256 secret for user JWT tokens
	CORSOrigins    []string // optional: allowed CORS origins (default: localhost dev ports)
	WGHubPublicKey string   // optional: WireGuard hub public key
	WGHubEndpoint  string   // optional: WireGuard hub endpoint (host:port)
	WGNetworkCIDR  string   // optional: WireGuard network CIDR (default: 10.10.0.0/24)
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
		WGHubPublicKey: os.Getenv("WG_HUB_PUBLIC_KEY"),
		WGHubEndpoint:  os.Getenv("WG_HUB_ENDPOINT"),
		WGNetworkCIDR:  getEnv("WG_NETWORK_CIDR", "10.10.0.0/24"),
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
