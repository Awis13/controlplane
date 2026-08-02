package server

import (
	"testing"

	"controlplane/internal/config"
)

// baseConfig is the minimum New needs. The database pool is nil: New only wires
// stores and handlers, it does not query, so the wiring decisions can be
// checked without a database.
func baseConfig() *config.Config {
	return &config.Config{
		EncryptionKey:  "test-key",
		JWTSecret:      "test-secret",
		WebAuthnRPID:   "localhost",
		WebAuthnOrigin: "http://localhost",
	}
}

// TestNew_WiresFreeRadioAutoDeploy pins the wiring this ticket exists for.
// WithFreeRadioRepo had no caller, so the deploy code was unreachable however
// the environment was configured.
func TestNew_WiresFreeRadioAutoDeploy(t *testing.T) {
	cfg := baseConfig()
	cfg.FreeRadioRepoURL = "https://github.com/example/freeRadio.git"
	cfg.FreeRadioRepoBranch = "dev"

	_, prov, _, _, err := New(nil, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !prov.AutoDeployEnabled() {
		t.Error("a configured repo URL must reach the provisioner")
	}
}

// TestNew_NoFreeRadioRepoLeavesAutoDeployOff pins the default: without a repo
// URL the provisioner keeps the legacy dashboard-token behaviour.
func TestNew_NoFreeRadioRepoLeavesAutoDeployOff(t *testing.T) {
	cfg := baseConfig()
	cfg.FreeRadioRepoBranch = "dev"

	_, prov, _, _, err := New(nil, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if prov.AutoDeployEnabled() {
		t.Error("auto-deploy must stay off when no repo is configured")
	}
}
