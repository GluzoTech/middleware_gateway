# Phase 5 — Orders to Vinculum

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 30 September 2026.

**Goal:** a BCPL order placed on the storefront reaches BCPL's warehouse.

> **Do not run this phase against BCPL production.** It is the first phase
> that writes into their live system, and unlike a stock push, a wrongly
> created order is not self-correcting. It needs B4 (the `orderLocation`
> code) and B5 (test credentials) first.

---

## What changed

```text
vinculum/dto/order/          CreateOrderRequest, lines, response
vinculum/mapper/order.go     pure domain order -> create-order document
vinculum/endpoint_order.go   CreateOrder, duplicate recognition
vinculum/ratelimit.go        80 calls per 5 minutes, sliding window
vinculum/vendor.go           + PrepareOrder, SubmitOrder
ordersync                    SKU translation and persisted line numbers
tests/e2e_test.go            the real adapter over a fake Vinculum server
```

`ORDER_SYNC` registers itself again: `buildWorker` checks for the
`OrderReceiver` role rather than merely that some vendor exists, so the
workflow appears exactly when a vendor can actually receive an order.

---

## Idempotency is structural, not bookkeeping

`orderNo` carries Gluzo's own order id. Vinculum rejects a second order under
the same number, and that rejection is reported as `Created=false` rather than
raised as an error.

The gateway therefore does not have to remember what it submitted. A resumed
run, a redelivered webhook and a retried attempt all converge on one order at
the vendor, and they do so even if the gateway's own state was lost — which is
the case a bookkeeping approach handles worst.

**Failing to recognise a duplicate is safe**, which is what makes a guess
tolerable while the exact wording is unconfirmed. An unrecognised rejection is
an error: the workflow retries, Vinculum rejects again, and the run fails
permanently with the vendor's own message in the log. The order is stuck and
visible. It is never duplicated, because Vinculum did the rejecting.

A transport failure or a 5xx is explicitly **not** treated as a duplicate. It
says nothing about whether the order exists, and reporting it as one would
mark an order accepted that the vendor never saw.

## The rate limiter

Vinculum documents 80 calls per five minutes on order creation. The queue
paces work but enforces no ceiling: a backlog drains as fast as the workers
can go.

The window **slides** rather than resetting on a fixed boundary. A fixed
window lets 160 calls through across its edge — 80 at the end of one and 80 at
the start of the next — which is exactly the burst the limit exists to
prevent.

It is applied to order creation only, because that is where the limit is
documented. Throttling the stock sweep on the same budget would be inventing a
constraint, and a sweep pacing itself at one page every four seconds would
take hours.

**It bounds one process.** Several replicas each hold their own budget. See
the follow-ups.

## SKU translation happens in the workflow

`PrepareOrder` must be pure — no network, no database — so it cannot load the
SKU map. `MAP_ORDER` translates first and hands the adapter an order already
carrying vendor item codes.

It translates into a **copy**. `state.Order` stays the origin's view of the
order: it is what the execution log shows and what an operator recognises, and
overwriting its SKUs would make the log describe an order EasyEcom never had.

An order with any unmapped SKU fails entirely. Sending the mapped lines and
dropping the rest would ship the customer part of their order and leave no
record that the remainder was never offered to anyone.

`MAP_ORDER` is no longer `NoRetry`: it reads the database now, and a transient
database failure must not fail an order permanently. A mapping error is
non-retryable in itself, so the budget does not cause a mapping failure to be
retried.

## Line numbers are persisted

`PayloadVendorOrderLines` records which vendor SKU each line number carries.
Vinculum's shipment response is line-level, and the SKU map can change between
this run and the dispatch that refers to it, so the mapping is stored rather
than recomputed in Phase 6.

## The e2e uses the real adapter now

Phase 0 replaced the fake Uniware server with an in-process stub so that phase
could run at all, and promised to restore transport-level coverage here. Done:
the definition-of-done scenario drives the **real** client, mapper and
duplicate handling over HTTP, against a fake Vinculum server that keeps an
order book.

Three submission attempts across a simulated crash leave **one** order at the
vendor, and the vendor is verified to have received the mapped `VIN-E2E` code
rather than Gluzo's `BCPL-E2E`, translated through the real `sku_map` table.

One adjustment was needed: the HTTP client's own retry was absorbing the
simulated outage below the workflow's submit budget, so the scenario completed
instead of failing and resuming. Transport retry is off in that test, and the
comment says why — the scenario is about resume, not about backoff, which has
its own tests.

---

## Exit criteria

- [x] **Submitting the same order twice creates one order at Vinculum.**
- [x] **A resumed run after a simulated crash does not duplicate** — proved in
      the e2e against the real adapter and a real order book.
- [x] **The rate limiter holds under a burst of 200 queued orders** — no
      five-minute window contains more than 80 calls, checked over every
      window that starts at a call.
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean.

---

## Unverified, and one of them is the dangerous kind

| Item | If wrong |
|---|---|
| **The `ship*` address field names** | **Fails quietly.** The specification says "the ship* address block" without enumerating it. Vinculum would accept the order and ship it with a field missing. |
| The duplicate rejection's code and wording | Fails loudly; the order is stuck and visible, never duplicated |
| `orderType`, `paymentType` values | Fails loudly at submission |
| `orderDate` format | Shared with B11, already tracked |

The first is the only thing in this phase that can go wrong without anyone
noticing, and it is **the reason not to run Phase 5 against production before
B4 and B5**. One test order against BCPL's test environment settles it.

Added to [blockers.md](../blockers.md) as B17–B18.

---

## Follow-ups

1. **The rate limiter is per process.** Two workers mean two budgets and
   160 calls per five minutes. Either give each replica a share of the
   ceiling (`VINCULUM_ORDER_RATE_LIMIT` divided by the replica count, which is
   configuration, not code) or move the limiter to Redis alongside the
   scheduler lock. The second is correct; the first is available today.
2. **`ORDER_CANCELLED` still has no workflow.** Vinculum publishes order
   operations, so a cancellation workflow is the same shape as this one. It
   is not in the plan's phases and needs a decision — a cancellation that
   never reaches the warehouse is a parcel shipped to someone who asked you
   not to.
3. **No per-order check that stock existed.** Deferred by agreement; BCPL
   maintain buffer stock and a rejection goes to the dead-letter path in
   Phase 7.

---

## Next

Phase 6 — `SHIPMENT_SYNC`. It adds `FulfilmentProvider` to the same adapter
and pushes dispatch back into EasyEcom, which needs B7 (the shipment status
enum) and B8 (courier registration).
