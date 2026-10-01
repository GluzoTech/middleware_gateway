package intlog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/intlog"
)

func seedLogs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	write := func(ts time.Time, e intlog.Entry) {
		rec, err := intlog.NewFileRecorder(root, intlog.WithClock(func() time.Time { return ts }))
		if err != nil {
			t.Fatalf("NewFileRecorder: %v", err)
		}
		e.Timestamp = ts
		rec.Record(ctx, e)
		_ = rec.Close()
	}
	d1 := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	write(d1, intlog.Entry{CorrelationID: "INT-A", Platform: "easyecom", ExternalOrderID: "100", Action: "WEBHOOK_RECEIVED", Status: intlog.StatusSuccess})
	write(d1.Add(time.Minute), intlog.Entry{CorrelationID: "INT-A", Workflow: "ORDER_SYNC", Integration: "vinculum", ExternalOrderID: "100", Action: "FETCH_ORDER", Status: intlog.StatusSuccess, Attempt: 1})
	write(d2, intlog.Entry{CorrelationID: "INT-A", Workflow: "ORDER_SYNC", Integration: "vinculum", ExternalOrderID: "100", OrderID: "SO-100", Action: "UPDATE_DESTINATION_ORDER", Status: intlog.StatusFailed, Attempt: 1, Error: &apperror.Info{Category: apperror.ExternalAPI, Message: "503"}})
	write(d2.Add(time.Minute), intlog.Entry{CorrelationID: "INT-A", Workflow: "ORDER_SYNC", Integration: "vinculum", ExternalOrderID: "100", OrderID: "SO-100", Action: "UPDATE_DESTINATION_ORDER", Status: intlog.StatusSuccess, Attempt: 2})
	write(d2.Add(2*time.Minute), intlog.Entry{CorrelationID: "INT-B", Platform: "easyecom", ExternalOrderID: "200", Action: "WEBHOOK_RECEIVED", Status: intlog.StatusSuccess})
	// A corrupt line must not break searches.
	_ = os.WriteFile(filepath.Join(root, "2026-09-11", "integration.jsonl"), []byte("not json\n"), 0o640)
	_ = os.MkdirAll(filepath.Join(root, "2026-09-11"), 0o750)
	_ = os.WriteFile(filepath.Join(root, "2026-09-11", "integration.jsonl"), []byte("not json\n{\"correlation_id\":\"INT-C\",\"action\":\"X\",\"status\":\"SUCCESS\",\"timestamp\":\"2026-09-11T00:00:00Z\"}\n"), 0o640)
	return root
}

func TestReaderTimeline(t *testing.T) {
	r := intlog.NewReader(seedLogs(t))
	entries, err := r.Timeline(context.Background(), "INT-A")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 across two days", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
			t.Fatal("timeline is not chronological")
		}
	}
	if entries[0].Action != "WEBHOOK_RECEIVED" || entries[2].Status != intlog.StatusFailed || entries[3].Attempt != 2 {
		t.Fatalf("unexpected timeline: %+v", entries)
	}
	if _, err := r.Timeline(context.Background(), " "); err == nil {
		t.Fatal("blank correlation id accepted")
	}
}

func TestReaderSearch(t *testing.T) {
	root := seedLogs(t)
	r := intlog.NewReader(root)
	ctx := context.Background()

	tests := []struct {
		name  string
		query intlog.Query
		want  int
		first string // action of the newest entry
	}{
		{"by external order id", intlog.Query{ExternalOrderID: "100", From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}, 4, "UPDATE_DESTINATION_ORDER"},
		{"by destination order id", intlog.Query{OrderID: "so-100", From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, 2, "UPDATE_DESTINATION_ORDER"},
		{"errors only", intlog.Query{ErrorsOnly: true, From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, 1, "UPDATE_DESTINATION_ORDER"},
		{"by action and status", intlog.Query{Action: "update_destination_order", Status: "SUCCESS", From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, 1, "UPDATE_DESTINATION_ORDER"},
		{"by platform", intlog.Query{Platform: "easyecom", From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, 2, "WEBHOOK_RECEIVED"},
		{"date window excludes older day", intlog.Query{From: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}, 3, "WEBHOOK_RECEIVED"},
		{"correlation id scans all days", intlog.Query{CorrelationID: "INT-A"}, 4, "UPDATE_DESTINATION_ORDER"},
		{"limit", intlog.Query{CorrelationID: "INT-A", Limit: 2}, 2, "UPDATE_DESTINATION_ORDER"},
		{"corrupt line skipped", intlog.Query{CorrelationID: "INT-C"}, 1, "X"},
		{"no match", intlog.Query{Integration: "shopify", From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Search(ctx, tt.query)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("got %d entries, want %d: %+v", len(got), tt.want, got)
			}
			if tt.want > 0 && got[0].Action != tt.first {
				t.Fatalf("newest entry = %s, want %s", got[0].Action, tt.first)
			}
		})
	}

	days, err := r.Days()
	if err != nil || len(days) != 3 || days[0] != "2026-09-12" {
		t.Fatalf("Days = %v, %v", days, err)
	}
	empty := intlog.NewReader(filepath.Join(root, "missing"))
	if got, err := empty.Search(ctx, intlog.Query{CorrelationID: "x"}); err != nil || len(got) != 0 {
		t.Fatalf("missing root: %v %v", got, err)
	}
}
