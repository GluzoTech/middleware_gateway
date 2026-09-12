// Package redisqueue implements queue.Queue on Redis Streams.
//
// Jobs are XADDed to one stream and read through a consumer group, which
// gives at-least-once delivery: a job stays in the group's pending list until
// the worker acknowledges it. Jobs left pending by a crashed worker are
// reclaimed with XAUTOCLAIM after ClaimMinIdle, and jobs delivered more than
// MaxDeliveries times are moved to a dead-letter stream instead of looping
// forever.
package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/gluzo/integration-gateway/app/queue"
)

// Defaults.
const (
	DefaultStream        = "gluzo:jobs"
	DefaultGroup         = "gateway-workers"
	DefaultBatchSize     = 10
	DefaultBlockTimeout  = 5 * time.Second
	DefaultClaimMinIdle  = 60 * time.Second
	DefaultMaxDeliveries = 5
	DefaultMaxLen        = 100_000
	DeadLetterSuffix     = ":dead"

	jobField    = "job"
	reasonField = "reason"
)

// Options tunes the queue.
type Options struct {
	Stream   string
	Group    string
	Consumer string
	// BatchSize is the maximum number of jobs fetched per read.
	BatchSize int64
	// BlockTimeout bounds how long a read waits for new jobs.
	BlockTimeout time.Duration
	// ClaimMinIdle is how long a pending job may sit unacknowledged before
	// another consumer reclaims it.
	ClaimMinIdle time.Duration
	// MaxDeliveries caps delivery attempts before a job is dead-lettered.
	MaxDeliveries int
	// MaxLen approximately caps the stream length.
	MaxLen int64
}

func (o *Options) applyDefaults() {
	if o.Stream == "" {
		o.Stream = DefaultStream
	}
	if o.Group == "" {
		o.Group = DefaultGroup
	}
	if o.Consumer == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "gateway"
		}
		o.Consumer = fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()[:8])
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.BlockTimeout <= 0 {
		o.BlockTimeout = DefaultBlockTimeout
	}
	if o.ClaimMinIdle <= 0 {
		o.ClaimMinIdle = DefaultClaimMinIdle
	}
	if o.MaxDeliveries <= 0 {
		o.MaxDeliveries = DefaultMaxDeliveries
	}
	if o.MaxLen <= 0 {
		o.MaxLen = DefaultMaxLen
	}
}

// Queue is a Redis Streams queue.
type Queue struct {
	rdb    redis.Cmdable
	opts   Options
	logger *slog.Logger
	now    func() time.Time
}

// New builds a Queue on rdb.
func New(rdb redis.Cmdable, opts Options, logger *slog.Logger) (*Queue, error) {
	if rdb == nil {
		return nil, errors.New("redisqueue: redis client is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	opts.applyDefaults()
	return &Queue{rdb: rdb, opts: opts, logger: logger, now: time.Now}, nil
}

// Options returns the effective options.
func (q *Queue) Options() Options { return q.opts }

// DeadLetterStream is where poison jobs end up.
func (q *Queue) DeadLetterStream() string { return q.opts.Stream + DeadLetterSuffix }

// Publish implements queue.Publisher.
func (q *Queue) Publish(ctx context.Context, job queue.Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if job.EnqueuedAt.IsZero() {
		job.EnqueuedAt = q.now()
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("redisqueue: encode job: %w", err)
	}
	if err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: q.opts.Stream,
		MaxLen: q.opts.MaxLen,
		Approx: true,
		Values: map[string]any{jobField: string(data)},
	}).Err(); err != nil {
		return fmt.Errorf("redisqueue: publish: %w", err)
	}
	return nil
}

// Consume implements queue.Consumer. The returned channel closes when ctx
// is cancelled.
func (q *Queue) Consume(ctx context.Context) (<-chan queue.Delivery, error) {
	if err := q.ensureGroup(ctx); err != nil {
		return nil, err
	}
	out := make(chan queue.Delivery)
	go q.loop(ctx, out)
	return out, nil
}

// Close implements queue.Queue. The Redis client is owned by the caller.
func (q *Queue) Close() error { return nil }

// ensureGroup creates the consumer group from the start of the stream so
// jobs published before the first worker started are not skipped.
func (q *Queue) ensureGroup(ctx context.Context) error {
	err := q.rdb.XGroupCreateMkStream(ctx, q.opts.Stream, q.opts.Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("redisqueue: create consumer group: %w", err)
	}
	return nil
}

func (q *Queue) loop(ctx context.Context, out chan<- queue.Delivery) {
	defer close(out)
	for ctx.Err() == nil {
		msgs, reclaimed, err := q.fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			q.logger.ErrorContext(ctx, "redisqueue: fetch failed", slog.String("error", err.Error()))
			if !sleep(ctx, time.Second) {
				return
			}
			continue
		}
		for _, m := range msgs {
			d, ok := q.delivery(ctx, m, reclaimed)
			if !ok {
				continue
			}
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
	}
}

// fetch first reclaims stale pending jobs, then blocks for new ones.
func (q *Queue) fetch(ctx context.Context) ([]redis.XMessage, bool, error) {
	claimed, _, err := q.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   q.opts.Stream,
		Group:    q.opts.Group,
		Consumer: q.opts.Consumer,
		MinIdle:  q.opts.ClaimMinIdle,
		Start:    "0-0",
		Count:    q.opts.BatchSize,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, false, fmt.Errorf("autoclaim: %w", err)
	}
	if len(claimed) > 0 {
		return claimed, true, nil
	}

	streams, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.opts.Group,
		Consumer: q.opts.Consumer,
		Streams:  []string{q.opts.Stream, ">"},
		Count:    q.opts.BatchSize,
		Block:    q.opts.BlockTimeout,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("readgroup: %w", err)
	}
	var msgs []redis.XMessage
	for _, s := range streams {
		msgs = append(msgs, s.Messages...)
	}
	return msgs, false, nil
}

// delivery decodes a message, applies the delivery count and dead-letters
// poison messages. ok is false when nothing should be handed to the worker.
func (q *Queue) delivery(ctx context.Context, m redis.XMessage, reclaimed bool) (queue.Delivery, bool) {
	raw, _ := m.Values[jobField].(string)
	var job queue.Job
	if err := json.Unmarshal([]byte(raw), &job); err != nil || job.Validate() != nil {
		q.logger.ErrorContext(ctx, "redisqueue: discarding undecodable job", slog.String("message_id", m.ID))
		q.deadLetter(ctx, m.ID, raw, "undecodable")
		return queue.Delivery{}, false
	}

	deliveries := int64(1)
	if reclaimed {
		if pending, err := q.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: q.opts.Stream, Group: q.opts.Group, Start: m.ID, End: m.ID, Count: 1,
		}).Result(); err == nil && len(pending) == 1 && pending[0].RetryCount > 0 {
			deliveries = pending[0].RetryCount
		} else {
			deliveries = 2
		}
	}
	job.Attempt += int(deliveries)

	if job.Attempt > q.opts.MaxDeliveries {
		q.logger.ErrorContext(ctx, "redisqueue: job exceeded max deliveries; dead-lettering",
			slog.String("job_id", job.ID),
			slog.String("correlation_id", job.CorrelationID),
			slog.Int("attempt", job.Attempt),
		)
		q.deadLetter(ctx, m.ID, raw, "max deliveries exceeded")
		return queue.Delivery{}, false
	}

	id := m.ID
	return queue.Delivery{
		Job: job,
		Ack: func(ctx context.Context) error { return q.ack(ctx, id) },
		Requeue: func(ctx context.Context) error {
			if err := q.Publish(ctx, job); err != nil {
				return err
			}
			return q.ack(ctx, id)
		},
	}, true
}

func (q *Queue) ack(ctx context.Context, id string) error {
	if err := q.rdb.XAck(ctx, q.opts.Stream, q.opts.Group, id).Err(); err != nil {
		return fmt.Errorf("redisqueue: ack: %w", err)
	}
	if err := q.rdb.XDel(ctx, q.opts.Stream, id).Err(); err != nil {
		return fmt.Errorf("redisqueue: delete: %w", err)
	}
	return nil
}

func (q *Queue) deadLetter(ctx context.Context, id, raw, reason string) {
	if err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: q.DeadLetterStream(),
		MaxLen: q.opts.MaxLen,
		Approx: true,
		Values: map[string]any{jobField: raw, reasonField: reason, "source_id": id},
	}).Err(); err != nil {
		q.logger.ErrorContext(ctx, "redisqueue: dead-letter publish failed", slog.String("error", err.Error()))
		return
	}
	if err := q.ack(ctx, id); err != nil {
		q.logger.ErrorContext(ctx, "redisqueue: dead-letter ack failed", slog.String("error", err.Error()))
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
