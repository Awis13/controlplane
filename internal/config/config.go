package config

import (
	"fmt"
	"os"
)

type Config struct {
	DatabaseURL   string
	ListenAddr    string
	LogLevel      string
	APIToken      string
	EncryptionKey string
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

	return &Config{
		DatabaseURL:   dbURL,
		ListenAddr:    getEnv("LISTEN_ADDR", ":8080"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		APIToken:      apiToken,
		EncryptionKey: encKey,
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
