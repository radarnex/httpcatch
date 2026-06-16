package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_DefaultsWhenEmpty(t *testing.T) {
	t.Parallel()

	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sinks.SQLite {
		t.Errorf("sqlite sink should be off by default")
	}
	if cfg.Sinks.SQLitePath != DefaultSQLitePath {
		t.Errorf("sqlite_path: got %q want %q", cfg.Sinks.SQLitePath, DefaultSQLitePath)
	}
	if cfg.CapturePort != DefaultCapturePort {
		t.Errorf("capture_port: got %d want %d", cfg.CapturePort, DefaultCapturePort)
	}
	if cfg.QueueSize != DefaultQueueSize {
		t.Errorf("queue_size: got %d want %d", cfg.QueueSize, DefaultQueueSize)
	}
	if cfg.BodyCap != DefaultBodyCap {
		t.Errorf("body_cap: got %d want %d", cfg.BodyCap, DefaultBodyCap)
	}
	if cfg.Workers != runtime.NumCPU() {
		t.Errorf("workers: got %d want %d (NumCPU)", cfg.Workers, runtime.NumCPU())
	}
	if cfg.Sinks.Stdout {
		t.Errorf("no sinks should be enabled by default")
	}
	if cfg.Sinks.Memory {
		t.Errorf("no sinks should be enabled by default")
	}
	if cfg.Sinks.MemoryCapacity != DefaultMemoryCapacity {
		t.Errorf("memory_capacity: got %d want %d", cfg.Sinks.MemoryCapacity, DefaultMemoryCapacity)
	}
	if cfg.ServiceHeader != DefaultServiceHeader {
		t.Errorf("service_header: got %q want %q", cfg.ServiceHeader, DefaultServiceHeader)
	}
}

func TestLoad_YAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
capture_port: 9090
queue_size: 4096
body_cap: 0
workers: 2
service_header: X-Custom-Service
sinks:
  stdout: true
  memory: true
  memory_capacity: 50
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CapturePort != 9090 {
		t.Errorf("capture_port: got %d want 9090", cfg.CapturePort)
	}
	if cfg.QueueSize != 4096 {
		t.Errorf("queue_size: got %d want 4096", cfg.QueueSize)
	}
	if cfg.BodyCap != 0 {
		t.Errorf("body_cap: got %d want 0 (explicitly disabled)", cfg.BodyCap)
	}
	if cfg.Workers != 2 {
		t.Errorf("workers: got %d want 2", cfg.Workers)
	}
	if !cfg.Sinks.Stdout {
		t.Errorf("stdout sink should be enabled from YAML")
	}
	if !cfg.Sinks.Memory {
		t.Errorf("memory sink should be enabled from YAML")
	}
	if cfg.Sinks.MemoryCapacity != 50 {
		t.Errorf("memory_capacity: got %d want 50", cfg.Sinks.MemoryCapacity)
	}
	if cfg.ServiceHeader != "X-Custom-Service" {
		t.Errorf("service_header: got %q want %q", cfg.ServiceHeader, "X-Custom-Service")
	}
}

func TestLoad_EnvOverridesYAMLAndDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
capture_port: 9090
queue_size: 8
workers: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := mapEnv(map[string]string{
		"HTTPCATCH_CAPTURE_PORT":    "12345",
		"HTTPCATCH_QUEUE_SIZE":      "16",
		"HTTPCATCH_BODY_CAP":        "2048",
		"HTTPCATCH_WORKER_COUNT":    "4",
		"HTTPCATCH_SERVICE_HEADER":  "X-Env-Service",
		"HTTPCATCH_SINKS":           "stdout,memory",
		"HTTPCATCH_MEMORY_CAPACITY": "25",
	})
	cfg, err := Load(path, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CapturePort != 12345 {
		t.Errorf("capture_port: got %d want 12345", cfg.CapturePort)
	}
	if cfg.QueueSize != 16 {
		t.Errorf("queue_size: got %d want 16", cfg.QueueSize)
	}
	if cfg.BodyCap != 2048 {
		t.Errorf("body_cap: got %d want 2048", cfg.BodyCap)
	}
	if cfg.Workers != 4 {
		t.Errorf("workers: got %d want 4", cfg.Workers)
	}
	if cfg.ServiceHeader != "X-Env-Service" {
		t.Errorf("service_header: got %q want %q", cfg.ServiceHeader, "X-Env-Service")
	}
	if !cfg.Sinks.Stdout {
		t.Errorf("stdout sink should be enabled via env")
	}
	if !cfg.Sinks.Memory {
		t.Errorf("memory sink should be enabled via env")
	}
	if cfg.Sinks.MemoryCapacity != 25 {
		t.Errorf("memory_capacity: got %d want 25", cfg.Sinks.MemoryCapacity)
	}
}

func TestLoad_EnvOnly_NoConfigFile(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_CAPTURE_PORT": "7777",
		"HTTPCATCH_SINKS":        "stdout",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CapturePort != 7777 {
		t.Errorf("capture_port: got %d want 7777", cfg.CapturePort)
	}
	if !cfg.Sinks.Stdout {
		t.Errorf("stdout sink should be enabled via env-only mode")
	}
}

func TestValidate_FieldSpecificErrors(t *testing.T) {
	t.Parallel()

	// Each case mutates a known-valid baseline so new validation rules don't
	// have to ripple through every row.
	base := func() Config {
		return Config{
			CapturePort: 8080,
			QueueSize:   1,
			Workers:     1,
			Sinks:       SinksConfig{MemoryCapacity: DefaultMemoryCapacity},
			Admin:       AdminConfig{SessionTTL: DefaultAdminSessionTTL},
		}
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		expect string
	}{
		{"zero capture port", func(c *Config) { c.CapturePort = 0 }, "capture_port"},
		{"negative capture port", func(c *Config) { c.CapturePort = -1 }, "capture_port"},
		{"out-of-range capture port", func(c *Config) { c.CapturePort = 70000 }, "capture_port"},
		{"zero queue size", func(c *Config) { c.QueueSize = 0 }, "queue_size"},
		{"negative queue size", func(c *Config) { c.QueueSize = -5 }, "queue_size"},
		{"zero workers", func(c *Config) { c.Workers = 0 }, "workers"},
		{"negative body cap", func(c *Config) { c.BodyCap = -1 }, "body_cap"},
		{"zero memory capacity", func(c *Config) { c.Sinks.MemoryCapacity = 0 }, "sinks.memory_capacity"},
		{"negative memory capacity", func(c *Config) { c.Sinks.MemoryCapacity = -3 }, "sinks.memory_capacity"},
		{"zero session ttl", func(c *Config) { c.Admin.SessionTTL = 0 }, "admin.session_ttl"},
		{"negative session ttl", func(c *Config) { c.Admin.SessionTTL = -time.Second }, "admin.session_ttl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Errorf("error %q does not mention %q", err, tt.expect)
			}
		})
	}
}

func TestLoad_RejectsZeroMemoryCapacity(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{"HTTPCATCH_MEMORY_CAPACITY": "0"})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "memory_capacity") {
		t.Errorf("error %q does not mention memory_capacity", err)
	}
}

func TestLoad_SQLiteFromYAMLAndEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
sinks:
  sqlite: true
  sqlite_path: /tmp/yaml.db
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load yaml: %v", err)
	}
	if !cfg.Sinks.SQLite {
		t.Errorf("sqlite sink should be enabled from yaml")
	}
	if cfg.Sinks.SQLitePath != "/tmp/yaml.db" {
		t.Errorf("sqlite_path: got %q want /tmp/yaml.db", cfg.Sinks.SQLitePath)
	}

	env := mapEnv(map[string]string{
		"HTTPCATCH_SINKS":       "sqlite",
		"HTTPCATCH_SQLITE_PATH": "/tmp/env.db",
	})
	cfg, err = Load(path, env)
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if !cfg.Sinks.SQLite {
		t.Errorf("sqlite sink should be enabled via HTTPCATCH_SINKS")
	}
	if cfg.Sinks.SQLitePath != "/tmp/env.db" {
		t.Errorf("sqlite_path: got %q want /tmp/env.db", cfg.Sinks.SQLitePath)
	}
	if cfg.Sinks.Stdout || cfg.Sinks.Memory {
		t.Errorf("HTTPCATCH_SINKS=sqlite should disable stdout/memory")
	}
}

func TestLoad_EnvSinksPreservesMemoryCapacity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
sinks:
  memory_capacity: 42
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := mapEnv(map[string]string{"HTTPCATCH_SINKS": "memory"})
	cfg, err := Load(path, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Sinks.Memory {
		t.Errorf("memory sink should be enabled via HTTPCATCH_SINKS")
	}
	if cfg.Sinks.Stdout {
		t.Errorf("stdout sink should not be enabled when HTTPCATCH_SINKS lists only memory")
	}
	if cfg.Sinks.MemoryCapacity != 42 {
		t.Errorf("memory_capacity from YAML should survive HTTPCATCH_SINKS reset: got %d want 42",
			cfg.Sinks.MemoryCapacity)
	}
}

func TestLoad_InvalidIntEnv(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{"HTTPCATCH_CAPTURE_PORT": "not-a-number"})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTPCATCH_CAPTURE_PORT") {
		t.Errorf("error %q does not mention HTTPCATCH_CAPTURE_PORT", err)
	}
}

func TestLoad_AdminDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.Bind != DefaultAdminBind {
		t.Errorf("admin.bind: got %q want %q", cfg.Admin.Bind, DefaultAdminBind)
	}
	if cfg.Admin.Token != "" {
		t.Errorf("admin.token: got %q want empty", cfg.Admin.Token)
	}
	if cfg.Admin.InsecureListen {
		t.Errorf("admin.insecure_listen: got true want false")
	}
	if cfg.Admin.SessionTTL != DefaultAdminSessionTTL {
		t.Errorf("admin.session_ttl: got %v want %v", cfg.Admin.SessionTTL, DefaultAdminSessionTTL)
	}
	if cfg.Admin.SessionSecure {
		t.Errorf("admin.session_secure: got true want false")
	}
}

func TestLoad_AdminYAMLBlock(t *testing.T) {
	t.Parallel()

	const wantToken = "a-secret-token-long-enough-to-be-valid-here"
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
admin:
  bind: 0.0.0.0:9090
  token: "` + wantToken + `"
  insecure_listen: true
  session_ttl: 30m
  session_secure: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.Bind != "0.0.0.0:9090" {
		t.Errorf("admin.bind: got %q want 0.0.0.0:9090", cfg.Admin.Bind)
	}
	if cfg.Admin.Token != wantToken {
		t.Errorf("admin.token: got %q want %q", cfg.Admin.Token, wantToken)
	}
	if !cfg.Admin.InsecureListen {
		t.Errorf("admin.insecure_listen: got false want true")
	}
	if cfg.Admin.SessionTTL != 30*time.Minute {
		t.Errorf("admin.session_ttl: got %v want 30m", cfg.Admin.SessionTTL)
	}
	if !cfg.Admin.SessionSecure {
		t.Errorf("admin.session_secure: got false want true")
	}
}

func TestLoad_AdminYAML_UnknownKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
admin:
  bind: 127.0.0.1:8081
  mystery_key: oops
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for unknown admin key, got nil")
	}
	if !strings.Contains(err.Error(), "mystery_key") {
		t.Errorf("error %q should mention the unknown key", err.Error())
	}
}

func TestLoad_AdminYAML_InvalidSessionTTL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
admin:
  session_ttl: "not-a-duration"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for invalid session_ttl, got nil")
	}
	if !strings.Contains(err.Error(), "admin.session_ttl") {
		t.Errorf("error %q should mention admin.session_ttl", err.Error())
	}
}

func TestLoad_AdminEnvOverrides(t *testing.T) {
	t.Parallel()

	const wantToken = "env-token-long-enough-to-pass-validation-check"
	env := mapEnv(map[string]string{
		"HTTPCATCH_ADMIN_BIND":            "0.0.0.0:9999",
		"HTTPCATCH_ADMIN_TOKEN":           wantToken,
		"HTTPCATCH_ADMIN_INSECURE_LISTEN": "true",
		"HTTPCATCH_ADMIN_SESSION_TTL":     "1h",
		"HTTPCATCH_ADMIN_SESSION_SECURE":  "1",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.Bind != "0.0.0.0:9999" {
		t.Errorf("admin.bind: got %q want 0.0.0.0:9999", cfg.Admin.Bind)
	}
	if cfg.Admin.Token != wantToken {
		t.Errorf("admin.token: got %q want %q", cfg.Admin.Token, wantToken)
	}
	if !cfg.Admin.InsecureListen {
		t.Errorf("admin.insecure_listen: got false want true")
	}
	if cfg.Admin.SessionTTL != time.Hour {
		t.Errorf("admin.session_ttl: got %v want 1h", cfg.Admin.SessionTTL)
	}
	if !cfg.Admin.SessionSecure {
		t.Errorf("admin.session_secure: got false want true")
	}
}

func TestLoad_AdminEnv_InvalidInsecureListen(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_ADMIN_INSECURE_LISTEN": "bogus",
	})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error for invalid HTTPCATCH_ADMIN_INSECURE_LISTEN, got nil")
	}
	if !strings.Contains(err.Error(), "HTTPCATCH_ADMIN_INSECURE_LISTEN") {
		t.Errorf("error %q should mention the env var name", err.Error())
	}
}

func TestLoad_AdminEnv_InvalidSessionTTL(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_ADMIN_SESSION_TTL": "not-a-duration",
	})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error for invalid HTTPCATCH_ADMIN_SESSION_TTL, got nil")
	}
	if !strings.Contains(err.Error(), "HTTPCATCH_ADMIN_SESSION_TTL") {
		t.Errorf("error %q should mention the env var name", err.Error())
	}
}

func TestLoad_AdminEnv_InvalidSessionSecure(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_ADMIN_SESSION_SECURE": "yes",
	})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error for invalid HTTPCATCH_ADMIN_SESSION_SECURE, got nil")
	}
	if !strings.Contains(err.Error(), "HTTPCATCH_ADMIN_SESSION_SECURE") {
		t.Errorf("error %q should mention the env var name", err.Error())
	}
}

func TestLoad_LogFormat_Default(t *testing.T) {
	t.Parallel()

	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("log_format: got %q want \"text\"", cfg.LogFormat)
	}
}

func TestLoad_LogFormat_YAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("log_format: json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("log_format: got %q want \"json\"", cfg.LogFormat)
	}
}

func TestLoad_LogFormat_Env(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{"HTTPCATCH_LOG_FORMAT": "json"})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("log_format: got %q want \"json\"", cfg.LogFormat)
	}
}

func TestLoad_LogFormat_Invalid(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{"HTTPCATCH_LOG_FORMAT": "logfmt"})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error for invalid log_format, got nil")
	}
	if !strings.Contains(err.Error(), "log_format") {
		t.Errorf("error %q should mention log_format", err.Error())
	}
}

func TestLoad_Timeouts_Defaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timeouts.ReadHeader != DefaultReadHeaderTimeout {
		t.Errorf("timeouts.read_header: got %v want %v", cfg.Timeouts.ReadHeader, DefaultReadHeaderTimeout)
	}
	if cfg.Timeouts.Read != DefaultReadTimeout {
		t.Errorf("timeouts.read: got %v want %v", cfg.Timeouts.Read, DefaultReadTimeout)
	}
	if cfg.Timeouts.Write != DefaultWriteTimeout {
		t.Errorf("timeouts.write: got %v want %v", cfg.Timeouts.Write, DefaultWriteTimeout)
	}
	if cfg.Timeouts.Idle != DefaultIdleTimeout {
		t.Errorf("timeouts.idle: got %v want %v", cfg.Timeouts.Idle, DefaultIdleTimeout)
	}
}

func TestLoad_Timeouts_YAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
timeouts:
  read_header: 5s
  read: 2m
  write: 45s
  idle: 3m
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timeouts.ReadHeader != 5*time.Second {
		t.Errorf("timeouts.read_header: got %v want 5s", cfg.Timeouts.ReadHeader)
	}
	if cfg.Timeouts.Read != 2*time.Minute {
		t.Errorf("timeouts.read: got %v want 2m", cfg.Timeouts.Read)
	}
	if cfg.Timeouts.Write != 45*time.Second {
		t.Errorf("timeouts.write: got %v want 45s", cfg.Timeouts.Write)
	}
	if cfg.Timeouts.Idle != 3*time.Minute {
		t.Errorf("timeouts.idle: got %v want 3m", cfg.Timeouts.Idle)
	}
}

func TestLoad_Timeouts_EnvOverrides(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_READ_HEADER_TIMEOUT": "1s",
		"HTTPCATCH_READ_TIMEOUT":        "10s",
		"HTTPCATCH_WRITE_TIMEOUT":       "20s",
		"HTTPCATCH_IDLE_TIMEOUT":        "90s",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timeouts.ReadHeader != time.Second {
		t.Errorf("timeouts.read_header: got %v want 1s", cfg.Timeouts.ReadHeader)
	}
	if cfg.Timeouts.Read != 10*time.Second {
		t.Errorf("timeouts.read: got %v want 10s", cfg.Timeouts.Read)
	}
	if cfg.Timeouts.Write != 20*time.Second {
		t.Errorf("timeouts.write: got %v want 20s", cfg.Timeouts.Write)
	}
	if cfg.Timeouts.Idle != 90*time.Second {
		t.Errorf("timeouts.idle: got %v want 90s", cfg.Timeouts.Idle)
	}
}

func TestLoad_Timeouts_ZeroDisables(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("timeouts:\n  read_header: 0s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timeouts.ReadHeader != 0 {
		t.Errorf("timeouts.read_header: got %v want 0 (disabled)", cfg.Timeouts.ReadHeader)
	}
}

func TestLoad_Timeouts_UnknownKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("timeouts:\n  mystery: 5s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for unknown timeouts key, got nil")
	}
	if !strings.Contains(err.Error(), "mystery") {
		t.Errorf("error %q should mention the unknown key", err.Error())
	}
}

func TestLoad_Timeouts_InvalidDuration(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{"HTTPCATCH_READ_TIMEOUT": "not-a-duration"})
	_, err := Load("", env)
	if err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
	if !strings.Contains(err.Error(), "HTTPCATCH_READ_TIMEOUT") {
		t.Errorf("error %q should mention the env var", err.Error())
	}
}

func TestValidate_RejectsNegativeTimeout(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("timeouts:\n  write: -5s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for negative timeout, got nil")
	}
	if !strings.Contains(err.Error(), "timeouts.write") {
		t.Errorf("error %q should mention timeouts.write", err.Error())
	}
}

func TestConfig_RetentionDefaults(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg.Sinks.Retention.MaxAge != 0 {
		t.Errorf("retention.max_age: got %v want 0 (disabled by default)", cfg.Sinks.Retention.MaxAge)
	}
	if cfg.Sinks.Retention.MaxCount != 0 {
		t.Errorf("retention.max_count: got %d want 0 (disabled by default)", cfg.Sinks.Retention.MaxCount)
	}
	if cfg.Sinks.Retention.Interval != 0 {
		t.Errorf("retention.interval: got %v want 0 (disabled by default)", cfg.Sinks.Retention.Interval)
	}
}

func TestConfig_RetentionValidation(t *testing.T) {
	t.Parallel()

	// base is a minimal valid config with SQLite enabled.
	base := func() Config {
		return Config{
			CapturePort: 8080,
			QueueSize:   1,
			Workers:     1,
			LogFormat:   DefaultLogFormat,
			Sinks: SinksConfig{
				MemoryCapacity: DefaultMemoryCapacity,
				SQLite:         true,
				SQLitePath:     "./test.db",
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		expect string
	}{
		{
			name: "both max_age and max_count set",
			mutate: func(c *Config) {
				c.Sinks.Retention.MaxAge = 7 * 24 * time.Hour
				c.Sinks.Retention.MaxCount = 1000
			},
			expect: "mutually exclusive",
		},
		{
			name: "negative max_age",
			mutate: func(c *Config) {
				c.Sinks.Retention.MaxAge = -time.Hour
			},
			expect: "retention.max_age",
		},
		{
			name: "negative max_count",
			mutate: func(c *Config) {
				c.Sinks.Retention.MaxCount = -1
			},
			expect: "retention.max_count",
		},
		{
			name: "negative interval",
			mutate: func(c *Config) {
				c.Sinks.Retention.Interval = -time.Minute
			},
			expect: "retention.interval",
		},
		{
			name: "retention without sqlite enabled",
			mutate: func(c *Config) {
				c.Sinks.SQLite = false
				c.Sinks.SQLitePath = ""
				c.Sinks.Retention.MaxAge = time.Hour
			},
			expect: "requires sinks.sqlite",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Errorf("error %q does not mention %q", err, tt.expect)
			}
		})
	}
}

func TestConfig_RetentionYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
sinks:
  sqlite: true
  sqlite_path: /tmp/test.db
  retention:
    max_age: 168h
    interval: 5m
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sinks.Retention.MaxAge != 168*time.Hour {
		t.Errorf("retention.max_age: got %v want 168h", cfg.Sinks.Retention.MaxAge)
	}
	if cfg.Sinks.Retention.Interval != 5*time.Minute {
		t.Errorf("retention.interval: got %v want 5m", cfg.Sinks.Retention.Interval)
	}
}

func TestConfig_RetentionYAML_MaxCount(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
sinks:
  sqlite: true
  sqlite_path: /tmp/test.db
  retention:
    max_count: 500
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sinks.Retention.MaxCount != 500 {
		t.Errorf("retention.max_count: got %d want 500", cfg.Sinks.Retention.MaxCount)
	}
}

func TestConfig_RetentionYAML_UnknownKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
sinks:
  sqlite: true
  sqlite_path: /tmp/test.db
  retention:
    maxcount: 100
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for unknown retention key, got nil")
	}
	if !strings.Contains(err.Error(), "maxcount") {
		t.Errorf("error %q should mention the unknown key", err.Error())
	}
}

func TestConfig_RetentionEnvOverrides(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_SINKS":                    "sqlite",
		"HTTPCATCH_SQLITE_PATH":              "/tmp/env.db",
		"HTTPCATCH_SINKS_RETENTION_MAX_AGE":  "24h",
		"HTTPCATCH_SINKS_RETENTION_INTERVAL": "2m",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sinks.Retention.MaxAge != 24*time.Hour {
		t.Errorf("retention.max_age: got %v want 24h", cfg.Sinks.Retention.MaxAge)
	}
	if cfg.Sinks.Retention.Interval != 2*time.Minute {
		t.Errorf("retention.interval: got %v want 2m", cfg.Sinks.Retention.Interval)
	}
}

func TestConfig_RetentionEnv_MaxCount(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_SINKS":                     "sqlite",
		"HTTPCATCH_SQLITE_PATH":               "/tmp/env.db",
		"HTTPCATCH_SINKS_RETENTION_MAX_COUNT": "250",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sinks.Retention.MaxCount != 250 {
		t.Errorf("retention.max_count: got %d want 250", cfg.Sinks.Retention.MaxCount)
	}
}

func TestConfig_MaxEventsPerBatch_Defaults(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg.MaxEventsPerBatch != DefaultMaxEventsPerBatch {
		t.Errorf("MaxEventsPerBatch default: got %d want %d", cfg.MaxEventsPerBatch, DefaultMaxEventsPerBatch)
	}
	if DefaultMaxEventsPerBatch != 1000 {
		t.Errorf("DefaultMaxEventsPerBatch: got %d want 1000", DefaultMaxEventsPerBatch)
	}
}

func TestConfig_MaxEventsPerBatch_YAMLOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := "max_events_per_batch: 500\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxEventsPerBatch != 500 {
		t.Errorf("MaxEventsPerBatch from YAML: got %d want 500", cfg.MaxEventsPerBatch)
	}
}

func TestConfig_MaxEventsPerBatch_YAMLZero_DisablesCap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := "max_events_per_batch: 0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxEventsPerBatch != 0 {
		t.Errorf("MaxEventsPerBatch zero: got %d want 0 (cap disabled)", cfg.MaxEventsPerBatch)
	}
}

func TestConfig_MaxEventsPerBatch_EnvOverride(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_MAX_EVENTS_PER_BATCH": "200",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxEventsPerBatch != 200 {
		t.Errorf("MaxEventsPerBatch from env: got %d want 200", cfg.MaxEventsPerBatch)
	}
}

func TestConfig_MaxEventsPerBatch_Validate_NegativeRejected(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	cfg.MaxEventsPerBatch = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative MaxEventsPerBatch")
	}
	if !strings.Contains(err.Error(), "max_events_per_batch") {
		t.Errorf("validation error does not mention max_events_per_batch: %v", err)
	}
}

func TestConfig_CaptureBind_Default_DerivedFromPort(t *testing.T) {
	t.Parallel()

	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// DefaultCapturePort is 8080; normalization must derive the bind address.
	if cfg.CaptureBind != "0.0.0.0:8080" {
		t.Errorf("CaptureBind: got %q want %q", cfg.CaptureBind, "0.0.0.0:8080")
	}
}

func TestConfig_CaptureBind_Explicit_Honored(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := "capture_bind: \"127.0.0.1:8080\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CaptureBind != "127.0.0.1:8080" {
		t.Errorf("CaptureBind: got %q want %q", cfg.CaptureBind, "127.0.0.1:8080")
	}
}

func TestConfig_CaptureBind_OverridesDerivation_WhenPortAlsoSet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := "capture_port: 9090\ncapture_bind: \"127.0.0.1:8080\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// capture_bind wins regardless of capture_port.
	if cfg.CaptureBind != "127.0.0.1:8080" {
		t.Errorf("CaptureBind: got %q want %q", cfg.CaptureBind, "127.0.0.1:8080")
	}
}

func TestConfig_CaptureBind_Env(t *testing.T) {
	t.Parallel()

	env := mapEnv(map[string]string{
		"HTTPCATCH_CAPTURE_BIND": "10.0.0.5:8080",
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CaptureBind != "10.0.0.5:8080" {
		t.Errorf("CaptureBind: got %q want %q", cfg.CaptureBind, "10.0.0.5:8080")
	}
}

func TestConfig_CaptureBind_Invalid_MissingPort(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := "capture_bind: \"127.0.0.1\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for capture_bind without port, got nil")
	}
	if !strings.Contains(err.Error(), "capture_bind") {
		t.Errorf("error %q does not mention capture_bind", err)
	}
}

func TestConfig_CaptureBind_Invalid_PortOutOfRange(t *testing.T) {
	t.Parallel()

	for _, bind := range []string{"127.0.0.1:0", "127.0.0.1:99999"} {
		bind := bind
		t.Run(bind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "c.yaml")
			body := "capture_bind: \"" + bind + "\"\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path, noEnv)
			if err == nil {
				t.Fatalf("expected error for capture_bind %q, got nil", bind)
			}
			if !strings.Contains(err.Error(), "capture_bind") {
				t.Errorf("error %q does not mention capture_bind", err)
			}
		})
	}
}
