package intlog_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/intlog"
)

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("line is not JSON: %v: %s", err, scanner.Text())
		}
		out = append(out, m)
	}
	return out
}

func TestFileRecorderWritesDatePartitionedJSONL(t *testing.T) {
	root := t.TempDir()
	day1 := time.Date(2026, 9, 12, 23, 30, 0, 0, time.UTC)
	rec, err := intlog.NewFileRecorder(root, intlog.WithClock(func() time.Time { return day1 }))
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	ctx := context.Background()

	rec.Record(ctx, intlog.Entry{Timestamp: day1, CorrelationID: "INT-1", Workflow: "ORDER_SYNC", Integration: "dabur", Action: "FETCH_ORDER", Status: intlog.StatusSuccess, Attempt: 1, DurationMS: 420})
	rec.Record(ctx, intlog.Entry{Timestamp: day1.Add(time.Minute), CorrelationID: "INT-1", Action: "FETCH_INVENTORY", Status: intlog.StatusFailed, Attempt: 1,
		Error: &apperror.Info{Category: apperror.Timeout, Message: "timeout", Retryable: true}})
	rec.Record(ctx, intlog.Entry{Timestamp: day1.Add(2 * time.Minute), CorrelationID: "INT-1", Action: "FETCH_INVENTORY", Status: intlog.StatusSuccess, Attempt: 2})
	// An entry stamped in IST that falls on the next UTC day lands in that day's directory.
	ist := time.FixedZone("IST", 19800)
	rec.Record(ctx, intlog.Entry{Timestamp: time.Date(2026, 9, 13, 5, 45, 0, 0, ist), CorrelationID: "INT-2", Action: "WEBHOOK_RECEIVED", Status: intlog.StatusSuccess})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	all := readLines(t, filepath.Join(root, "2026-09-12", intlog.IntegrationFile))
	if len(all) != 3 {
		t.Fatalf("integration.jsonl has %d lines, want 3", len(all))
	}
	if all[0]["action"] != "FETCH_ORDER" || all[0]["status"] != "SUCCESS" || all[0]["duration_ms"] != float64(420) || all[0]["timestamp"] != "2026-09-12T23:30:00Z" {
		t.Fatalf("first line = %v", all[0])
	}
	// Both the failed and the successful attempt are kept: the log is append-only.
	if all[1]["status"] != "FAILED" || all[1]["attempt"] != float64(1) || all[2]["status"] != "SUCCESS" || all[2]["attempt"] != float64(2) {
		t.Fatalf("attempt history = %v / %v", all[1], all[2])
	}
	errs := readLines(t, filepath.Join(root, "2026-09-12", intlog.ErrorFile))
	if len(errs) != 1 || errs[0]["action"] != "FETCH_INVENTORY" {
		t.Fatalf("error.jsonl = %v", errs)
	}
	next := readLines(t, filepath.Join(root, "2026-09-13", intlog.IntegrationFile))
	if len(next) != 1 || next[0]["correlation_id"] != "INT-2" || next[0]["timestamp"] != "2026-09-13T00:15:00Z" {
		t.Fatalf("next-day partition = %v", next)
	}
}

func TestFileRecorderSanitisesBeforeWriting(t *testing.T) {
	root := t.TempDir()
	rec, _ := intlog.NewFileRecorder(root)
	rec.Record(context.Background(), intlog.Entry{
		CorrelationID: "INT-3", Action: "FETCH_ORDER", Status: intlog.StatusFailed,
		Request: map[string]any{"headers": map[string]any{"Authorization": "Bearer topsecret", "X-API-Key": "key123"}},
		Details: map[string]any{"email": "asha@example.com"},
		Error:   &apperror.Info{Message: "rejected", Detail: "Get \"https://x/oauth/token?password=hunter2\": refused"},
	})
	_ = rec.Close()

	data, err := os.ReadFile(filepath.Join(intlog.DayDir(root, time.Now()), intlog.IntegrationFile))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, secret := range []string{"topsecret", "key123", "hunter2", "asha@"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("secret %q written to log: %s", secret, data)
		}
	}
	if !strings.Contains(string(data), intlog.Redacted) {
		t.Fatalf("no redaction marker in %s", data)
	}
}

func TestFileRecorderAppendsAcrossInstances(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		rec, _ := intlog.NewFileRecorder(root, intlog.WithClock(func() time.Time { return now }))
		rec.Record(context.Background(), intlog.Entry{CorrelationID: "INT-4", Action: "A", Status: intlog.StatusSuccess})
		_ = rec.Close()
	}
	if lines := readLines(t, filepath.Join(root, "2026-09-12", intlog.IntegrationFile)); len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (restart must append, not truncate)", len(lines))
	}
}

func TestFileRecorderIgnoresWritesAfterClose(t *testing.T) {
	root := t.TempDir()
	rec, _ := intlog.NewFileRecorder(root)
	_ = rec.Close()
	rec.Record(context.Background(), intlog.Entry{CorrelationID: "INT-5", Action: "A", Status: intlog.StatusSuccess})
	if _, err := os.Stat(intlog.DayDir(root, time.Now())); !os.IsNotExist(err) {
		t.Fatalf("closed recorder still wrote: %v", err)
	}
}

func TestRetentionDeletesOnlyExpiredDayDirectories(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"2026-08-01", "2026-08-13", "2026-08-14", "2026-09-11", "2026-09-12", "notes", "2026-13-99"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		_ = os.WriteFile(filepath.Join(root, name, "integration.jsonl"), []byte("{}\n"), 0o640)
	}
	_ = os.WriteFile(filepath.Join(root, "2026-07-01"), []byte("a file, not a directory"), 0o640)

	ret, err := intlog.NewRetention(root, 30, nil)
	if err != nil {
		t.Fatalf("NewRetention: %v", err)
	}
	if _, err := intlog.NewRetention(root, 0, nil); err == nil {
		t.Fatal("zero retention accepted")
	}

	// Use a fixed clock via a wrapper: RunOnce reads the injected time.
	deleted, err := retentionAt(ret, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if strings.Join(deleted, ",") != "2026-08-01,2026-08-13" {
		t.Fatalf("deleted = %v (30 days keeps 2026-08-14 through 2026-09-12)", deleted)
	}
	for _, kept := range []string{"2026-08-14", "2026-09-11", "2026-09-12", "notes", "2026-13-99"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "2026-07-01")); err != nil {
		t.Error("plain files must never be deleted")
	}

	again, err := retentionAt(ret, now).RunOnce(context.Background())
	if err != nil || len(again) != 0 {
		t.Fatalf("second run should be a no-op: %v %v", again, err)
	}
	if _, err := ret.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce with the real clock: %v", err)
	}
}
