package tests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/idempotency"
)

func TestIdempotencyPostgresStore(t *testing.T) {
	ctx := context.Background()
	store := idempotency.NewPostgresStore(pool)
	key := "test:ORDER_CREATED:" + uuid.NewString()

	rec := idempotency.Record{Key: key, CorrelationID: "INT-1", Platform: "easyecom", EventType: "ORDER_CREATED"}
	first, err := store.Claim(ctx, rec)
	if err != nil || !first.Accepted {
		t.Fatalf("first claim: %+v %v", first, err)
	}

	dup := rec
	dup.CorrelationID = "INT-2"
	second, err := store.Claim(ctx, dup)
	if err != nil || second.Accepted || second.Existing == nil || second.Existing.CorrelationID != "INT-1" {
		t.Fatalf("duplicate claim: %+v %v", second, err)
	}

	if err := store.SetStatus(ctx, key, idempotency.StatusProcessing); err != nil {
		t.Fatalf("SetStatus processing: %v", err)
	}
	got, err := store.Get(ctx, key)
	if err != nil || got.Status != idempotency.StatusProcessing || got.ProcessedAt != nil {
		t.Fatalf("Get after processing: %+v %v", got, err)
	}
	if err := store.SetStatus(ctx, key, idempotency.StatusCompleted); err != nil {
		t.Fatalf("SetStatus completed: %v", err)
	}
	got, err = store.Get(ctx, key)
	if err != nil || got.Status != idempotency.StatusCompleted || got.ProcessedAt == nil {
		t.Fatalf("Get after completion: %+v %v", got, err)
	}
	if err := store.SetStatus(ctx, "missing:"+uuid.NewString(), idempotency.StatusFailed); !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("SetStatus missing: %v", err)
	}

	if err := store.Release(ctx, key); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("released key still present: %v", err)
	}

	old := "test:old:" + uuid.NewString()
	if _, err := store.Claim(ctx, idempotency.Record{Key: old, CorrelationID: "INT-3", Platform: "easyecom", EventType: "ORDER_CREATED"}); err != nil {
		t.Fatalf("claim old: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE idempotency_records SET created_at = now() - interval '40 days' WHERE idempotency_key = $1`, old); err != nil {
		t.Fatalf("age record: %v", err)
	}
	n, err := store.DeleteOlderThan(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil || n < 1 {
		t.Fatalf("DeleteOlderThan = %d, %v", n, err)
	}
	if _, err := store.Get(ctx, old); !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("old record survived retention: %v", err)
	}
}

func TestIdempotencyPostgresConcurrentClaims(t *testing.T) {
	ctx := context.Background()
	store := idempotency.NewPostgresStore(pool)
	key := "test:race:" + uuid.NewString()

	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := store.Claim(ctx, idempotency.Record{Key: key, CorrelationID: uuid.NewString(), Platform: "easyecom", EventType: "ORDER_CREATED"})
			if err != nil {
				t.Errorf("Claim: %v", err)
				return
			}
			if c.Accepted {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}
