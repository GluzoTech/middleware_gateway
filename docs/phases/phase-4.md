# Phase 4 — `STOCK_SYNC`

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 30 September 2026.

**Goal:** BCPL's stock stays current in EasyEcom, on a schedule, without
pushing what has not changed.

```text
FETCH_VENDOR_STOCK   every page of GetWhInventory for the route's location
MAP_STOCK            SKU translation, safety buffer, drop unchanged entries
PUSH_STOCK           BulkInventoryUpdate into BCPL's EasyEcom location
```

---

## What changed

```text
app/workflow/stocksync/      workflow, actions, and its two scheduled jobs
app/inventorystate/          what was last pushed, per SKU and origin location
  0007_inventory_state.sql
vinculum/vendor.go           StockProvider; refuses to build without a bucket
routing                      ActiveLocations: distinct vendor locations
event                        STOCK_SYNC_DUE
cmd/server/main.go           vendor registered, STOCK_SYNC registered, jobs scheduled
```

This is the first phase whose output actually runs: given credentials, a
route and a SKU map, a deployed gateway now sweeps stock on a timer.

---

## Where the bucket and the quantity rule live

**A deviation from the plan, and a deliberate one.** The plan put "sellable
bucket, quantity rule, SKU translation" all in `MAP_STOCK`. The bucket filter
and assumption A1 are applied in the **Vinculum adapter** instead, and only
SKU translation and change detection happen in the workflow.

The role contract written in Phase 0 already says so: `vendor.StockPage`
levels arrive "already expressed in Gluzo's terms, with the vendor's own
bucket filtering and committed-quantity arithmetic already applied". Buckets
and committed quantities are Vinculum's concepts. A workflow that understood
them would have to be rewritten for the second vendor, which is the thing the
role split exists to prevent.

So `MAP_STOCK` does what is genuinely platform-neutral: translate codes, apply
the configured buffer, drop what has not moved.

## The vendor refuses to start without a sellable bucket

Recommended at the end of Phase 3 and implemented here. `vinculum.NewVendor`
returns an error when `VINCULUM_SELLABLE_BUCKET` is empty.

Reading every bucket is harmless and is what the read-only phase did.
*Publishing* every bucket puts damaged, quarantined and in-transit stock on
the storefront as sellable — and the distinction is invisible in the data,
because every bucket is just a quantity. Failing at start-up is the point: an
unset bucket stops the deployment instead of overselling quietly.

## Only changes are pushed, and a nightly sweep ignores that

`inventory_state` records the quantity last **sent** — after the buffer and
the platform cap — rather than the vendor's figure. That is what makes the
comparison meaningful: two different vendor quantities that clamp to the same
sent value are genuinely the same write.

It is a cache of the gateway's own writes, never a source of truth. It can be
wrong in one direction: a run that wrote the platform and failed before
recording leaves the table behind. **That is exactly why the full sweep
ignores it.** Treating the table as authoritative would make that drift
permanent, and nobody would ever look.

Two scheduled jobs rather than one flag on a schedule, because they are
different work with different costs — the incremental sweep runs often and
usually pushes nothing; the full push runs nightly and writes everything.
Separate names mean separate watermarks, so a failing full push does not hold
up the frequent one.

## Fan-out is per distinct vendor location

`routing.ActiveLocations` is `SELECT DISTINCT`. Several warehouse routes
commonly share one vendor location — a vendor ships to more than one of the
origin's warehouses from the same shelf — and sweeping per route would read
the same stock and push it once per route, spending the origin platform's rate
limit to reach the same answer.

The scheduler builds one queue job per location, so one location's vendor
outage does not fail every other location's sweep.

## The whole catalogue is read before anything is pushed

A page-at-a-time push is interruptible halfway, leaving the storefront holding
a mixture of two sweeps with no record of which SKUs came from which.

## An unmapped SKU fails the run

Never a skip, and the error names the SKUs — the operator's next action is to
add exactly those mappings. They are collected rather than reported one per
failed run. `MAP_STOCK` is a single attempt: a missing mapping row will not
appear because we asked again.

---

## Exit criteria

- [x] **A run with no changes pushes nothing** — and does not call the sink
      at all, since a push of zero SKUs still spends a request.
- [x] **A run with one change pushes one SKU.**
- [x] **The nightly sweep pushes everything**, including when the recorded
      state has diverged from what the platform holds.
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean.

---

## A bug the tests caught

`event.Validate` rejects unknown event types, and `STOCK_SYNC_DUE` was not in
its list. Every scheduled job would have been **discarded by the worker**
before reaching the workflow — a silent, total failure of the feature, with
the queue looking healthy.

Found by asserting in the schedule test that the event a job carries is one
the worker would accept, rather than only that the job was built.

---

## Still blocked from going live

Three, unchanged in substance from [blockers.md](../blockers.md):

| | | |
|---|---|---|
| **B1** | Does `committedQty` rise or fall? | One expression in the Vinculum mapper |
| **B2** | Which `bucket` is sellable? | One environment variable — and the process now refuses to start without it |
| **B15** | The bulk endpoint's path | One constant; fails loudly on the first call |

The code is complete and tested against fakes. None of these changes its
shape.

---

## Follow-ups

1. **`inventory_state` has no retention.** A SKU removed from the vendor's
   catalogue leaves its row behind forever. Harmless at this scale; fold into
   the existing retention job.
2. **`Forget` has no operator command.** It is the repair tool for a
   corrupted record and is currently only reachable from code. One
   `gatewayctl` subcommand.
3. **The sweep reads the whole catalogue every time.** `GetWhInventory`
   accepts `fromDate`, and `Sweep.Since` is already plumbed through, but
   nothing sets it — an incremental read needs B11 (the date format)
   confirmed first, and reading everything is correct meanwhile.

---

## Next

Phase 5 — orders to Vinculum. It adds `OrderReceiver` to the same adapter, at
which point `ORDER_SYNC` registers itself again. The plan's standing warning
applies: **do not run Phase 5 against BCPL production.**
