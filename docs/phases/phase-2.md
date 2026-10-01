# Phase 2 — Scheduler

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 30 September 2026.

**Goal:** periodic work enters the existing queue, exactly once across
replicas, without losing a tick that was missed while the process was down.

---

## Why there is a scheduler at all

A dropship vendor cannot call us. Vinculum's published specification contains
no callback registration — every documented operation is inbound — so stock
and dispatch have to be pulled. That is assumption A2 of the plan.

If BCPL later confirm push is available, a receiving endpoint is added and
this keeps running as the backstop. Push improves latency; it does not remove
the sweep. A pushed event dropped during a deployment leaves an order with no
tracking forever, whereas a sweep recovers by itself.

---

## What changed

### Added: `app/scheduler`

```text
scheduler.go   Job, Window, the poll loop and one tick's logic
store.go       Store contract, PostgreSQL implementation, in-memory one
lock.go        Locker contract, Redis implementation, no-op and in-memory ones
```

Plus migration `0004_scheduler_state.sql` and `SCHEDULER_*` configuration.

The scheduler performs no work of its own. A due job builds queue jobs and
publishes them, so scheduled work travels the same queue, worker, retry,
resume and execution-log path as a webhook event. Nothing bypasses the log and
there is no second execution engine to reason about.

### The claim is in PostgreSQL; the lock is in Redis

**This is a deliberate departure from the plan**, which named a short-lived
Redis lock as the single-firing mechanism. The lock is here and does a real
job, but it is not what makes a tick fire once.

Firing once is a conditional `UPDATE` on `scheduler_state`:

```sql
UPDATE scheduler_state SET next_due_at = $now + interval, last_fired_at = $now
WHERE job_name = $1 AND next_due_at <= $now
RETURNING watermark, next_due_at, ...
```

PostgreSQL serialises the two updates; the loser's clause no longer matches
and it gets no row back. The reason to put it here rather than in Redis is
that **the claim and the watermark are the same row**. With a Redis lock as
the claim there are two sources of truth for "has this tick been taken", and a
replica that dies between acquiring the lock and writing the watermark leaves
them disagreeing.

The lock covers what the claim cannot: a run that outlives its own interval,
whose next tick would otherwise start on another replica while the first is
still talking to the vendor. Two overlapping stock sweeps double the load on a
vendor whose documented limit is 80 calls per five minutes.

Its release is a compare-and-delete Lua script, not `DEL`. A replica that
stalls past its TTL no longer owns the lock; deleting on the way out would
drop a lock another replica has since taken, producing exactly the overlap the
lock exists to prevent.

### The watermark is seeded at registration, not at first success

A bug the tests caught rather than the design anticipated.

The first version set the watermark only after a successful run. A job whose
first run failed therefore had no watermark, and the run after it started from
its own `InitialLookback` — silently skipping everything between deployment
and the first success. That is the window most likely to have failed.

`Register` now writes `watermark = now - InitialLookback` when it creates the
row, so a watermark always exists and the zero case is a defensive fallback
for rows written by an earlier version.

The same bug made a long backlog drain in one oversized window instead of in
capped chunks. One fix, both symptoms.

### Failure leaves the watermark alone

- **Build fails** → watermark untouched; the next run covers this window and
  everything since.
- **Publish fails partway** → the jobs already queued stay queued, the
  watermark does not advance, and the window is republished. Scheduled
  workflows are idempotent precisely so this is safe, and the default
  idempotency key is per job name, window end and target, so a republished
  window is recognised as a repeat rather than executed twice.
- **Process killed mid-tick** → the claim already moved `next_due_at`, so no
  other replica retries immediately, and the Redis lock expires on its TTL.
  The watermark is untouched, so the next run covers the whole gap.

`last_fired_at` records the attempt even when the run fails. Far ahead of
`last_success_at` means runs are starting and not finishing, which is the
thing an operator wants to see.

### `MaxWindow` drains a backlog in chunks

After a long outage the window would widen to the whole outage and ask the
vendor for everything at once. A capped run covers at most `MaxWindow`,
advances the watermark only that far, and leaves the job **immediately due**
rather than waiting a full interval — so a day's backlog drains faster than it
accumulated instead of in real time. Nothing is lost: the remainder is the
next window.

### Wired into `run()`, with nothing registered

`cmd/server/main.go` builds the scheduler alongside the retention goroutine.
No job is registered: `STOCK_SYNC` is Phase 4 and `SHIPMENT_SYNC` is Phase 6.
An empty scheduler logs that it has nothing to run and returns, in the same
spirit as `buildWorker` with no vendor — the binary should be honest about the
state it is in rather than pretend.

---

## Exit criteria

- [x] **Two replicas under test fire a tick exactly once.** Proved twice: in
      `app/scheduler` with a shared in-memory store, and in `tests/` with a
      real PostgreSQL row and a real Redis lock, ten concurrent claims for one
      tick.
- [x] **A process killed mid-tick resumes from the watermark rather than
      skipping.** A run that fails mid-tick leaves the watermark; the next run
      after two further intervals covers the whole gap, not just the last one.
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean.

The in-memory store proves the scheduler's logic and nothing about whether two
conditional updates serialise, which is the entire mechanism — hence the
database test.

---

## Follow-ups

1. **No job exercises the loop in production yet.** The wiring is covered by
   tests only until Phase 4.
2. **`scheduler_state` is not in the admin viewer.** An operator cannot
   currently see watermarks or a job that is firing without finishing. Small
   addition; belongs with Phase 7's reconciliation view.
3. **The poll interval is global.** A job due every 24 hours is still checked
   every 30 seconds. Harmless at this scale, and the alternative — per-job
   timers — reintroduces the replica problem.

---

## Next

Phase 3 — SKU mapping and EasyEcom as a stock sink. Blocked on EasyEcom open
item 6 (a separate location for BCPL and its `location_key`) for a live test;
the code can be written against a fake first. See
[blockers.md](../blockers.md) for what that costs and what can proceed.
