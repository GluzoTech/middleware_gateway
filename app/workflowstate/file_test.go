package workflowstate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

func sampleState(id string) *workflow.State {
	st := workflow.NewState("ORDER_SYNC", event.Event{
		Platform: "easyecom", EventType: event.OrderCreated, CorrelationID: id, ExternalOrderID: "1001",
		RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "k", Payload: []byte(`{"order_id":1001}`),
	}, "job-1", time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC))
	st.Order = &order.Order{ExternalID: "1001", Items: []order.Item{{SKU: "A", Quantity: 1}}}
	st.SetResult("destination_order_id", "SO-1")
	st.SetPayload("vendor_request", []byte(`{"saleOrder":{"code":"1001"}}`))
	return st
}

func TestFileRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo, err := workflowstate.NewFileRepository(root)
	if err != nil {
		t.Fatalf("NewFileRepository: %v", err)
	}

	st := sampleState("INT-1")
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := repo.Get(ctx, "INT-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CorrelationID != "INT-1" || got.WorkflowName != "ORDER_SYNC" || got.Order == nil || got.Order.ExternalID != "1001" || got.Result("destination_order_id") != "SO-1" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, got.Payload("vendor_request")); err != nil || compact.String() != `{"saleOrder":{"code":"1001"}}` {
		t.Fatalf("payload lost: %s (%v)", got.Payload("vendor_request"), err)
	}
	var eventPayload bytes.Buffer
	if err := json.Compact(&eventPayload, got.Event.Payload); err != nil || got.Event.ExternalOrderID != "1001" || eventPayload.String() != `{"order_id":1001}` {
		t.Fatalf("event lost: %+v (%v)", got.Event, err)
	}

	if _, err := os.Stat(filepath.Join(root, workflowstate.ActiveDir, "INT-1.json")); err != nil {
		t.Fatalf("active file missing: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, workflowstate.ActiveDir))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestFileRepositoryMovesFinalStatesToCompleted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo, _ := workflowstate.NewFileRepository(root)

	running := sampleState("INT-running")
	running.Status = workflow.StatusRunning
	failed := sampleState("INT-failed")
	failed.Status = workflow.StatusFailed
	done := sampleState("INT-done")
	for _, st := range []*workflow.State{running, failed, done} {
		if err := repo.Save(ctx, st); err != nil {
			t.Fatalf("Save %s: %v", st.CorrelationID, err)
		}
	}

	done.Status = workflow.StatusCompleted
	if err := repo.Save(ctx, done); err != nil {
		t.Fatalf("Save completed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, workflowstate.ActiveDir, "INT-done.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed run still in active: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, workflowstate.CompletedDir, "INT-done.json")); err != nil {
		t.Fatalf("completed file missing: %v", err)
	}
	got, err := repo.Get(ctx, "INT-done")
	if err != nil || got.Status != workflow.StatusCompleted {
		t.Fatalf("Get completed: %+v %v", got, err)
	}

	active, err := repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	sort.Strings(active)
	if strings.Join(active, ",") != "INT-failed,INT-running" {
		t.Fatalf("active = %v", active)
	}
}

func TestFileRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	repo, _ := workflowstate.NewFileRepository(t.TempDir())

	if _, err := repo.Get(ctx, "INT-missing"); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("missing: err = %v", err)
	}
	if _, err := repo.Get(ctx, "../etc/passwd"); err == nil || errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("path traversal id accepted: %v", err)
	}
	if err := repo.Save(ctx, &workflow.State{CorrelationID: "bad/id"}); err == nil {
		t.Fatal("invalid id accepted on save")
	}
	if err := repo.Save(ctx, nil); err == nil {
		t.Fatal("nil state accepted")
	}
	if _, err := workflowstate.NewFileRepository(" "); err == nil {
		t.Fatal("blank root accepted")
	}
}

func TestFileRepositoryOverwritesAtomically(t *testing.T) {
	ctx := context.Background()
	repo, _ := workflowstate.NewFileRepository(t.TempDir())
	st := sampleState("INT-2")
	for i := 0; i < 5; i++ {
		st.CurrentAction = i
		if err := repo.Save(ctx, st); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
		got, err := repo.Get(ctx, "INT-2")
		if err != nil || got.CurrentAction != i {
			t.Fatalf("after save %d: %+v %v", i, got, err)
		}
	}
}

// TestFileRepositoryWritesWhileBeingRead reproduces a worker persisting
// state while a reader (the admin viewer) is polling the same file, which on
// Windows makes the atomic rename fail unless it is retried.
func TestFileRepositoryWritesWhileBeingRead(t *testing.T) {
	ctx := context.Background()
	repo, _ := workflowstate.NewFileRepository(t.TempDir())
	st := sampleState("INT-concurrent")
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = repo.Get(ctx, "INT-concurrent")
			}
		}
	}()
	for i := 0; i < 50; i++ {
		st.CurrentAction = i
		if err := repo.Save(ctx, st); err != nil {
			close(stop)
			<-readerDone
			t.Fatalf("Save %d while being read: %v", i, err)
		}
	}
	close(stop)
	<-readerDone
	got, err := repo.Get(ctx, "INT-concurrent")
	if err != nil || got.CurrentAction != 49 {
		t.Fatalf("final state: %+v %v", got, err)
	}
}

func TestFileRepositoryDeleteCompletedBefore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo, _ := workflowstate.NewFileRepository(root)

	old := sampleState("INT-old")
	old.Status = workflow.StatusCompleted
	recent := sampleState("INT-recent")
	recent.Status = workflow.StatusSkipped
	active := sampleState("INT-active")
	active.Status = workflow.StatusFailed
	for _, st := range []*workflow.State{old, recent, active} {
		if err := repo.Save(ctx, st); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, workflowstate.CompletedDir, "INT-old.json"), past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	removed, err := repo.DeleteCompletedBefore(time.Now().Add(-24 * time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d, %v", removed, err)
	}
	if _, err := repo.Get(ctx, "INT-old"); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("old completed state survived: %v", err)
	}
	if _, err := repo.Get(ctx, "INT-recent"); err != nil {
		t.Fatalf("recent completed state deleted: %v", err)
	}
	if _, err := repo.Get(ctx, "INT-active"); err != nil {
		t.Fatalf("active state deleted: %v", err)
	}
}

func TestMemoryRepository(t *testing.T) {
	ctx := context.Background()
	repo := workflowstate.NewMemoryRepository()
	if _, err := repo.Get(ctx, "x"); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("missing: %v", err)
	}
	st := sampleState("INT-3")
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := repo.Get(ctx, "INT-3")
	if err != nil || got.Order == nil || got.Order.ExternalID != "1001" {
		t.Fatalf("Get: %+v %v", got, err)
	}
	// The returned state is a copy: mutating it does not change the store.
	got.Order.ExternalID = "changed"
	again, _ := repo.Get(ctx, "INT-3")
	if again.Order.ExternalID != "1001" {
		t.Fatal("memory repository leaked its internal state")
	}
	active, _ := repo.ListActive(ctx)
	if len(active) != 1 {
		t.Fatalf("active = %v", active)
	}
	st.Status = workflow.StatusCompleted
	_ = repo.Save(ctx, st)
	active, _ = repo.ListActive(ctx)
	if len(active) != 0 {
		t.Fatalf("completed run listed as active: %v", active)
	}
	if repo.Saves != 2 {
		t.Fatalf("saves = %d", repo.Saves)
	}
}
