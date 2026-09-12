package redisqueue_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/queue/redisqueue"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func newQueue(t *testing.T, client *redis.Client, consumer string) *redisqueue.Queue {
	t.Helper()
	q, err := redisqueue.New(client, redisqueue.Options{
		Stream:        "test:jobs",
		Group:         "workers",
		Consumer:      consumer,
		BlockTimeout:  100 * time.Millisecond,
		ClaimMinIdle:  time.Minute,
		MaxDeliveries: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q
}

func testJob(id string) queue.Job {
	return queue.Job{ID: id, CorrelationID: "INT-" + id, EventType: "ORDER_CREATED", Platform: "easyecom", Payload: []byte(`{"k":"v"}`)}
}

func receive(t *testing.T, ch <-chan queue.Delivery) queue.Delivery {
	t.Helper()
	select {
	case d, ok := <-ch:
		if !ok {
			t.Fatal("delivery channel closed")
		}
		return d
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery within 3s")
	}
	return queue.Delivery{}
}

func expectNone(t *testing.T, ch <-chan queue.Delivery, wait time.Duration) {
	t.Helper()
	select {
	case d := <-ch:
		t.Fatalf("unexpected delivery %+v", d.Job)
	case <-time.After(wait):
	}
}

func TestPublishConsumeAck(t *testing.T) {
	mr, client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newQueue(t, client, "c1")

	if err := q.Publish(ctx, testJob("1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := q.Publish(ctx, queue.Job{}); err == nil {
		t.Fatal("invalid job accepted")
	}

	deliveries, err := q.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	d := receive(t, deliveries)
	if d.Job.ID != "1" || d.Job.Attempt != 1 || string(d.Job.Payload) != `{"k":"v"}` || d.Job.EnqueuedAt.IsZero() {
		t.Fatalf("unexpected delivery: %+v", d.Job)
	}
	if err := d.Ack(ctx); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if n, _ := client.XLen(ctx, "test:jobs").Result(); n != 0 {
		t.Fatalf("stream length after ack = %d, want 0", n)
	}
	_ = mr
	expectNone(t, deliveries, 300*time.Millisecond)
}

func TestRequeueAdvancesAttempt(t *testing.T) {
	_, client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newQueue(t, client, "c1")

	if err := q.Publish(ctx, testJob("1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	deliveries, _ := q.Consume(ctx)
	first := receive(t, deliveries)
	if err := first.Requeue(ctx); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	second := receive(t, deliveries)
	if second.Job.ID != "1" || second.Job.Attempt != 2 {
		t.Fatalf("requeued delivery = %+v, want attempt 2", second.Job)
	}
	_ = second.Ack(ctx)
}

func TestUnackedJobIsReclaimedByAnotherConsumer(t *testing.T) {
	mr, client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Consumer one reads the job and dies without acknowledging.
	dead := newQueue(t, client, "dead")
	if err := dead.Publish(ctx, testJob("1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	deadCtx, stopDead := context.WithCancel(ctx)
	deadDeliveries, _ := dead.Consume(deadCtx)
	receive(t, deadDeliveries)
	stopDead()

	// Consumer two sees nothing until the job has been idle long enough.
	alive := newQueue(t, client, "alive")
	aliveDeliveries, _ := alive.Consume(ctx)
	expectNone(t, aliveDeliveries, 300*time.Millisecond)

	// miniredis measures pending idle time against its own clock; move it
	// past ClaimMinIdle so XAUTOCLAIM hands the job to the live consumer.
	mr.SetTime(time.Now().Add(2 * time.Minute))
	d := receive(t, aliveDeliveries)
	if d.Job.ID != "1" || d.Job.Attempt < 2 {
		t.Fatalf("reclaimed delivery = %+v, want job 1 with attempt >= 2", d.Job)
	}
	_ = d.Ack(ctx)
}

func TestPoisonJobIsDeadLettered(t *testing.T) {
	_, client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newQueue(t, client, "c1")

	if err := q.Publish(ctx, testJob("poison")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	deliveries, _ := q.Consume(ctx)
	for attempt := 1; attempt <= 3; attempt++ {
		d := receive(t, deliveries)
		if d.Job.Attempt != attempt {
			t.Fatalf("attempt = %d, want %d", d.Job.Attempt, attempt)
		}
		if err := d.Requeue(ctx); err != nil {
			t.Fatalf("Requeue %d: %v", attempt, err)
		}
	}
	// The fourth delivery exceeds MaxDeliveries and must not reach the worker.
	expectNone(t, deliveries, 400*time.Millisecond)
	if n, _ := client.XLen(ctx, q.DeadLetterStream()).Result(); n != 1 {
		t.Fatalf("dead-letter length = %d, want 1", n)
	}
	if n, _ := client.XLen(ctx, "test:jobs").Result(); n != 0 {
		t.Fatalf("main stream length = %d, want 0", n)
	}
}

func TestUndecodableMessageIsDeadLettered(t *testing.T) {
	_, client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newQueue(t, client, "c1")

	if err := client.XAdd(ctx, &redis.XAddArgs{Stream: "test:jobs", Values: map[string]any{"job": "not json"}}).Err(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	deliveries, _ := q.Consume(ctx)
	expectNone(t, deliveries, 400*time.Millisecond)
	if n, _ := client.XLen(ctx, q.DeadLetterStream()).Result(); n != 1 {
		t.Fatalf("dead-letter length = %d, want 1", n)
	}
}

func TestJobsPublishedBeforeGroupExistsAreDelivered(t *testing.T) {
	_, client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newQueue(t, client, "c1")

	if err := q.Publish(ctx, testJob("early")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	deliveries, err := q.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if d := receive(t, deliveries); d.Job.ID != "early" {
		t.Fatalf("delivery = %+v", d.Job)
	}
}

func TestDefaults(t *testing.T) {
	_, client := newRedis(t)
	q, err := redisqueue.New(client, redisqueue.Options{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	o := q.Options()
	if o.Stream != redisqueue.DefaultStream || o.Group != redisqueue.DefaultGroup || o.Consumer == "" || o.MaxDeliveries != redisqueue.DefaultMaxDeliveries {
		t.Fatalf("defaults not applied: %+v", o)
	}
	if _, err := redisqueue.New(nil, redisqueue.Options{}, nil); err == nil {
		t.Fatal("nil client accepted")
	}
}
