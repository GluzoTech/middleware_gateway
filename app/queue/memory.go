package queue

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Publish after Close.
var ErrClosed = errors.New("queue: closed")

// Memory is an in-process Queue for tests and local runs. It has the same
// delivery semantics as the Redis queue (Attempt counting, requeue) without
// durability.
type Memory struct {
	ch     chan Job
	mu     sync.Mutex
	closed bool
}

// NewMemory creates a queue buffering up to size jobs.
func NewMemory(size int) *Memory {
	if size < 1 {
		size = 1
	}
	return &Memory{ch: make(chan Job, size)}
}

// Publish implements Publisher. It blocks while the buffer is full.
func (m *Memory) Publish(ctx context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	select {
	case m.ch <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Consume implements Consumer.
func (m *Memory) Consume(ctx context.Context) (<-chan Delivery, error) {
	out := make(chan Delivery)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case job, ok := <-m.ch:
				if !ok {
					return
				}
				job.Attempt++
				d := Delivery{
					Job: job,
					Ack: func(context.Context) error { return nil },
					Requeue: func(ctx context.Context) error {
						return m.Publish(ctx, job)
					},
				}
				select {
				case out <- d:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// Len reports the number of buffered jobs.
func (m *Memory) Len() int { return len(m.ch) }

// Close implements Queue. Buffered jobs are still delivered to an active
// consumer.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.ch)
	}
	return nil
}
