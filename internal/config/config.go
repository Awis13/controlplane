package config

import (
	"os"
)

type Config struct {
	DatabaseURL string
	ListenAddr  string
	LogLevel    string
}

func Load() *Config {
	return &Config{
		DatabaseURL: getEnv("DATABASE_URL", "postgres://controlplane:changeme@postgres:5432/controlplane?sslmode=disable"),
		ListenAddr:  getEnv("LISTEN_ADDR", ":8080"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
