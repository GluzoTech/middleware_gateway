package scheduler_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gluzo/integration-gateway/app/scheduler"
)

func newRedisLocker(t *testing.T) (*miniredis.Miniredis, *scheduler.RedisLocker) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, scheduler.NewRedisLocker(client, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRedisLockerExcludesASecondHolder(t *testing.T) {
	ctx := context.Background()
	_, locker := newRedisLocker(t)

	release, ok, err := locker.Acquire(ctx, "stock-sync", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first Acquire: ok=%v err=%v", ok, err)
	}

	_, ok, err = locker.Acquire(ctx, "stock-sync", time.Minute)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if ok {
		t.Fatal("two holders of the same job lock: the runs would overlap")
	}

	// A different job is unaffected: the lock is per job name.
	otherRelease, ok, err := locker.Acquire(ctx, "shipment-sync", time.Minute)
	if err != nil || !ok {
		t.Fatalf("another job must not be blocked: ok=%v err=%v", ok, err)
	}
	otherRelease(ctx)

	release(ctx)
	release(ctx) // releasing twice must be harmless

	_, ok, err = locker.Acquire(ctx, "stock-sync", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Acquire after release: ok=%v err=%v", ok, err)
	}
}

func TestRedisLockExpiresSoACrashedReplicaDoesNotBlockForever(t *testing.T) {
	ctx := context.Background()
	mr, locker := newRedisLocker(t)

	// Acquired and never released: the replica died mid-run.
	if _, ok, err := locker.Acquire(ctx, "stock-sync", time.Minute); err != nil || !ok {
		t.Fatalf("Acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := locker.Acquire(ctx, "stock-sync", time.Minute); ok {
		t.Fatal("the lock should still be held")
	}

	mr.FastForward(61 * time.Second)

	if _, ok, err := locker.Acquire(ctx, "stock-sync", time.Minute); err != nil || !ok {
		t.Fatalf("after the TTL another replica must be able to take the job: ok=%v err=%v", ok, err)
	}
}

// A replica that stalls past its TTL must not delete the lock a different
// replica has since taken. A plain DEL on release would do exactly that, and
// the overlap the lock exists to prevent would happen at the worst moment.
func TestReleaseDoesNotDropSomeoneElsesLock(t *testing.T) {
	ctx := context.Background()
	mr, locker := newRedisLocker(t)

	stalled, ok, err := locker.Acquire(ctx, "stock-sync", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Acquire: ok=%v err=%v", ok, err)
	}

	// The first holder stalls; its lock expires and a second replica takes it.
	mr.FastForward(61 * time.Second)
	if _, ok, err = locker.Acquire(ctx, "stock-sync", time.Minute); err != nil || !ok {
		t.Fatalf("second Acquire: ok=%v err=%v", ok, err)
	}

	// The first holder finally finishes and releases.
	stalled(ctx)

	// The second holder must still have the lock.
	if _, ok, _ := locker.Acquire(ctx, "stock-sync", time.Minute); ok {
		t.Fatal("the stalled replica released a lock it no longer owned")
	}
}

func TestNopLockerAlwaysGrants(t *testing.T) {
	ctx := context.Background()
	var l scheduler.Locker = scheduler.NopLocker{}
	for i := 0; i < 3; i++ {
		release, ok, err := l.Acquire(ctx, "stock-sync", time.Minute)
		if err != nil || !ok {
			t.Fatalf("Acquire %d: ok=%v err=%v", i, ok, err)
		}
		release(ctx)
	}
}

func TestMemoryLockerMatchesTheRedisContract(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	l := scheduler.NewMemoryLocker()
	l.SetClock(c.now)

	release, ok, err := l.Acquire(ctx, "stock-sync", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := l.Acquire(ctx, "stock-sync", time.Minute); ok {
		t.Fatal("two holders of the same lock")
	}

	c.advance(61 * time.Second)
	if _, ok, err := l.Acquire(ctx, "stock-sync", time.Minute); err != nil || !ok {
		t.Fatalf("expired lock must be re-acquirable: ok=%v err=%v", ok, err)
	}
	release(ctx)
}
