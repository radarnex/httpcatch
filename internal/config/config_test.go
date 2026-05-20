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

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
admin:
  bind: 0.0.0.0:9090
  token: "secret"
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
	if cfg.Admin.Token != "secret" {
		t.Errorf("admin.token: got %q want secret", cfg.Admin.Token)
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

	env := mapEnv(map[string]string{
		"HTTPCATCH_ADMIN_BIND":            "0.0.0.0:9999",
		"HTTPCATCH_ADMIN_TOKEN":           "mytoken",
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
	if cfg.Admin.Token != "mytoken" {
		t.Errorf("admin.token: got %q want mytoken", cfg.Admin.Token)
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
