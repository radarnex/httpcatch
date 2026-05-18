package config

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultCapturePort   = 8080
	DefaultQueueSize     = 1024
	DefaultBodyCap       = 1 << 20
	DefaultServiceHeader = "X-Httpcatch-Service"
)

type SinksConfig struct {
	Stdout bool `yaml:"stdout"`
}

type Config struct {
	CapturePort   int         `yaml:"capture_port"`
	QueueSize     int         `yaml:"queue_size"`
	BodyCap       int         `yaml:"body_cap"`
	Workers       int         `yaml:"workers"`
	ServiceHeader string      `yaml:"service_header"`
	Sinks         SinksConfig `yaml:"sinks"`
}

func Defaults() Config {
	return Config{
		CapturePort:   DefaultCapturePort,
		QueueSize:     DefaultQueueSize,
		BodyCap:       DefaultBodyCap,
		Workers:       runtime.NumCPU(),
		ServiceHeader: DefaultServiceHeader,
		Sinks:         SinksConfig{},
	}
}

type rawSinks struct {
	Stdout *bool `yaml:"stdout"`
}

// Pointer fields distinguish "absent" from "set to zero" so the YAML cannot
// silently overwrite a default with the zero value.
type rawConfig struct {
	CapturePort   *int     `yaml:"capture_port"`
	QueueSize     *int     `yaml:"queue_size"`
	BodyCap       *int     `yaml:"body_cap"`
	Workers       *int     `yaml:"workers"`
	ServiceHeader *string  `yaml:"service_header"`
	Sinks         rawSinks `yaml:"sinks"`
}

func Load(path string, env func(string) string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
		var raw rawConfig
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return cfg, fmt.Errorf("parse config %q: %w", path, err)
		}
		applyRaw(&cfg, raw)
	}
	if env == nil {
		env = os.Getenv
	}
	if err := applyEnv(&cfg, env); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyRaw(cfg *Config, raw rawConfig) {
	if raw.CapturePort != nil {
		cfg.CapturePort = *raw.CapturePort
	}
	if raw.QueueSize != nil {
		cfg.QueueSize = *raw.QueueSize
	}
	if raw.BodyCap != nil {
		cfg.BodyCap = *raw.BodyCap
	}
	if raw.Workers != nil {
		cfg.Workers = *raw.Workers
	}
	if raw.ServiceHeader != nil {
		cfg.ServiceHeader = *raw.ServiceHeader
	}
	if raw.Sinks.Stdout != nil {
		cfg.Sinks.Stdout = *raw.Sinks.Stdout
	}
}

func applyEnv(cfg *Config, env func(string) string) error {
	for _, step := range []struct {
		name string
		dest *int
	}{
		{"HTTPCATCH_CAPTURE_PORT", &cfg.CapturePort},
		{"HTTPCATCH_QUEUE_SIZE", &cfg.QueueSize},
		{"HTTPCATCH_BODY_CAP", &cfg.BodyCap},
		{"HTTPCATCH_WORKER_COUNT", &cfg.Workers},
	} {
		v := env(step.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q: %w", step.name, v, err)
		}
		*step.dest = n
	}
	if v := env("HTTPCATCH_SERVICE_HEADER"); v != "" {
		cfg.ServiceHeader = v
	}
	if v := env("HTTPCATCH_SINKS"); v != "" {
		cfg.Sinks = SinksConfig{}
		for _, name := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' }) {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "stdout":
				cfg.Sinks.Stdout = true
			default:
				return fmt.Errorf("HTTPCATCH_SINKS: unknown sink %q", name)
			}
		}
	}
	return nil
}

func (c Config) Validate() error {
	var errs []error
	if c.CapturePort < 1 || c.CapturePort > 65535 {
		errs = append(errs, fmt.Errorf("capture_port: must be 1-65535, got %d", c.CapturePort))
	}
	if c.QueueSize < 1 {
		errs = append(errs, fmt.Errorf("queue_size: must be >= 1, got %d", c.QueueSize))
	}
	if c.Workers < 1 {
		errs = append(errs, fmt.Errorf("workers: must be >= 1, got %d", c.Workers))
	}
	if c.BodyCap < 0 {
		errs = append(errs, fmt.Errorf("body_cap: must be >= 0, got %d", c.BodyCap))
	}
	return errors.Join(errs...)
}
