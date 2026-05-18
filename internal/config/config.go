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
	DefaultCapturePort    = 8080
	DefaultQueueSize      = 1024
	DefaultBodyCap        = 1 << 20
	DefaultServiceHeader  = "X-Httpcatch-Service"
	DefaultMemoryCapacity = 1000
	DefaultSQLitePath     = "./httpcatch.db"
)

type SinksConfig struct {
	Stdout         bool   `yaml:"stdout"`
	Memory         bool   `yaml:"memory"`
	MemoryCapacity int    `yaml:"memory_capacity"`
	SQLite         bool   `yaml:"sqlite"`
	SQLitePath     string `yaml:"sqlite_path"`
}

// RedactionConfig holds the parsed redaction rules for use by the ruleset.
type RedactionConfig struct {
	Headers     []string
	QueryParams []string
	Cookies     []CookieRuleConfig
}

// CookieRuleConfig is one entry under `cookies:` — a mode plus the names the
// mode applies to. Mode is left as a string at the config layer; the ruleset
// loader validates it against the set of known modes.
type CookieRuleConfig struct {
	Mode  string
	Names []string
}

type Config struct {
	CapturePort   int             `yaml:"capture_port"`
	QueueSize     int             `yaml:"queue_size"`
	BodyCap       int             `yaml:"body_cap"`
	Workers       int             `yaml:"workers"`
	ServiceHeader string          `yaml:"service_header"`
	Sinks         SinksConfig     `yaml:"sinks"`
	Redaction     RedactionConfig
}

func Defaults() Config {
	return Config{
		CapturePort:   DefaultCapturePort,
		QueueSize:     DefaultQueueSize,
		BodyCap:       DefaultBodyCap,
		Workers:       runtime.NumCPU(),
		ServiceHeader: DefaultServiceHeader,
		Sinks: SinksConfig{
			MemoryCapacity: DefaultMemoryCapacity,
			SQLitePath:     DefaultSQLitePath,
		},
	}
}

type rawSinks struct {
	Stdout         *bool   `yaml:"stdout"`
	Memory         *bool   `yaml:"memory"`
	MemoryCapacity *int    `yaml:"memory_capacity"`
	SQLite         *bool   `yaml:"sqlite"`
	SQLitePath     *string `yaml:"sqlite_path"`
}

type rawRegexRule struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
}

type rawCookieRule struct {
	Mode  string   `yaml:"mode"`
	Names []string `yaml:"names"`
}

type rawRedactionConfig struct {
	Headers     []string        `yaml:"headers"`
	QueryParams []string        `yaml:"query_params"`
	JSONPaths   []string        `yaml:"json_paths"`
	Regex       []rawRegexRule  `yaml:"regex"`
	Cookies     []rawCookieRule `yaml:"cookies"`
}

var validRedactionKeys = map[string]bool{
	"headers":      true,
	"query_params": true,
	"json_paths":   true,
	"regex":        true,
	"cookies":      true,
}

func (r *rawRedactionConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		for i := 0; i < len(value.Content)-1; i += 2 {
			key := value.Content[i].Value
			if !validRedactionKeys[key] {
				return fmt.Errorf("redaction: unknown key %q", key)
			}
		}
	}
	type plain rawRedactionConfig
	return value.Decode((*plain)(r))
}

// Pointer fields distinguish "absent" from "set to zero" so the YAML cannot
// silently overwrite a default with the zero value.
type rawConfig struct {
	CapturePort   *int                `yaml:"capture_port"`
	QueueSize     *int                `yaml:"queue_size"`
	BodyCap       *int                `yaml:"body_cap"`
	Workers       *int                `yaml:"workers"`
	ServiceHeader *string             `yaml:"service_header"`
	Sinks         rawSinks            `yaml:"sinks"`
	Redaction     *rawRedactionConfig `yaml:"redaction"`
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
	if raw.Sinks.Memory != nil {
		cfg.Sinks.Memory = *raw.Sinks.Memory
	}
	if raw.Sinks.MemoryCapacity != nil {
		cfg.Sinks.MemoryCapacity = *raw.Sinks.MemoryCapacity
	}
	if raw.Sinks.SQLite != nil {
		cfg.Sinks.SQLite = *raw.Sinks.SQLite
	}
	if raw.Sinks.SQLitePath != nil {
		cfg.Sinks.SQLitePath = *raw.Sinks.SQLitePath
	}
	if raw.Redaction != nil {
		cfg.Redaction.Headers = raw.Redaction.Headers
		cfg.Redaction.QueryParams = raw.Redaction.QueryParams
		if len(raw.Redaction.Cookies) > 0 {
			cookies := make([]CookieRuleConfig, len(raw.Redaction.Cookies))
			for i, c := range raw.Redaction.Cookies {
				cookies[i] = CookieRuleConfig{Mode: c.Mode, Names: c.Names}
			}
			cfg.Redaction.Cookies = cookies
		}
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
		{"HTTPCATCH_MEMORY_CAPACITY", &cfg.Sinks.MemoryCapacity},
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
	if v := env("HTTPCATCH_SQLITE_PATH"); v != "" {
		cfg.Sinks.SQLitePath = v
	}
	if v := env("HTTPCATCH_SINKS"); v != "" {
		cfg.Sinks.Stdout = false
		cfg.Sinks.Memory = false
		cfg.Sinks.SQLite = false
		for _, name := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' }) {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "stdout":
				cfg.Sinks.Stdout = true
			case "memory":
				cfg.Sinks.Memory = true
			case "sqlite":
				cfg.Sinks.SQLite = true
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
	if c.Sinks.MemoryCapacity < 1 {
		errs = append(errs, fmt.Errorf("sinks.memory_capacity: must be >= 1, got %d", c.Sinks.MemoryCapacity))
	}
	if c.Sinks.SQLite && c.Sinks.SQLitePath == "" {
		errs = append(errs, fmt.Errorf("sinks.sqlite_path: must be set when sinks.sqlite is true"))
	}
	return errors.Join(errs...)
}
