// Package logging configures the process-wide structured application logger.
//
// This logger is for operational events (startup, HTTP requests, failures).
// Integration execution logs, which are append-only JSONL records of workflow
// actions, live in the integration logger added in a later phase.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New builds a JSON logger writing to w at the given level, tagged with the
// service name so that aggregated logs can be filtered per service.
func New(w io.Writer, level string, service string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler).With(slog.String("service", service)), nil
}

// ParseLevel maps a configuration string to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (expected debug, info, warn or error)", s)
	}
}
