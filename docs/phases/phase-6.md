# Phase 6 — `SHIPMENT_SYNC`

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 30 September 2026.

**Goal:** the customer sees tracking.

```text
FETCH_SHIPMENTS   ShipmentDetail from the last watermark, paged
MAP_SHIPMENTS     carrier translation, and the decision whether to push at all
PUSH_SHIPMENT     AssignShipmentDetails, then updateTrackingStatus
```

---

## What changed

```text
app/workflow/shipmentsync/   workflow, actions, scheduled job
app/couriermap/              vendor carrier name -> origin carrier id  (0008)
app/shipmentstate/           furthest state pushed per package         (0009)
vinculum/vendor.go           + FetchShipments
easyecom/endpoint_tracking   + AssignShipmentDetails, UpdateTrackingStatus
easyecom/sink.go             + PushShipment
domain/tracking              + Progress(), IsAdvanceOver(), DeliveredAt
cmd/gatewayctl               courier add/list/set-status/remove
```

The Vinculum adapter now implements all three vendor roles.

---

## Out-of-order arrival is the ordinary case

The gateway polls on a schedule, so a sweep can read "delivered" before it has
ever read "shipped". That is not an edge case to defend against; it is what
normally happens when a parcel moves faster than the poll interval.

`tracking.Status.Progress()` orders the statuses once, in the domain, so "a
status never moves backwards" is stated in one place rather than re-derived by
every adapter. `shipmentstate.Decide` then gives one of three answers:

- **advance** — push the details and the status;
- **details changed but the status did not** — push the details, leave the
  status alone, so a corrected waybill cannot roll a delivered order back;
- **identical** — push nothing, which is what makes a sweep safe to repeat.

**Returned and cancelled rank above delivered.** An RTO reported after a
delivery notice is the vendor correcting itself, and the vendor owns dispatch:
a parcel that came back is later news than a parcel that was said to arrive.

A blank incoming waybill is not a change. A sweep that returns a shipment
without its tracking number must not erase the one already sent.

## The decision is made in the workflow, not the sink

It needs durable state, and a platform adapter must not own that.

That forced a change to the role contract: `vendor.ShipmentSink.PushShipment`
now takes a `ShipmentUpdate` rather than a bare shipment. The update carries
two things that are not properties of the parcel — the origin's own carrier
identifier, which only configuration can supply, and whether the status
advances, which is a judgement against what was pushed before.

This is the same shape as the stock path, where the workflow translates SKUs
and the sink receives levels already in Gluzo's terms.

## The status enumeration is not guessed

`current_shipment_status_id` is a number EasyEcom assigns and does not publish
(open item B7). Rather than guess:

- `AssignShipmentDetails` is sent regardless, so the customer gets a **tracking
  number** even while the status cannot be set;
- the status update is skipped with a warning naming the status.

Guessing would put an order into a state nobody asked for, with no error to
notice. Skipping degrades a feature; a wrong number corrupts a record.

The same reasoning skips the status update when a dispatch carries no waybill:
the update is addressed by waybill, and sending one without would be a guess
about which parcel moved.

## One order's failure does not cost the others

A sweep covers many orders. One order whose carrier the platform rejects must
not lose every other customer their tracking, so failures are counted and the
rest are pushed — but the action still fails at the end, so the run is visibly
unclean and is retried rather than quietly half-done.

## One job, not two

Stock needs a periodic full push because it compares against a record that can
drift. Dispatch reads a **period** bounded by its watermark, so a missed run is
closed by widening the next window. `MaxWindow` caps how much one run covers so
a backlog drains in chunks.

---

## Exit criteria

- [x] **A delivered-before-shipped sequence leaves the order delivered** — and
      the late notice reaches the platform not at all.
- [x] **A redelivered shipment event is a no-op.**
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean.

Covered at three levels: the decision logic in `shipmentstate`, the workflow
end to end against stubs, and the store against the embedded PostgreSQL.

---

## Unverified

| Item | If wrong |
|---|---|
| `AssignShipmentDetails` and `updateTrackingStatus` **paths** | Fails loudly on the first call |
| `current_shipment_status_id` values (B7) | Not guessed; status simply not pushed |
| `companyCarrierId` values (B8) | Operator configuration; an unmapped carrier fails the run and names it |
| The `delivery_date` format | Fails loudly or is ignored by the platform |

The Postman collection recorded both operations and their bodies but not their
paths — the same gap as the bulk inventory endpoint in Phase 3. Added to
[blockers.md](../blockers.md) as B19.

---

## Follow-ups

1. **`history_scans` is never populated.** Vinculum's dispatch record has no
   scan history, so there is nothing to fill it with. If BCPL expose carrier
   scans later it is additive.
2. **`shipment_state` has no retention**, like `inventory_state`. Fold both
   into the existing retention job.
3. **Multi-package orders are modelled but untested against a real payload.**
   `PackageCode` is carried through and keyed on; Vinculum's specification
   does not show an order shipping in several parcels, so whether it reports
   them separately is unconfirmed.

---

## Next

Phase 7 — reconciliation, and the last one. It is the only remaining phase
that is **live-testable today**: it reads the gateway's own state rather than
anyone's API.
