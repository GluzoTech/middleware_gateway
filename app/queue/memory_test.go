package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/queue"
)

func testJob(id string) queue.Job {
	return queue.Job{ID: id, CorrelationID: "INT-" + id, EventType: "ORDER_CREATED", Platform: "easyecom"}
}

func receive(t *testing.T, ch <-chan queue.Delivery) queue.Delivery {
	t.Helper()
	select {
	case d, ok := <-ch:
		if !ok {
			t.Fatal("delivery channel closed")
		}
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery within 2s")
	}
	return queue.Delivery{}
}

func TestJobValidate(t *testing.T) {
	if err := testJob("1").Validate(); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	for _, bad := range []queue.Job{{}, {ID: "1"}, {ID: "1", CorrelationID: "c"}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("job %+v should be invalid", bad)
		}
	}
	if err := (queue.Job{ID: "1", CorrelationID: "c", Workflow: "ORDER_SYNC"}).Validate(); err != nil {
		t.Fatalf("job with workflow but no event type rejected: %v", err)
	}
}

func TestMemoryQueueDeliversInOrderWithAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := queue.NewMemory(10)

	for _, id := range []string{"a", "b"} {
		if err := q.Publish(ctx, testJob(id)); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}
	deliveries, err := q.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	first := receive(t, deliveries)
	if first.Job.ID != "a" || first.Job.Attempt != 1 {
		t.Fatalf("first delivery = %+v", first.Job)
	}
	if err := first.Requeue(ctx); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	second := receive(t, deliveries)
	if second.Job.ID != "b" || second.Job.Attempt != 1 {
		t.Fatalf("second delivery = %+v", second.Job)
	}
	if err := second.Ack(ctx); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	third := receive(t, deliveries)
	if third.Job.ID != "a" || third.Job.Attempt != 2 {
		t.Fatalf("requeued delivery = %+v, want job a attempt 2", third.Job)
	}
}

func TestMemoryQueueRejectsInvalidJobsAndPublishAfterClose(t *testing.T) {
	q := queue.NewMemory(1)
	if err := q.Publish(context.Background(), queue.Job{}); err == nil {
		t.Fatal("invalid job accepted")
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := q.Publish(context.Background(), testJob("x")); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("publish after close: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMemoryQueueConsumerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := queue.NewMemory(1)
	deliveries, _ := q.Consume(ctx)
	cancel()
	select {
	case _, ok := <-deliveries:
		if ok {
			t.Fatal("unexpected delivery")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}
