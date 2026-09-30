package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/config"
)

func lookupFrom(m map[string]string) config.Lookup {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func merge(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestLoadFrom(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL": "postgres://user:secret@localhost:5432/gateway",
		"REDIS_URL":    "redis://localhost:6379/0",
	}

	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "defaults apply when only required values are set",
			env:  base,
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.App.Env != config.EnvDevelopment {
					t.Errorf("env = %q, want development", cfg.App.Env)
				}
				if cfg.App.Port != 8080 {
					t.Errorf("port = %d, want 8080", cfg.App.Port)
				}
				if cfg.Storage.LogRetentionDays != 30 {
					t.Errorf("retention = %d, want 30", cfg.Storage.LogRetentionDays)
				}
				if cfg.HTTP.MaxBodyBytes != 1<<20 {
					t.Errorf("max body = %d, want %d", cfg.HTTP.MaxBodyBytes, 1<<20)
				}
				if cfg.Database.MaxConns != 10 {
					t.Errorf("max conns = %d, want 10", cfg.Database.MaxConns)
				}
				if cfg.App.IsProduction() {
					t.Error("development config reported as production")
				}
				if cfg.EasyEcom.BaseURL != "https://api.easyecom.io" || cfg.EasyEcom.Timeout != 15*time.Second {
					t.Errorf("easyecom defaults = %+v", cfg.EasyEcom)
				}
				if cfg.Queue.Stream != "gluzo:jobs" || cfg.Queue.Group != "gateway-workers" || cfg.Queue.MaxDeliveries != 5 || cfg.Queue.MaxLen != 100_000 {
					t.Errorf("queue defaults = %+v", cfg.Queue)
				}
			},
		},
		{
			name:    "queue max deliveries must be positive",
			env:     merge(base, map[string]string{"QUEUE_MAX_DELIVERIES": "0"}),
			wantErr: "QUEUE_MAX_DELIVERIES must be at least 1",
		},
		{
			name: "worker settings are read",
			env: merge(base, map[string]string{
				"WORKER_ENABLED":     "false",
				"WORKER_CONCURRENCY": "2",
			}),
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.Worker.Enabled || cfg.Worker.Concurrency != 2 || cfg.Worker.MaxAutoResumes != 3 {
					t.Errorf("worker config = %+v", cfg.Worker)
				}
			},
		},
		{
			name: "vinculum settings are read",
			env: merge(base, map[string]string{
				"VINCULUM_API_OWNER":       "gluzo",
				"VINCULUM_API_KEY":         "vk",
				"VINCULUM_LOCATION":        "DEL",
				"VINCULUM_SELLABLE_BUCKET": "Good",
				"VINCULUM_TIMEOUT":         "30s",
			}),
			check: func(t *testing.T, cfg *config.Config) {
				v := cfg.Vinculum
				if v.BaseURL != "https://erp.vineretail.com" || v.APIOwner != "gluzo" || v.APIKey != "vk" || v.Location != "DEL" || v.SellableBucket != "Good" || v.Timeout != 30*time.Second {
					t.Errorf("vinculum config = %+v", v)
				}
			},
		},
		{
			name:    "vinculum timeout must be positive",
			env:     merge(base, map[string]string{"VINCULUM_TIMEOUT": "0s"}),
			wantErr: "VINCULUM_TIMEOUT must be a positive duration",
		},
		{
			name:    "worker enabled must be boolean",
			env:     merge(base, map[string]string{"WORKER_ENABLED": "maybe"}),
			wantErr: "WORKER_ENABLED: expected true or false",
		},
		{
			name:    "worker concurrency must be positive",
			env:     merge(base, map[string]string{"WORKER_CONCURRENCY": "0"}),
			wantErr: "WORKER_CONCURRENCY must be at least 1",
		},
		{
			name: "easyecom credentials are read",
			env: merge(base, map[string]string{
				"EASYECOM_API_KEY":      "key",
				"EASYECOM_EMAIL":        "ops@example.com",
				"EASYECOM_PASSWORD":     "pw",
				"EASYECOM_LOCATION_KEY": "loc",
				"EASYECOM_TIMEOUT":      "3s",
			}),
			check: func(t *testing.T, cfg *config.Config) {
				e := cfg.EasyEcom
				if e.APIKey != "key" || e.Email != "ops@example.com" || e.Password != "pw" || e.LocationKey != "loc" || e.Timeout != 3*time.Second {
					t.Errorf("easyecom config = %+v", e)
				}
			},
		},
		{
			name:    "easyecom timeout must be positive",
			env:     merge(base, map[string]string{"EASYECOM_TIMEOUT": "0s"}),
			wantErr: "EASYECOM_TIMEOUT must be a positive duration",
		},
		{
			name: "explicit values override defaults",
			env: merge(base, map[string]string{
				"APP_ENV":            "production",
				"APP_PORT":           "9090",
				"LOG_RETENTION_DAYS": "7",
				"HTTP_READ_TIMEOUT":  "2s",
				"DATABASE_MAX_CONNS": "25",
			}),
			check: func(t *testing.T, cfg *config.Config) {
				if !cfg.App.IsProduction() {
					t.Error("expected production")
				}
				if cfg.App.Port != 9090 {
					t.Errorf("port = %d, want 9090", cfg.App.Port)
				}
				if cfg.Storage.LogRetentionDays != 7 {
					t.Errorf("retention = %d, want 7", cfg.Storage.LogRetentionDays)
				}
				if cfg.HTTP.ReadTimeout != 2*time.Second {
					t.Errorf("read timeout = %s, want 2s", cfg.HTTP.ReadTimeout)
				}
				if cfg.Database.MaxConns != 25 {
					t.Errorf("max conns = %d, want 25", cfg.Database.MaxConns)
				}
			},
		},
		{
			name: "blank values fall back to defaults",
			env:  merge(base, map[string]string{"APP_PORT": "   ", "LOG_LEVEL": ""}),
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.App.Port != 8080 {
					t.Errorf("port = %d, want 8080", cfg.App.Port)
				}
				if cfg.App.LogLevel != "info" {
					t.Errorf("log level = %q, want info", cfg.App.LogLevel)
				}
			},
		},
		{
			name:    "missing database url",
			env:     map[string]string{"REDIS_URL": "redis://localhost:6379/0"},
			wantErr: "DATABASE_URL is required",
		},
		{
			name:    "missing redis url",
			env:     map[string]string{"DATABASE_URL": "postgres://localhost/db"},
			wantErr: "REDIS_URL is required",
		},
		{
			name:    "port out of range",
			env:     merge(base, map[string]string{"APP_PORT": "70000"}),
			wantErr: "APP_PORT must be between 1 and 65535",
		},
		{
			name:    "non-numeric port",
			env:     merge(base, map[string]string{"APP_PORT": "eighty"}),
			wantErr: "APP_PORT: expected integer",
		},
		{
			name:    "invalid duration",
			env:     merge(base, map[string]string{"HTTP_READ_TIMEOUT": "soon"}),
			wantErr: "HTTP_READ_TIMEOUT: expected duration",
		},
		{
			name:    "unknown environment name",
			env:     merge(base, map[string]string{"APP_ENV": "qa"}),
			wantErr: "APP_ENV must be one of",
		},
		{
			name:    "zero retention",
			env:     merge(base, map[string]string{"LOG_RETENTION_DAYS": "0"}),
			wantErr: "LOG_RETENTION_DAYS must be at least 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(lookupFrom(tt.env))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestLoadFromReportsAllErrorsTogether(t *testing.T) {
	env := map[string]string{
		"APP_PORT":          "abc",
		"HTTP_READ_TIMEOUT": "later",
	}
	_, err := config.LoadFrom(lookupFrom(env))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_PORT: expected integer", "HTTP_READ_TIMEOUT: expected duration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestLoadFromDoesNotLeakSecretsInErrors(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://user:hunter2@localhost:5432/gateway",
		"REDIS_URL":    "redis://:topsecret@localhost:6379/0",
		"APP_PORT":     "bad",
	}
	_, err := config.LoadFrom(lookupFrom(env))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"hunter2", "topsecret"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error message leaked secret %q: %s", secret, err)
		}
	}
}
