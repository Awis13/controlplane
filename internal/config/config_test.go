package config

import (
	"os"
	"testing"
)

func TestParseCORSOrigins_Empty(t *testing.T) {
	origins := parseCORSOrigins("")
	if len(origins) != 2 {
		t.Fatalf("expected 2 defaults, got %d", len(origins))
	}
	if origins[0] != "http://localhost:5173" {
		t.Errorf("origins[0] = %q, want http://localhost:5173", origins[0])
	}
	if origins[1] != "http://localhost:5174" {
		t.Errorf("origins[1] = %q, want http://localhost:5174", origins[1])
	}
}

func TestParseCORSOrigins_SingleOrigin(t *testing.T) {
	origins := parseCORSOrigins("https://example.com")
	if len(origins) != 1 {
		t.Fatalf("expected 1 origin, got %d", len(origins))
	}
	if origins[0] != "https://example.com" {
		t.Errorf("origins[0] = %q, want https://example.com", origins[0])
	}
}

func TestParseCORSOrigins_MultipleOrigins(t *testing.T) {
	origins := parseCORSOrigins("https://a.com, https://b.com,https://c.com")
	if len(origins) != 3 {
		t.Fatalf("expected 3 origins, got %d", len(origins))
	}
	if origins[0] != "https://a.com" {
		t.Errorf("origins[0] = %q, want https://a.com", origins[0])
	}
	if origins[1] != "https://b.com" {
		t.Errorf("origins[1] = %q, want https://b.com", origins[1])
	}
	if origins[2] != "https://c.com" {
		t.Errorf("origins[2] = %q, want https://c.com", origins[2])
	}
}

func TestParseCORSOrigins_IgnoresEmptyEntries(t *testing.T) {
	origins := parseCORSOrigins("https://a.com,,, https://b.com, ,")
	if len(origins) != 2 {
		t.Fatalf("expected 2 origins, got %d: %v", len(origins), origins)
	}
}

func TestGetEnv_ReturnsValue(t *testing.T) {
	os.Setenv("TEST_GET_ENV_VAL", "hello")
	defer os.Unsetenv("TEST_GET_ENV_VAL")

	v := getEnv("TEST_GET_ENV_VAL", "fallback")
	if v != "hello" {
		t.Errorf("got %q, want %q", v, "hello")
	}
}

func TestGetEnv_ReturnsFallback(t *testing.T) {
	os.Unsetenv("TEST_GET_ENV_MISSING")

	v := getEnv("TEST_GET_ENV_MISSING", "fallback")
	if v != "fallback" {
		t.Errorf("got %q, want %q", v, "fallback")
	}
}

func TestRequireEnv_ReturnsValue(t *testing.T) {
	os.Setenv("TEST_REQUIRE_ENV", "present")
	defer os.Unsetenv("TEST_REQUIRE_ENV")

	v, err := requireEnv("TEST_REQUIRE_ENV")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "present" {
		t.Errorf("got %q, want %q", v, "present")
	}
}

func TestRequireEnv_ErrorWhenMissing(t *testing.T) {
	os.Unsetenv("TEST_REQUIRE_ENV_MISSING")

	_, err := requireEnv("TEST_REQUIRE_ENV_MISSING")
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestLoad_MissingDatabaseURL(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing DATABASE_URL")
	}
}

func TestLoad_MissingAPIToken(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	defer os.Unsetenv("DATABASE_URL")
	os.Unsetenv("API_TOKEN")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing API_TOKEN")
	}
}

func TestLoad_MissingEncryptionKey(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("API_TOKEN", "test-token")
	defer os.Unsetenv("DATABASE_URL")
	defer os.Unsetenv("API_TOKEN")
	os.Unsetenv("ENCRYPTION_KEY")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing ENCRYPTION_KEY")
	}
}

func TestLoad_MissingJWTSecret(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("API_TOKEN", "test-token")
	os.Setenv("ENCRYPTION_KEY", "test-key")
	defer os.Unsetenv("DATABASE_URL")
	defer os.Unsetenv("API_TOKEN")
	defer os.Unsetenv("ENCRYPTION_KEY")
	os.Unsetenv("JWT_SECRET")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing JWT_SECRET")
	}
}

func TestLoad_Success(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("API_TOKEN", "test-token")
	os.Setenv("ENCRYPTION_KEY", "test-key")
	os.Setenv("JWT_SECRET", "test-jwt-secret")
	os.Setenv("LISTEN_ADDR", "0.0.0.0:9090")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("WEBAUTHN_RPID", "example.com")
	os.Setenv("WEBAUTHN_ORIGIN", "https://example.com")
	os.Setenv("SETUP_TOKEN", "setup123")
	os.Setenv("CORS_ORIGINS", "https://app.example.com")
	defer func() {
		for _, k := range []string{
			"DATABASE_URL", "API_TOKEN", "ENCRYPTION_KEY", "JWT_SECRET",
			"LISTEN_ADDR", "LOG_LEVEL", "WEBAUTHN_RPID", "WEBAUTHN_ORIGIN",
			"SETUP_TOKEN", "CORS_ORIGINS",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.JWTSecret != "test-jwt-secret" {
		t.Errorf("JWTSecret = %q", cfg.JWTSecret)
	}
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "https://app.example.com" {
		t.Errorf("CORSOrigins = %v", cfg.CORSOrigins)
	}
	if cfg.WebAuthnRPID != "example.com" {
		t.Errorf("WebAuthnRPID = %q", cfg.WebAuthnRPID)
	}
	if cfg.SetupToken != "setup123" {
		t.Errorf("SetupToken = %q", cfg.SetupToken)
	}
}

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("API_TOKEN", "test-token")
	os.Setenv("ENCRYPTION_KEY", "test-key")
	os.Setenv("JWT_SECRET", "test-jwt-secret")
	os.Unsetenv("LISTEN_ADDR")
	os.Unsetenv("LOG_LEVEL")
	os.Unsetenv("CORS_ORIGINS")
	defer func() {
		for _, k := range []string{"DATABASE_URL", "API_TOKEN", "ENCRYPTION_KEY", "JWT_SECRET"} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr default = %q, want 127.0.0.1:8080", cfg.ListenAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want info", cfg.LogLevel)
	}
	if len(cfg.CORSOrigins) != 2 {
		t.Errorf("CORSOrigins default count = %d, want 2", len(cfg.CORSOrigins))
	}
}

// --- freeRadio auto-deploy ---

func TestLoad_FreeRadioDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("API_TOKEN", "test-token")
	t.Setenv("ENCRYPTION_KEY", "test-key")
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	os.Unsetenv("FREERADIO_REPO_URL")
	os.Unsetenv("FREERADIO_REPO_BRANCH")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// An unset URL is what keeps auto-deploy off and the legacy path in use.
	if cfg.FreeRadioRepoURL != "" {
		t.Errorf("FreeRadioRepoURL = %q, want empty so auto-deploy stays disabled", cfg.FreeRadioRepoURL)
	}
	if cfg.FreeRadioRepoBranch != "dev" {
		t.Errorf("FreeRadioRepoBranch = %q, want %q", cfg.FreeRadioRepoBranch, "dev")
	}
}

func TestLoad_FreeRadioFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("API_TOKEN", "test-token")
	t.Setenv("ENCRYPTION_KEY", "test-key")
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("FREERADIO_REPO_URL", "https://github.com/example/freeRadio.git")
	t.Setenv("FREERADIO_REPO_BRANCH", "main")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.FreeRadioRepoURL != "https://github.com/example/freeRadio.git" {
		t.Errorf("FreeRadioRepoURL = %q", cfg.FreeRadioRepoURL)
	}
	if cfg.FreeRadioRepoBranch != "main" {
		t.Errorf("FreeRadioRepoBranch = %q, want the configured branch to override the default", cfg.FreeRadioRepoBranch)
	}
}

// --- Topology ---

// TestLoad_TopologyDefaults pins that an unset environment reproduces exactly
// the values that used to be hardcoded. If one of these drifts, a deployment
// that never set the variable changes behaviour silently.
func TestLoad_TopologyDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("API_TOKEN", "test-token")
	t.Setenv("ENCRYPTION_KEY", "test-key")
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	// t.Setenv rather than os.Unsetenv: an empty value takes the same default
	// path, and the environment is restored when the test ends.
	for _, k := range []string{
		"LXC_BRIDGE", "TENANT_MOUNT_ROOT", "TENANT_APP_DIR", "CADDY_UPSTREAM_PORT",
		"SSH_USER", "SSH_PORT", "WG_INTERFACE", "WG_DNS_ADDR", "WG_KEEPALIVE_SECONDS",
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"LXCBridge", cfg.LXCBridge, "vmbr0"},
		{"TenantMountRoot", cfg.TenantMountRoot, "/mnt/tenants"},
		{"TenantAppDir", cfg.TenantAppDir, "/root/freeRadio"},
		{"CaddyUpstreamPort", cfg.CaddyUpstreamPort, "80"},
		{"SSHUser", cfg.SSHUser, "root"},
		{"SSHPort", cfg.SSHPort, "22"},
		{"WGInterface", cfg.WGInterface, "wg0"},
		{"WGDNSAddr", cfg.WGDNSAddr, "10.10.0.1"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want the previous literal %q", c.name, c.got, c.want)
		}
	}
	if cfg.WGKeepalive != 25 {
		t.Errorf("WGKeepalive = %d, want 25", cfg.WGKeepalive)
	}
}

func TestLoad_TopologyFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("API_TOKEN", "test-token")
	t.Setenv("ENCRYPTION_KEY", "test-key")
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("LXC_BRIDGE", "vmbr1")
	t.Setenv("TENANT_MOUNT_ROOT", "/srv/tenants")
	t.Setenv("TENANT_APP_DIR", "/opt/app")
	t.Setenv("CADDY_UPSTREAM_PORT", "8080")
	t.Setenv("SSH_USER", "operator")
	t.Setenv("SSH_PORT", "2222")
	t.Setenv("WG_INTERFACE", "wg1")
	t.Setenv("WG_DNS_ADDR", "10.20.0.1")
	t.Setenv("WG_KEEPALIVE_SECONDS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.LXCBridge != "vmbr1" || cfg.TenantMountRoot != "/srv/tenants" || cfg.TenantAppDir != "/opt/app" {
		t.Errorf("provisioner topology not read: %+v", cfg)
	}
	if cfg.CaddyUpstreamPort != "8080" || cfg.SSHUser != "operator" || cfg.SSHPort != "2222" {
		t.Errorf("caddy or ssh topology not read: %+v", cfg)
	}
	if cfg.WGInterface != "wg1" || cfg.WGDNSAddr != "10.20.0.1" || cfg.WGKeepalive != 45 {
		t.Errorf("wireguard topology not read: %+v", cfg)
	}
}

// TestParseInt_RejectsNonsense pins that a typo falls back rather than turning a
// tuning value into zero.
func TestParseInt_RejectsNonsense(t *testing.T) {
	for _, v := range []string{"", "abc", "0", "-5"} {
		t.Setenv("TEST_PARSE_INT", v)
		if got := parseInt("TEST_PARSE_INT", 25); got != 25 {
			t.Errorf("parseInt(%q) = %d, want the fallback 25", v, got)
		}
	}
	t.Setenv("TEST_PARSE_INT", "45")
	if got := parseInt("TEST_PARSE_INT", 25); got != 45 {
		t.Errorf("parseInt(\"45\") = %d, want 45", got)
	}
}
