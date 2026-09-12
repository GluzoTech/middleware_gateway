package idempotency_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/idempotency"
)

func TestKeyFor(t *testing.T) {
	if got := idempotency.KeyFor(" easyecom ", "ORDER_CREATED", " 123 "); got != "easyecom:ORDER_CREATED:123" {
		t.Fatalf("KeyFor = %q", got)
	}
	long := idempotency.KeyFor("easyecom", strings.Repeat("x", 300))
	if len(long) > idempotency.MaxKeyLength || !strings.HasPrefix(long, "easyecom:sha256:") {
		t.Fatalf("long key not hashed with prefix: %q", long)
	}
	if idempotency.KeyFor("a", strings.Repeat("x", 300)) == idempotency.KeyFor("a", strings.Repeat("y", 300)) {
		t.Fatal("different long keys collided")
	}
}

func TestMemoryStoreClaimSemantics(t *testing.T) {
	ctx := context.Background()
	store := idempotency.NewMemoryStore()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	rec := idempotency.Record{Key: "easyecom:ORDER_CREATED:1", CorrelationID: "INT-1", Platform: "easyecom", EventType: "ORDER_CREATED"}
	first, err := store.Claim(ctx, rec)
	if err != nil || !first.Accepted {
		t.Fatalf("first claim: %+v %v", first, err)
	}

	dup := rec
	dup.CorrelationID = "INT-2"
	second, err := store.Claim(ctx, dup)
	if err != nil || second.Accepted || second.Existing == nil || second.Existing.CorrelationID != "INT-1" || second.Existing.Status != idempotency.StatusAccepted {
		t.Fatalf("duplicate claim: %+v %v", second, err)
	}

	if err := store.SetStatus(ctx, rec.Key, idempotency.StatusCompleted); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, err := store.Get(ctx, rec.Key)
	if err != nil || got.Status != idempotency.StatusCompleted || got.ProcessedAt == nil {
		t.Fatalf("Get after completion: %+v %v", got, err)
	}
	if err := store.SetStatus(ctx, rec.Key, "bogus"); err == nil {
		t.Fatal("invalid status accepted")
	}
	if err := store.SetStatus(ctx, "missing", idempotency.StatusFailed); !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("SetStatus missing: %v", err)
	}

	if err := store.Release(ctx, rec.Key); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := store.Get(ctx, rec.Key); !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("released key still present: %v", err)
	}
	again, err := store.Claim(ctx, rec)
	if err != nil || !again.Accepted {
		t.Fatalf("claim after release: %+v %v", again, err)
	}

	if _, err := store.Claim(ctx, idempotency.Record{}); err == nil {
		t.Fatal("empty key accepted")
	}

	store.Now = func() time.Time { return now.Add(48 * time.Hour) }
	if _, err := store.Claim(ctx, idempotency.Record{Key: "newer"}); err != nil {
		t.Fatalf("claim newer: %v", err)
	}
	n, err := store.DeleteOlderThan(ctx, now.Add(24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v; want 1", n, err)
	}
	if _, err := store.Get(ctx, "newer"); err != nil {
		t.Fatal("newer record was deleted")
	}
}

func TestMemoryStoreConcurrentDuplicatesYieldOneWinner(t *testing.T) {
	ctx := context.Background()
	store := idempotency.NewMemoryStore()
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := store.Claim(ctx, idempotency.Record{Key: "same"})
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
