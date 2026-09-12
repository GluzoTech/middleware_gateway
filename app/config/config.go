// Package config loads and validates the gateway's runtime configuration.
//
// All configuration comes from environment variables so that secrets never
// live in source control. Load reads the process environment; LoadFrom takes
// an explicit lookup function so tests can supply their own values.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Supported values for APP_ENV.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// ServiceName identifies the gateway in logs and health responses.
const ServiceName = "gluzo-integration-gateway"

// Config is the fully resolved configuration for one process.
type Config struct {
	App      App
	HTTP     HTTP
	Database Database
	Redis    Redis
	Storage  Storage
	EasyEcom EasyEcom
}

// EasyEcom holds credentials for outbound EasyEcom API calls. Every call
// carries the account API key; the JWT is either supplied directly or
// obtained by logging in with email, password and location key. Presence of
// credentials is validated when the EasyEcom client is built, so a process
// that only receives webhooks can start without them.
type EasyEcom struct {
	BaseURL     string
	APIKey      string
	JWTToken    string
	Email       string
	Password    string
	LocationKey string
	Timeout     time.Duration
}

// App holds process-level settings.
type App struct {
	Env             string
	Port            int
	LogLevel        string
	ShutdownTimeout time.Duration
}

// IsProduction reports whether the process runs with production settings.
func (a App) IsProduction() bool { return a.Env == EnvProduction }

// HTTP holds server hardening settings.
type HTTP struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxBodyBytes      int64
}

// Database holds PostgreSQL connection settings.
type Database struct {
	URL            string
	MaxConns       int32
	ConnectTimeout time.Duration
}

// Redis holds Redis connection settings.
type Redis struct {
	URL            string
	ConnectTimeout time.Duration
}

// Storage holds file-system locations for execution logs and workflow state.
type Storage struct {
	LogDirectory      string
	WorkflowDirectory string
	LogRetentionDays  int
}

// Lookup resolves an environment variable, reporting whether it was set.
type Lookup func(key string) (string, bool)

// Load reads configuration from the process environment.
func Load() (*Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom reads configuration through lookup and validates it.
//
// Blank values are treated as unset so that a `.env` file with empty
// placeholders falls back to defaults instead of producing parse errors.
func LoadFrom(lookup Lookup) (*Config, error) {
	r := reader{lookup: lookup}

	cfg := &Config{
		App: App{
			Env:             r.str("APP_ENV", EnvDevelopment),
			Port:            r.int("APP_PORT", 8080),
			LogLevel:        r.str("LOG_LEVEL", "info"),
			ShutdownTimeout: r.duration("APP_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		HTTP: HTTP{
			ReadHeaderTimeout: r.duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       r.duration("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      r.duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:       r.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			MaxBodyBytes:      r.int64("HTTP_MAX_BODY_BYTES", 1<<20),
		},
		Database: Database{
			URL:            r.str("DATABASE_URL", ""),
			MaxConns:       int32(r.int("DATABASE_MAX_CONNS", 10)),
			ConnectTimeout: r.duration("DATABASE_CONNECT_TIMEOUT", 5*time.Second),
		},
		Redis: Redis{
			URL:            r.str("REDIS_URL", ""),
			ConnectTimeout: r.duration("REDIS_CONNECT_TIMEOUT", 5*time.Second),
		},
		Storage: Storage{
			LogDirectory:      r.str("LOG_DIRECTORY", "./storage/logs"),
			WorkflowDirectory: r.str("WORKFLOW_DIRECTORY", "./storage/workflows"),
			LogRetentionDays:  r.int("LOG_RETENTION_DAYS", 30),
		},
		EasyEcom: EasyEcom{
			BaseURL:     r.str("EASYECOM_BASE_URL", "https://api.easyecom.io"),
			APIKey:      r.str("EASYECOM_API_KEY", ""),
			JWTToken:    r.str("EASYECOM_JWT_TOKEN", ""),
			Email:       r.str("EASYECOM_EMAIL", ""),
			Password:    r.str("EASYECOM_PASSWORD", ""),
			LocationKey: r.str("EASYECOM_LOCATION_KEY", ""),
			Timeout:     r.duration("EASYECOM_TIMEOUT", 15*time.Second),
		},
	}

	if err := r.err(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks invariants that parsing alone cannot express.
func (c *Config) Validate() error {
	var errs []error

	switch c.App.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV must be one of %s, %s, %s", EnvDevelopment, EnvStaging, EnvProduction))
	}
	if c.App.Port < 1 || c.App.Port > 65535 {
		errs = append(errs, errors.New("APP_PORT must be between 1 and 65535"))
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.Database.MaxConns < 1 {
		errs = append(errs, errors.New("DATABASE_MAX_CONNS must be at least 1"))
	}
	if c.Redis.URL == "" {
		errs = append(errs, errors.New("REDIS_URL is required"))
	}
	if c.HTTP.MaxBodyBytes < 1 {
		errs = append(errs, errors.New("HTTP_MAX_BODY_BYTES must be at least 1"))
	}
	if c.Storage.LogDirectory == "" {
		errs = append(errs, errors.New("LOG_DIRECTORY is required"))
	}
	if c.Storage.WorkflowDirectory == "" {
		errs = append(errs, errors.New("WORKFLOW_DIRECTORY is required"))
	}
	if c.Storage.LogRetentionDays < 1 {
		errs = append(errs, errors.New("LOG_RETENTION_DAYS must be at least 1"))
	}

	durations := []struct {
		name  string
		value time.Duration
	}{
		{"APP_SHUTDOWN_TIMEOUT", c.App.ShutdownTimeout},
		{"HTTP_READ_HEADER_TIMEOUT", c.HTTP.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", c.HTTP.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", c.HTTP.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", c.HTTP.IdleTimeout},
		{"DATABASE_CONNECT_TIMEOUT", c.Database.ConnectTimeout},
		{"REDIS_CONNECT_TIMEOUT", c.Redis.ConnectTimeout},
		{"EASYECOM_TIMEOUT", c.EasyEcom.Timeout},
	}
	for _, d := range durations {
		if d.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive duration", d.name))
		}
	}

	return errors.Join(errs...)
}

// reader accumulates parse errors so that every misconfigured variable is
// reported in one go rather than one restart at a time.
type reader struct {
	lookup Lookup
	errs   []error
}

func (r *reader) raw(key string) (string, bool) {
	v, ok := r.lookup(key)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

func (r *reader) str(key, def string) string {
	if v, ok := r.raw(key); ok {
		return v
	}
	return def
}

func (r *reader) int(key string, def int) int {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: expected integer, got %q", key, v))
		return def
	}
	return n
}

func (r *reader) int64(key string, def int64) int64 {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: expected integer, got %q", key, v))
		return def
	}
	return n
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: expected duration such as 5s or 2m, got %q", key, v))
		return def
	}
	return d
}

func (r *reader) err() error {
	return errors.Join(r.errs...)
}
