package intlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Hook is extra clean-up run on the retention schedule with the same
// cutoff, for example expiring idempotency records or completed workflow
// state files.
type Hook func(ctx context.Context, cutoff time.Time) error

// Retention removes date directories older than the retention window.
// Only directories whose name is a date are considered, today's directory
// is never touched, and running it repeatedly is harmless.
type Retention struct {
	root   string
	days   int
	logger *slog.Logger
	now    func() time.Time
	hooks  []Hook
}

// AddHook registers extra clean-up to run after the directories are pruned.
func (r *Retention) AddHook(h Hook) {
	if h != nil {
		r.hooks = append(r.hooks, h)
	}
}

// NewRetention keeps the latest days of logs under root.
func NewRetention(root string, days int, logger *slog.Logger) (*Retention, error) {
	if root == "" {
		return nil, errors.New("intlog: retention root is required")
	}
	if days < 1 {
		return nil, errors.New("intlog: retention days must be at least 1")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Retention{root: root, days: days, logger: logger, now: time.Now}, nil
}

// WithClock returns a copy of r using now as its clock; intended for tests.
func (r *Retention) WithClock(now func() time.Time) *Retention {
	c := *r
	c.now = now
	return &c
}

// RunOnce deletes expired directories and returns their names.
func (r *Retention) RunOnce(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(r.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("intlog: list %s: %w", r.root, err)
	}

	today := r.now().UTC().Truncate(24 * time.Hour)
	cutoff := today.AddDate(0, 0, -(r.days - 1)) // keep today plus days-1 earlier days

	var deleted []string
	var errs []error
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if !e.IsDir() {
			continue
		}
		day, err := time.Parse(dayLayout, e.Name())
		if err != nil {
			continue
		}
		if !day.Before(cutoff) {
			continue
		}
		path := filepath.Join(r.root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("intlog: remove %s: %w", path, err))
			continue
		}
		deleted = append(deleted, e.Name())
	}
	sort.Strings(deleted)
	if len(deleted) > 0 {
		r.logger.Info("log retention removed expired directories", slog.Int("count", len(deleted)), slog.Any("days", deleted))
	}
	for _, hook := range r.hooks {
		if ctx.Err() != nil {
			break
		}
		if err := hook(ctx, cutoff); err != nil {
			errs = append(errs, err)
		}
	}
	return deleted, errors.Join(errs...)
}

// Cutoff returns the oldest day kept by a retention of days at now.
func (r *Retention) Cutoff() time.Time {
	return r.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(r.days - 1))
}

// Run executes RunOnce immediately and then every interval until ctx ends.
func (r *Retention) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if _, err := r.RunOnce(ctx); err != nil {
		r.logger.Error("log retention failed", slog.String("error", err.Error()))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				r.logger.Error("log retention failed", slog.String("error", err.Error()))
			}
		}
	}
}
