package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultCapturePort       = 8080
	DefaultQueueSize         = 1024
	DefaultBodyCap           = 1 << 20
	DefaultMaxEventsPayload  = 1 << 20 // 1 MiB
	DefaultMaxEventsPerBatch = 1000
	DefaultServiceHeader     = "X-Httpcatch-Service"
	DefaultMemoryCapacity    = 1000
	DefaultSQLitePath        = "./httpcatch.db"
	DefaultAdminBind         = "127.0.0.1:8081"
	DefaultAdminSessionTTL   = 24 * time.Hour
	DefaultLogFormat         = "text"
	LogFormatJSON            = "json"

	// MinAdminTokenLength is the minimum number of characters required when an
	// admin token is configured. An empty token disables bearer auth entirely.
	MinAdminTokenLength = 32

	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 60 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 120 * time.Second

	// DefaultInspectQueryTimeout is the per-query deadline applied to inspect
	// reads. Zero disables the timeout.
	DefaultInspectQueryTimeout = 5 * time.Second
)

// TimeoutsConfig bounds how long a connection may occupy an HTTP server.
// The same values are applied to both the capture port and the admin port.
// ReadHeaderTimeout is the slow-loris defence: it bounds how long a client may
// take to send request headers. ReadTimeout bounds the full request including
// body; WriteTimeout bounds the response; IdleTimeout bounds keep-alive idle
// time. A zero value disables the corresponding Go http.Server timeout.
type TimeoutsConfig struct {
	ReadHeader time.Duration `yaml:"read_header"`
	Read       time.Duration `yaml:"read"`
	Write      time.Duration `yaml:"write"`
	Idle       time.Duration `yaml:"idle"`
}

// SinksRetentionConfig controls how httpcatch trims the SQLite store.
// Exactly one of MaxAge or MaxCount may be set; setting both is an error.
// A zero value for both disables retention (the store grows without bound).
// Interval controls how often the sweeper runs; zero defaults to 1 minute.
type SinksRetentionConfig struct {
	MaxAge   time.Duration `yaml:"max_age"`
	MaxCount int           `yaml:"max_count"`
	Interval time.Duration `yaml:"interval"`
}

type SinksConfig struct {
	Stdout         bool                 `yaml:"stdout"`
	Memory         bool                 `yaml:"memory"`
	MemoryCapacity int                  `yaml:"memory_capacity"`
	SQLite         bool                 `yaml:"sqlite"`
	SQLitePath     string               `yaml:"sqlite_path"`
	Retention      SinksRetentionConfig `yaml:"retention"`
}

// AdminConfig holds the parsed admin port settings.
type AdminConfig struct {
	Bind           string        `yaml:"bind"`
	Token          string        `yaml:"token"`
	InsecureListen bool          `yaml:"insecure_listen"`
	SessionTTL     time.Duration `yaml:"session_ttl"`
	SessionSecure  bool          `yaml:"session_secure"`
}

// InspectConfig holds settings for the inspect read API.
// QueryTimeout is the per-request deadline applied to every inspect read.
// A value of 0 disables the timeout (operator opt-out).
type InspectConfig struct {
	QueryTimeout time.Duration `yaml:"query_timeout"`
}

// RedactionConfig holds the parsed redaction rules for use by the ruleset.
type RedactionConfig struct {
	Headers     []string           `yaml:"headers"`
	QueryParams []string           `yaml:"query_params"`
	JSONPaths   []string           `yaml:"json_paths"`
	Regex       []RegexRuleConfig  `yaml:"regex"`
	Cookies     []CookieRuleConfig `yaml:"cookies"`
}

// CookieRuleConfig is one entry under `cookies:` — a mode plus the names the
// mode applies to. Mode is left as a string at the config layer; the ruleset
// loader validates it against the set of known modes.
type CookieRuleConfig struct {
	Mode  string   `yaml:"mode"`
	Names []string `yaml:"names"`
}

// RegexRuleConfig is one entry under `regex:` — a human-readable name plus the
// RE2 pattern. The pattern is left as a string at the config layer; the
// ruleset loader compiles it and surfaces any compile error attributed to the
// rule name.
type RegexRuleConfig struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
}

type Config struct {
	CapturePort       int             `yaml:"capture_port"`
	CaptureBind       string          `yaml:"capture_bind"`
	QueueSize         int             `yaml:"queue_size"`
	BodyCap           int             `yaml:"body_cap"`
	MaxEventsPayload  int             `yaml:"max_events_payload"`
	MaxEventsPerBatch int             `yaml:"max_events_per_batch"`
	Workers           int             `yaml:"workers"`
	ServiceHeader     string          `yaml:"service_header"`
	LogFormat         string          `yaml:"log_format"`
	Timeouts          TimeoutsConfig  `yaml:"timeouts"`
	Sinks             SinksConfig     `yaml:"sinks"`
	Redaction         RedactionConfig `yaml:"redaction"`
	Admin             AdminConfig     `yaml:"admin"`
	Inspect           InspectConfig   `yaml:"inspect"`
}

func Defaults() Config {
	return Config{
		CapturePort:       DefaultCapturePort,
		QueueSize:         DefaultQueueSize,
		BodyCap:           DefaultBodyCap,
		MaxEventsPayload:  DefaultMaxEventsPayload,
		MaxEventsPerBatch: DefaultMaxEventsPerBatch,
		Workers:           runtime.NumCPU(),
		ServiceHeader:     DefaultServiceHeader,
		LogFormat:         DefaultLogFormat,
		Timeouts: TimeoutsConfig{
			ReadHeader: DefaultReadHeaderTimeout,
			Read:       DefaultReadTimeout,
			Write:      DefaultWriteTimeout,
			Idle:       DefaultIdleTimeout,
		},
		Sinks: SinksConfig{
			MemoryCapacity: DefaultMemoryCapacity,
			SQLitePath:     DefaultSQLitePath,
		},
		Admin: AdminConfig{
			Bind:       DefaultAdminBind,
			SessionTTL: DefaultAdminSessionTTL,
		},
		Inspect: InspectConfig{
			QueryTimeout: DefaultInspectQueryTimeout,
		},
	}
}

type rawSinksRetentionConfig struct {
	MaxAge   *string `yaml:"max_age"`
	MaxCount *int    `yaml:"max_count"`
	Interval *string `yaml:"interval"`
}

var validSinksRetentionKeys = map[string]bool{
	"max_age":   true,
	"max_count": true,
	"interval":  true,
}

func (r *rawSinksRetentionConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		for i := 0; i < len(value.Content)-1; i += 2 {
			key := value.Content[i].Value
			if !validSinksRetentionKeys[key] {
				return fmt.Errorf("sinks.retention: unknown key %q", key)
			}
		}
	}
	type plain rawSinksRetentionConfig
	return value.Decode((*plain)(r))
}

type rawSinks struct {
	Stdout         *bool                    `yaml:"stdout"`
	Memory         *bool                    `yaml:"memory"`
	MemoryCapacity *int                     `yaml:"memory_capacity"`
	SQLite         *bool                    `yaml:"sqlite"`
	SQLitePath     *string                  `yaml:"sqlite_path"`
	Retention      *rawSinksRetentionConfig `yaml:"retention"`
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

type rawTimeoutsConfig struct {
	ReadHeader *string `yaml:"read_header"`
	Read       *string `yaml:"read"`
	Write      *string `yaml:"write"`
	Idle       *string `yaml:"idle"`
}

var validTimeoutsKeys = map[string]bool{
	"read_header": true,
	"read":        true,
	"write":       true,
	"idle":        true,
}

func (r *rawTimeoutsConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		for i := 0; i < len(value.Content)-1; i += 2 {
			key := value.Content[i].Value
			if !validTimeoutsKeys[key] {
				return fmt.Errorf("timeouts: unknown key %q", key)
			}
		}
	}
	type plain rawTimeoutsConfig
	return value.Decode((*plain)(r))
}

type rawAdminConfig struct {
	Bind           *string `yaml:"bind"`
	Token          *string `yaml:"token"`
	InsecureListen *bool   `yaml:"insecure_listen"`
	SessionTTL     *string `yaml:"session_ttl"`
	SessionSecure  *bool   `yaml:"session_secure"`
}

var validAdminKeys = map[string]bool{
	"bind":            true,
	"token":           true,
	"insecure_listen": true,
	"session_ttl":     true,
	"session_secure":  true,
}

func (r *rawAdminConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		for i := 0; i < len(value.Content)-1; i += 2 {
			key := value.Content[i].Value
			if !validAdminKeys[key] {
				return fmt.Errorf("admin: unknown key %q", key)
			}
		}
	}
	type plain rawAdminConfig
	return value.Decode((*plain)(r))
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

type rawInspectConfig struct {
	QueryTimeout *string `yaml:"query_timeout"`
}

var validInspectKeys = map[string]bool{
	"query_timeout": true,
}

func (r *rawInspectConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		for i := 0; i < len(value.Content)-1; i += 2 {
			key := value.Content[i].Value
			if !validInspectKeys[key] {
				return fmt.Errorf("inspect: unknown key %q", key)
			}
		}
	}
	type plain rawInspectConfig
	return value.Decode((*plain)(r))
}

// Pointer fields distinguish "absent" from "set to zero" so the YAML cannot
// silently overwrite a default with the zero value.
type rawConfig struct {
	CapturePort       *int                `yaml:"capture_port"`
	CaptureBind       *string             `yaml:"capture_bind"`
	QueueSize         *int                `yaml:"queue_size"`
	BodyCap           *int                `yaml:"body_cap"`
	MaxEventsPayload  *int                `yaml:"max_events_payload"`
	MaxEventsPerBatch *int                `yaml:"max_events_per_batch"`
	Workers           *int                `yaml:"workers"`
	ServiceHeader     *string             `yaml:"service_header"`
	LogFormat         *string             `yaml:"log_format"`
	Timeouts          *rawTimeoutsConfig  `yaml:"timeouts"`
	Sinks             rawSinks            `yaml:"sinks"`
	Redaction         *rawRedactionConfig `yaml:"redaction"`
	Admin             *rawAdminConfig     `yaml:"admin"`
	Inspect           *rawInspectConfig   `yaml:"inspect"`
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
		if err := applyRaw(&cfg, raw); err != nil {
			return cfg, fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	if env == nil {
		env = os.Getenv
	}
	if err := applyEnv(&cfg, env); err != nil {
		return cfg, err
	}
	if cfg.CaptureBind == "" {
		cfg.CaptureBind = fmt.Sprintf("0.0.0.0:%d", cfg.CapturePort)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyRaw(cfg *Config, raw rawConfig) error {
	if raw.CapturePort != nil {
		cfg.CapturePort = *raw.CapturePort
	}
	if raw.CaptureBind != nil {
		cfg.CaptureBind = *raw.CaptureBind
	}
	if raw.QueueSize != nil {
		cfg.QueueSize = *raw.QueueSize
	}
	if raw.BodyCap != nil {
		cfg.BodyCap = *raw.BodyCap
	}
	if raw.MaxEventsPayload != nil {
		cfg.MaxEventsPayload = *raw.MaxEventsPayload
	}
	if raw.MaxEventsPerBatch != nil {
		cfg.MaxEventsPerBatch = *raw.MaxEventsPerBatch
	}
	if raw.Workers != nil {
		cfg.Workers = *raw.Workers
	}
	if raw.ServiceHeader != nil {
		cfg.ServiceHeader = *raw.ServiceHeader
	}
	if raw.LogFormat != nil {
		cfg.LogFormat = *raw.LogFormat
	}
	if raw.Timeouts != nil {
		for _, step := range []struct {
			key string
			src *string
			dst *time.Duration
		}{
			{"read_header", raw.Timeouts.ReadHeader, &cfg.Timeouts.ReadHeader},
			{"read", raw.Timeouts.Read, &cfg.Timeouts.Read},
			{"write", raw.Timeouts.Write, &cfg.Timeouts.Write},
			{"idle", raw.Timeouts.Idle, &cfg.Timeouts.Idle},
		} {
			if step.src == nil {
				continue
			}
			d, err := time.ParseDuration(*step.src)
			if err != nil {
				return fmt.Errorf("timeouts.%s: invalid duration %q: %w", step.key, *step.src, err)
			}
			*step.dst = d
		}
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
	if raw.Sinks.Retention != nil {
		ret := raw.Sinks.Retention
		for _, step := range []struct {
			key string
			src *string
			dst *time.Duration
		}{
			{"max_age", ret.MaxAge, &cfg.Sinks.Retention.MaxAge},
			{"interval", ret.Interval, &cfg.Sinks.Retention.Interval},
		} {
			if step.src == nil {
				continue
			}
			d, err := time.ParseDuration(*step.src)
			if err != nil {
				return fmt.Errorf("sinks.retention.%s: invalid duration %q: %w", step.key, *step.src, err)
			}
			*step.dst = d
		}
		if ret.MaxCount != nil {
			cfg.Sinks.Retention.MaxCount = *ret.MaxCount
		}
	}
	if raw.Redaction != nil {
		cfg.Redaction.Headers = raw.Redaction.Headers
		cfg.Redaction.QueryParams = raw.Redaction.QueryParams
		cfg.Redaction.JSONPaths = raw.Redaction.JSONPaths
		if len(raw.Redaction.Regex) > 0 {
			regex := make([]RegexRuleConfig, len(raw.Redaction.Regex))
			for i, r := range raw.Redaction.Regex {
				regex[i] = RegexRuleConfig(r)
			}
			cfg.Redaction.Regex = regex
		}
		if len(raw.Redaction.Cookies) > 0 {
			cookies := make([]CookieRuleConfig, len(raw.Redaction.Cookies))
			for i, c := range raw.Redaction.Cookies {
				cookies[i] = CookieRuleConfig(c)
			}
			cfg.Redaction.Cookies = cookies
		}
	}
	if raw.Admin != nil {
		if raw.Admin.Bind != nil {
			cfg.Admin.Bind = *raw.Admin.Bind
		}
		if raw.Admin.Token != nil {
			cfg.Admin.Token = *raw.Admin.Token
		}
		if raw.Admin.InsecureListen != nil {
			cfg.Admin.InsecureListen = *raw.Admin.InsecureListen
		}
		if raw.Admin.SessionTTL != nil {
			d, err := time.ParseDuration(*raw.Admin.SessionTTL)
			if err != nil {
				return fmt.Errorf("admin.session_ttl: invalid duration %q: %w", *raw.Admin.SessionTTL, err)
			}
			cfg.Admin.SessionTTL = d
		}
		if raw.Admin.SessionSecure != nil {
			cfg.Admin.SessionSecure = *raw.Admin.SessionSecure
		}
	}
	if raw.Inspect != nil && raw.Inspect.QueryTimeout != nil {
		d, err := time.ParseDuration(*raw.Inspect.QueryTimeout)
		if err != nil {
			return fmt.Errorf("inspect.query_timeout: invalid duration %q: %w", *raw.Inspect.QueryTimeout, err)
		}
		cfg.Inspect.QueryTimeout = d
	}
	return nil
}

func applyEnv(cfg *Config, env func(string) string) error {
	for _, step := range []struct {
		name string
		dest *int
	}{
		{"HTTPCATCH_CAPTURE_PORT", &cfg.CapturePort},
		{"HTTPCATCH_QUEUE_SIZE", &cfg.QueueSize},
		{"HTTPCATCH_BODY_CAP", &cfg.BodyCap},
		{"HTTPCATCH_MAX_EVENTS_PAYLOAD", &cfg.MaxEventsPayload},
		{"HTTPCATCH_MAX_EVENTS_PER_BATCH", &cfg.MaxEventsPerBatch},
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
	if v := env("HTTPCATCH_CAPTURE_BIND"); v != "" {
		cfg.CaptureBind = v
	}
	if v := env("HTTPCATCH_SERVICE_HEADER"); v != "" {
		cfg.ServiceHeader = v
	}
	if v := env("HTTPCATCH_LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := env("HTTPCATCH_SQLITE_PATH"); v != "" {
		cfg.Sinks.SQLitePath = v
	}
	for _, step := range []struct {
		name string
		dest *time.Duration
	}{
		{"HTTPCATCH_SINKS_RETENTION_MAX_AGE", &cfg.Sinks.Retention.MaxAge},
		{"HTTPCATCH_SINKS_RETENTION_INTERVAL", &cfg.Sinks.Retention.Interval},
	} {
		v := env(step.name)
		if v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q: %w", step.name, v, err)
		}
		*step.dest = d
	}
	if v := env("HTTPCATCH_SINKS_RETENTION_MAX_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("HTTPCATCH_SINKS_RETENTION_MAX_COUNT: invalid integer %q: %w", v, err)
		}
		cfg.Sinks.Retention.MaxCount = n
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
	for _, step := range []struct {
		name string
		dest *time.Duration
	}{
		{"HTTPCATCH_READ_HEADER_TIMEOUT", &cfg.Timeouts.ReadHeader},
		{"HTTPCATCH_READ_TIMEOUT", &cfg.Timeouts.Read},
		{"HTTPCATCH_WRITE_TIMEOUT", &cfg.Timeouts.Write},
		{"HTTPCATCH_IDLE_TIMEOUT", &cfg.Timeouts.Idle},
	} {
		v := env(step.name)
		if v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q: %w", step.name, v, err)
		}
		*step.dest = d
	}
	if v := env("HTTPCATCH_ADMIN_BIND"); v != "" {
		cfg.Admin.Bind = v
	}
	if v := env("HTTPCATCH_ADMIN_TOKEN"); v != "" {
		cfg.Admin.Token = v
	}
	if v := env("HTTPCATCH_ADMIN_INSECURE_LISTEN"); v != "" {
		b, err := parseBoolEnv("HTTPCATCH_ADMIN_INSECURE_LISTEN", v)
		if err != nil {
			return err
		}
		cfg.Admin.InsecureListen = b
	}
	if v := env("HTTPCATCH_ADMIN_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("HTTPCATCH_ADMIN_SESSION_TTL: invalid duration %q: %w", v, err)
		}
		cfg.Admin.SessionTTL = d
	}
	if v := env("HTTPCATCH_ADMIN_SESSION_SECURE"); v != "" {
		b, err := parseBoolEnv("HTTPCATCH_ADMIN_SESSION_SECURE", v)
		if err != nil {
			return err
		}
		cfg.Admin.SessionSecure = b
	}
	if v := env("HTTPCATCH_INSPECT_QUERY_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("HTTPCATCH_INSPECT_QUERY_TIMEOUT: invalid duration %q: %w", v, err)
		}
		cfg.Inspect.QueryTimeout = d
	}
	return nil
}

func parseBoolEnv(name, v string) (bool, error) {
	switch v {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean %q: must be true/1 or false/0", name, v)
	}
}

func (c Config) Validate() error {
	var errs []error
	if c.CapturePort < 1 || c.CapturePort > 65535 {
		errs = append(errs, fmt.Errorf("capture_port: must be 1-65535, got %d", c.CapturePort))
	}
	if c.CaptureBind != "" {
		_, portStr, splitErr := net.SplitHostPort(c.CaptureBind)
		if splitErr != nil {
			errs = append(errs, fmt.Errorf("capture_bind: must be host:port with port 1-65535"))
		} else {
			p, parseErr := strconv.Atoi(portStr)
			if parseErr != nil || p < 1 || p > 65535 {
				errs = append(errs, fmt.Errorf("capture_bind: must be host:port with port 1-65535"))
			}
		}
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
	if c.MaxEventsPayload < 0 {
		errs = append(errs, fmt.Errorf("max_events_payload: must be >= 0, got %d", c.MaxEventsPayload))
	}
	if c.MaxEventsPerBatch < 0 {
		errs = append(errs, fmt.Errorf("max_events_per_batch: must be >= 0, got %d", c.MaxEventsPerBatch))
	}
	for _, t := range []struct {
		name string
		val  time.Duration
	}{
		{"timeouts.read_header", c.Timeouts.ReadHeader},
		{"timeouts.read", c.Timeouts.Read},
		{"timeouts.write", c.Timeouts.Write},
		{"timeouts.idle", c.Timeouts.Idle},
	} {
		if t.val < 0 {
			errs = append(errs, fmt.Errorf("%s: must be >= 0, got %s", t.name, t.val))
		}
	}
	if c.Sinks.MemoryCapacity < 1 {
		errs = append(errs, fmt.Errorf("sinks.memory_capacity: must be >= 1, got %d", c.Sinks.MemoryCapacity))
	}
	if c.Sinks.SQLite && c.Sinks.SQLitePath == "" {
		errs = append(errs, fmt.Errorf("sinks.sqlite_path: must be set when sinks.sqlite is true"))
	}
	if c.Sinks.Retention.MaxAge > 0 && c.Sinks.Retention.MaxCount > 0 {
		errs = append(errs, fmt.Errorf("sinks.retention: max_age and max_count are mutually exclusive"))
	}
	if c.Sinks.Retention.MaxAge < 0 {
		errs = append(errs, fmt.Errorf("sinks.retention.max_age: must be >= 0, got %s", c.Sinks.Retention.MaxAge))
	}
	if c.Sinks.Retention.MaxCount < 0 {
		errs = append(errs, fmt.Errorf("sinks.retention.max_count: must be >= 0, got %d", c.Sinks.Retention.MaxCount))
	}
	if c.Sinks.Retention.Interval < 0 {
		errs = append(errs, fmt.Errorf("sinks.retention.interval: must be >= 0, got %s", c.Sinks.Retention.Interval))
	}
	retentionSet := c.Sinks.Retention.MaxAge > 0 || c.Sinks.Retention.MaxCount > 0 || c.Sinks.Retention.Interval > 0
	if retentionSet && !c.Sinks.SQLite {
		errs = append(errs, fmt.Errorf("sinks.retention: requires sinks.sqlite: true"))
	}
	if c.LogFormat != DefaultLogFormat && c.LogFormat != LogFormatJSON {
		errs = append(errs, fmt.Errorf("log_format: must be %q or %q, got %q", DefaultLogFormat, LogFormatJSON, c.LogFormat))
	}
	if c.Admin.SessionTTL <= 0 {
		errs = append(errs, fmt.Errorf("admin.session_ttl: must be > 0, got %s", c.Admin.SessionTTL))
	}
	if c.Admin.Token != "" && len(c.Admin.Token) < MinAdminTokenLength {
		errs = append(errs, fmt.Errorf("admin.token: must be at least %d characters (got %d); generate with: openssl rand -base64 32", MinAdminTokenLength, len(c.Admin.Token)))
	}
	if c.Inspect.QueryTimeout < 0 {
		errs = append(errs, fmt.Errorf("inspect.query_timeout: must be >= 0, got %s", c.Inspect.QueryTimeout))
	}
	return errors.Join(errs...)
}
