package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/gluzo/integration-gateway/app/logging"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{" warn ", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"verbose", slog.LevelInfo, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := logging.ParseLevel(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("level = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewWritesJSONWithServiceName(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(&buf, "info", "test-service")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Debug("hidden")
	logger.Info("hello", "key", "value")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not a single JSON document: %v\n%s", err, buf.String())
	}
	if entry["msg"] != "hello" || entry["service"] != "test-service" || entry["key"] != "value" {
		t.Fatalf("unexpected entry: %v", entry)
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	if _, err := logging.New(&bytes.Buffer{}, "loud", "svc"); err == nil {
		t.Fatal("expected error for unknown level")
	}
}
