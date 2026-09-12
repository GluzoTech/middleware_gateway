// Command server runs the Gluzo Integration Gateway HTTP service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gluzo/integration-gateway/app/config"
	"github.com/gluzo/integration-gateway/app/database/migrations"
	"github.com/gluzo/integration-gateway/app/database/postgres"
	"github.com/gluzo/integration-gateway/app/database/redisconn"
	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpserver"
	"github.com/gluzo/integration-gateway/app/logging"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const readinessTimeout = 2 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger, err := logging.New(os.Stdout, cfg.App.LogLevel, config.ServiceName)
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	logger = logger.With(slog.String("env", cfg.App.Env), slog.String("version", version))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for _, dir := range []string{cfg.Storage.LogDirectory, cfg.Storage.WorkflowDirectory} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create storage directory %s: %w", dir, err)
		}
	}

	pool, err := postgres.Connect(ctx, postgres.Config{
		URL:            cfg.Database.URL,
		MaxConns:       cfg.Database.MaxConns,
		ConnectTimeout: cfg.Database.ConnectTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("postgres connected")

	if err := migrations.Apply(ctx, pool, migrations.Files(), logger); err != nil {
		return err
	}

	rdb, err := redisconn.Connect(ctx, cfg.Redis.URL, cfg.Redis.ConnectTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()
	logger.Info("redis connected")

	healthHandler := health.NewHandler(config.ServiceName, version, readinessTimeout, logger,
		postgres.Checker{Pool: pool},
		redisconn.Checker{Client: rdb},
	)

	router := httpserver.NewRouter(httpserver.Dependencies{
		Logger:       logger,
		Health:       healthHandler,
		MaxBodyBytes: cfg.HTTP.MaxBodyBytes,
	})
	srv := httpserver.New(cfg.App.Port, cfg.HTTP, router)

	logger.Info("starting gateway", slog.Int("port", cfg.App.Port))
	return httpserver.Run(ctx, srv, cfg.App.ShutdownTimeout, logger)
}
