package intlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// File names inside each date directory.
const (
	IntegrationFile = "integration.jsonl"
	ErrorFile       = "error.jsonl"
	dayLayout       = "2006-01-02"
)

// FileRecorder appends entries to date-partitioned JSONL files:
//
//	<root>/2026-09-12/integration.jsonl   every entry
//	<root>/2026-09-12/error.jsonl         failed entries only
//
// Files are opened in append mode and each entry is one write, so entries
// are never modified after being written. Days are partitioned in UTC so
// retention and search do not depend on the host time zone. Write failures
// are logged and never block processing.
type FileRecorder struct {
	root      string
	sanitizer *Sanitizer
	logger    *slog.Logger
	now       func() time.Time

	mu      sync.Mutex
	day     string
	all     *os.File
	errOnly *os.File
	closed  bool
}

// FileRecorderOption configures a FileRecorder.
type FileRecorderOption func(*FileRecorder)

// WithSanitizer replaces the default sanitiser.
func WithSanitizer(s *Sanitizer) FileRecorderOption {
	return func(r *FileRecorder) {
		if s != nil {
			r.sanitizer = s
		}
	}
}

// WithLogger sets where write failures are reported.
func WithLogger(l *slog.Logger) FileRecorderOption {
	return func(r *FileRecorder) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithClock replaces the clock; intended for tests.
func WithClock(now func() time.Time) FileRecorderOption {
	return func(r *FileRecorder) { r.now = now }
}

// NewFileRecorder prepares root.
func NewFileRecorder(root string, opts ...FileRecorderOption) (*FileRecorder, error) {
	if root == "" {
		return nil, errors.New("intlog: root directory is required")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("intlog: create %s: %w", root, err)
	}
	r := &FileRecorder{root: root, sanitizer: NewSanitizer(), logger: slog.Default(), now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Record implements Recorder.
func (r *FileRecorder) Record(_ context.Context, e Entry) {
	if e.Timestamp.IsZero() {
		e.Timestamp = r.now()
	}
	e.Timestamp = e.Timestamp.UTC()
	e = r.sanitizer.Entry(e)

	line, err := json.Marshal(e)
	if err != nil {
		r.logger.Error("intlog: encode entry", slog.String("error", err.Error()), slog.String("correlation_id", e.CorrelationID))
		return
	}
	line = append(line, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if err := r.rotate(e.Timestamp.Format(dayLayout)); err != nil {
		r.logger.Error("intlog: open log files", slog.String("error", err.Error()))
		return
	}
	if _, err := r.all.Write(line); err != nil {
		r.logger.Error("intlog: write entry", slog.String("error", err.Error()), slog.String("correlation_id", e.CorrelationID))
	}
	if e.Status == StatusFailed || e.Error != nil {
		if _, err := r.errOnly.Write(line); err != nil {
			r.logger.Error("intlog: write error entry", slog.String("error", err.Error()), slog.String("correlation_id", e.CorrelationID))
		}
	}
}

// rotate opens the files for day if they are not the current ones.
func (r *FileRecorder) rotate(day string) error {
	if r.day == day && r.all != nil && r.errOnly != nil {
		return nil
	}
	r.closeFiles()

	dir := filepath.Join(r.root, day)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	all, err := os.OpenFile(filepath.Join(dir, IntegrationFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	errOnly, err := os.OpenFile(filepath.Join(dir, ErrorFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		_ = all.Close()
		return err
	}
	r.day, r.all, r.errOnly = day, all, errOnly
	return nil
}

func (r *FileRecorder) closeFiles() {
	if r.all != nil {
		_ = r.all.Close()
		r.all = nil
	}
	if r.errOnly != nil {
		_ = r.errOnly.Close()
		r.errOnly = nil
	}
	r.day = ""
}

// Close flushes and closes the current files.
func (r *FileRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	var errs []error
	for _, f := range []*os.File{r.all, r.errOnly} {
		if f == nil {
			continue
		}
		if err := f.Sync(); err != nil {
			errs = append(errs, err)
		}
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	r.all, r.errOnly, r.day = nil, nil, ""
	return errors.Join(errs...)
}

// DayDir returns the directory holding entries for t.
func DayDir(root string, t time.Time) string {
	return filepath.Join(root, t.UTC().Format(dayLayout))
}
