package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		"HTTPCATCH_CAPTURE_PORT":   "12345",
		"HTTPCATCH_QUEUE_SIZE":     "16",
		"HTTPCATCH_BODY_CAP":       "2048",
		"HTTPCATCH_WORKER_COUNT":   "4",
		"HTTPCATCH_SERVICE_HEADER": "X-Env-Service",
		"HTTPCATCH_SINKS":          "stdout",
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

	tests := []struct {
		name   string
		cfg    Config
		expect string
	}{
		{
			name:   "zero capture port",
			cfg:    Config{CapturePort: 0, QueueSize: 1, Workers: 1},
			expect: "capture_port",
		},
		{
			name:   "negative capture port",
			cfg:    Config{CapturePort: -1, QueueSize: 1, Workers: 1},
			expect: "capture_port",
		},
		{
			name:   "out-of-range capture port",
			cfg:    Config{CapturePort: 70000, QueueSize: 1, Workers: 1},
			expect: "capture_port",
		},
		{
			name:   "zero queue size",
			cfg:    Config{CapturePort: 8080, QueueSize: 0, Workers: 1},
			expect: "queue_size",
		},
		{
			name:   "negative queue size",
			cfg:    Config{CapturePort: 8080, QueueSize: -5, Workers: 1},
			expect: "queue_size",
		},
		{
			name:   "zero workers",
			cfg:    Config{CapturePort: 8080, QueueSize: 1, Workers: 0},
			expect: "workers",
		},
		{
			name:   "negative body cap",
			cfg:    Config{CapturePort: 8080, QueueSize: 1, Workers: 1, BodyCap: -1},
			expect: "body_cap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Errorf("error %q does not mention %q", err, tt.expect)
			}
		})
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
