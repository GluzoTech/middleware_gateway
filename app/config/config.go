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
	Queue    Queue
	Worker   Worker
	Admin    Admin
	EasyEcom EasyEcom
}

// Admin protects the operator endpoints. An empty token leaves them
// unmounted.
type Admin struct {
	LogViewerToken string
}

// Worker tunes the in-process job worker. Enabled=false runs an API-only
// instance that accepts webhooks but executes nothing.
type Worker struct {
	Enabled           bool
	Concurrency       int
	MaxAutoResumes    int
	RecoveryInterval  time.Duration
	StaleRunningAfter time.Duration
	RetryFailedAfter  time.Duration
}

// Queue tunes the Redis Streams job queue.
type Queue struct {
	Stream        string
	Group         string
	BatchSize     int
	BlockTimeout  time.Duration
	ClaimMinIdle  time.Duration
	MaxDeliveries int
	MaxLen        int64
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
	LogDirectory         string
	WorkflowDirectory    string
	LogRetentionDays     int
	LogRetentionInterval time.Duration
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
			LogDirectory:         r.str("LOG_DIRECTORY", "./storage/logs"),
			WorkflowDirectory:    r.str("WORKFLOW_DIRECTORY", "./storage/workflows"),
			LogRetentionDays:     r.int("LOG_RETENTION_DAYS", 30),
			LogRetentionInterval: r.duration("LOG_RETENTION_INTERVAL", time.Hour),
		},
		Admin: Admin{
			LogViewerToken: r.str("ADMIN_LOG_VIEWER_TOKEN", ""),
		},
		Queue: Queue{
			Stream:        r.str("QUEUE_STREAM", "gluzo:jobs"),
			Group:         r.str("QUEUE_GROUP", "gateway-workers"),
			BatchSize:     r.int("QUEUE_BATCH_SIZE", 10),
			BlockTimeout:  r.duration("QUEUE_BLOCK_TIMEOUT", 5*time.Second),
			ClaimMinIdle:  r.duration("QUEUE_CLAIM_MIN_IDLE", 60*time.Second),
			MaxDeliveries: r.int("QUEUE_MAX_DELIVERIES", 5),
			MaxLen:        r.int64("QUEUE_MAX_LEN", 100_000),
		},
		Worker: Worker{
			Enabled:           r.bool("WORKER_ENABLED", true),
			Concurrency:       r.int("WORKER_CONCURRENCY", 4),
			MaxAutoResumes:    r.int("WORKER_MAX_AUTO_RESUMES", 3),
			RecoveryInterval:  r.duration("WORKER_RECOVERY_INTERVAL", 5*time.Minute),
			StaleRunningAfter: r.duration("WORKER_STALE_RUNNING_AFTER", 10*time.Minute),
			RetryFailedAfter:  r.duration("WORKER_RETRY_FAILED_AFTER", time.Minute),
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
	if c.Queue.Stream == "" || c.Queue.Group == "" {
		errs = append(errs, errors.New("QUEUE_STREAM and QUEUE_GROUP are required"))
	}
	if c.Queue.BatchSize < 1 {
		errs = append(errs, errors.New("QUEUE_BATCH_SIZE must be at least 1"))
	}
	if c.Queue.MaxDeliveries < 1 {
		errs = append(errs, errors.New("QUEUE_MAX_DELIVERIES must be at least 1"))
	}
	if c.Queue.MaxLen < 1 {
		errs = append(errs, errors.New("QUEUE_MAX_LEN must be at least 1"))
	}
	if c.Worker.Concurrency < 1 {
		errs = append(errs, errors.New("WORKER_CONCURRENCY must be at least 1"))
	}
	if c.Worker.MaxAutoResumes < 0 {
		errs = append(errs, errors.New("WORKER_MAX_AUTO_RESUMES must not be negative"))
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
		{"QUEUE_BLOCK_TIMEOUT", c.Queue.BlockTimeout},
		{"QUEUE_CLAIM_MIN_IDLE", c.Queue.ClaimMinIdle},
		{"LOG_RETENTION_INTERVAL", c.Storage.LogRetentionInterval},
		{"WORKER_RECOVERY_INTERVAL", c.Worker.RecoveryInterval},
		{"WORKER_STALE_RUNNING_AFTER", c.Worker.StaleRunningAfter},
		{"WORKER_RETRY_FAILED_AFTER", c.Worker.RetryFailedAfter},
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

func (r *reader) bool(key string, def bool) bool {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: expected true or false, got %q", key, v))
		return def
	}
	return b
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
