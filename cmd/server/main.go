// Command server runs the Gluzo Integration Gateway: the webhook intake API,
// the protected admin log viewer and, unless WORKER_ENABLED=false, the job
// worker that executes workflows.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gluzo/integration-gateway/app/admin"
	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/config"
	"github.com/gluzo/integration-gateway/app/database/migrations"
	"github.com/gluzo/integration-gateway/app/database/postgres"
	"github.com/gluzo/integration-gateway/app/database/redisconn"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpserver"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/logging"
	"github.com/gluzo/integration-gateway/app/queue/redisqueue"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/webhook"
	"github.com/gluzo/integration-gateway/app/worker"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/ordersync"
	"github.com/gluzo/integration-gateway/app/workflowstate"
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

	jobQueue, err := redisqueue.New(rdb, redisqueue.Options{
		Stream:        cfg.Queue.Stream,
		Group:         cfg.Queue.Group,
		BatchSize:     int64(cfg.Queue.BatchSize),
		BlockTimeout:  cfg.Queue.BlockTimeout,
		ClaimMinIdle:  cfg.Queue.ClaimMinIdle,
		MaxDeliveries: cfg.Queue.MaxDeliveries,
		MaxLen:        cfg.Queue.MaxLen,
	}, logger)
	if err != nil {
		return err
	}

	// Append-only, date-partitioned integration log shared by intake and
	// the workflow engine.
	recorder, err := intlog.NewFileRecorder(cfg.Storage.LogDirectory, intlog.WithLogger(logger))
	if err != nil {
		return err
	}
	defer func() { _ = recorder.Close() }()

	credentials := auth.NewStore(pool)
	idempotencyStore := idempotency.NewPostgresStore(pool)
	easyecomWebhook := webhook.NewHandler(easyecom.WebhookParser{}, idempotencyStore, jobQueue, logger, webhook.WithRecorder(recorder))

	stateRepo, err := workflowstate.NewFileRepository(cfg.Storage.WorkflowDirectory)
	if err != nil {
		return err
	}
	executor := workflow.NewExecutor(stateRepo, workflow.WithRecorder(recorder), workflow.WithLogger(logger))
	registry := workflow.NewRegistry()

	var jobWorker *worker.Worker
	if cfg.Worker.Enabled {
		jobWorker, err = buildWorker(cfg, pool, jobQueue, registry, executor, stateRepo, idempotencyStore, recorder, logger)
		if err != nil {
			return err
		}
	} else {
		logger.Warn("worker disabled: this instance accepts webhooks but executes no workflows")
	}

	// Retention prunes expired log days, idempotency records and completed
	// workflow state on one schedule with one cutoff.
	retention, err := intlog.NewRetention(cfg.Storage.LogDirectory, cfg.Storage.LogRetentionDays, logger)
	if err != nil {
		return err
	}
	retention.AddHook(func(ctx context.Context, cutoff time.Time) error {
		n, err := idempotencyStore.DeleteOlderThan(ctx, cutoff)
		if n > 0 {
			logger.Info("retention removed idempotency records", slog.Int64("count", n))
		}
		return err
	})
	retention.AddHook(func(_ context.Context, cutoff time.Time) error {
		n, err := stateRepo.DeleteCompletedBefore(cutoff)
		if n > 0 {
			logger.Info("retention removed completed workflow states", slog.Int("count", n))
		}
		return err
	})

	var adminHandler *admin.Handler
	if cfg.Admin.LogViewerToken != "" {
		var resumer admin.Resumer
		if jobWorker != nil {
			resumer = jobWorker
		}
		adminHandler, err = admin.NewHandler(intlog.NewReader(cfg.Storage.LogDirectory), stateRepo, resumer, logger)
		if err != nil {
			return err
		}
	} else {
		logger.Warn("ADMIN_LOG_VIEWER_TOKEN is empty: the admin log viewer is disabled")
	}

	healthHandler := health.NewHandler(config.ServiceName, version, readinessTimeout, logger,
		postgres.Checker{Pool: pool},
		redisconn.Checker{Client: rdb},
	)
	router := httpserver.NewRouter(httpserver.Dependencies{
		Logger:       logger,
		Health:       healthHandler,
		MaxBodyBytes: cfg.HTTP.MaxBodyBytes,
		PlatformKeys: credentials,
		AccessTokens: credentials,
		Webhooks:     []*webhook.Handler{easyecomWebhook},
		Admin:        adminHandler,
		AdminToken:   cfg.Admin.LogViewerToken,
	})
	srv := httpserver.New(cfg.App.Port, cfg.HTTP, router)

	// The HTTP server, the worker and the retention job share the signal
	// context: the first failure cancels the others, and a signal drains all.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting gateway", slog.Int("port", cfg.App.Port), slog.Bool("worker", jobWorker != nil), slog.Bool("admin", adminHandler != nil))
		if err := httpserver.Run(ctx, srv, cfg.App.ShutdownTimeout, logger); err != nil {
			errs <- err
			stop()
		}
	}()
	if jobWorker != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := jobWorker.Run(ctx); err != nil {
				errs <- err
				stop()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		retention.Run(ctx, cfg.Storage.LogRetentionInterval)
	}()
	wg.Wait()
	close(errs)

	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	logger.Info("gateway stopped")
	return errors.Join(failures...)
}

// buildWorker assembles the adapters, the ORDER_SYNC workflow and the worker.
// Outbound credentials are validated here, so an instance that only receives
// webhooks (WORKER_ENABLED=false) can run without them.
func buildWorker(
	cfg *config.Config,
	pool *pgxpool.Pool,
	jobQueue *redisqueue.Queue,
	registry *workflow.Registry,
	executor *workflow.Executor,
	states workflow.Repository,
	idempotencyStore idempotency.Store,
	recorder intlog.Recorder,
	logger *slog.Logger,
) (*worker.Worker, error) {
	easyecomClient, err := easyecom.NewClient(easyecom.Config{
		BaseURL:     cfg.EasyEcom.BaseURL,
		APIKey:      cfg.EasyEcom.APIKey,
		JWTToken:    cfg.EasyEcom.JWTToken,
		Email:       cfg.EasyEcom.Email,
		Password:    cfg.EasyEcom.Password,
		LocationKey: cfg.EasyEcom.LocationKey,
		Timeout:     cfg.EasyEcom.Timeout,
	}, easyecom.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("configure EasyEcom client (set WORKER_ENABLED=false for an intake-only instance): %w", err)
	}
	// Fulfilment vendors. Each adapter declares its capabilities by the
	// roles it implements; the registry discovers them. Adding a partner is
	// one MustRegister call and a route row.
	//
	// The previous vendor pipeline is gone and Vinculum is not yet written, so this is
	// deliberately empty. An instance in this state accepts webhooks but has
	// nothing to submit orders to.
	vendors := vendor.NewRegistry()

	if len(vendors.Platforms()) == 0 {
		logger.Warn("no fulfilment vendor is registered: ORDER_SYNC is not available; run with WORKER_ENABLED=false until a vendor adapter is wired")
		return worker.New(worker.Dependencies{
			Queue:       jobQueue,
			Registry:    registry,
			Executor:    executor,
			States:      states,
			Idempotency: idempotencyStore,
			Recorder:    recorder,
			Logger:      logger,
		}, workerConfig(cfg))
	}

	orderSync, err := ordersync.New(ordersync.Dependencies{
		Resolver: routing.NewStore(pool),
		Origins:  map[string]vendor.Origin{easyecom.PlatformName: easyecom.NewSource(easyecomClient, logger)},
		Vendors:  vendors,
		Logger:   logger,
	})
	if err != nil {
		return nil, err
	}
	if err := registry.Register(orderSync, event.OrderCreated, event.OrderConfirmed); err != nil {
		return nil, err
	}

	return worker.New(worker.Dependencies{
		Queue:       jobQueue,
		Registry:    registry,
		Executor:    executor,
		States:      states,
		Idempotency: idempotencyStore,
		Recorder:    recorder,
		Logger:      logger,
	}, workerConfig(cfg))
}

func workerConfig(cfg *config.Config) worker.Config {
	return worker.Config{
		Concurrency:       cfg.Worker.Concurrency,
		MaxAutoResumes:    cfg.Worker.MaxAutoResumes,
		RecoveryInterval:  cfg.Worker.RecoveryInterval,
		StaleRunningAfter: cfg.Worker.StaleRunningAfter,
		RetryFailedAfter:  cfg.Worker.RetryFailedAfter,
	}
}
