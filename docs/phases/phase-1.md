# Phase 1 — Vinculum client and read paths

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 29 September 2026.

**Goal:** the gateway can read BCPL's stock and dispatch records. Read-only,
so nothing on BCPL's side can be broken by it.

---

## What changed

### Added: `app/integrations/vinculum`

```text
config.go                     credentials, default location, sellable bucket
client.go                     transport, static headers, envelope handling
endpoint_inventory.go         GetWhInventory + paged FetchAllWhInventory
endpoint_shipment.go          ShipmentDetail + paged FetchAllShipmentDetail
dto/common.go                 envelope, Flex scalars, date handling
dto/inventory/                get_wh_inventory.go
dto/shipment/                 shipment_detail.go
mapper/inventory.go           bucket filter, A1, inventory.Level
mapper/shipment.go            tracking.Shipment, status normalisation
```

Nothing calls it yet. The vendor roles are Phases 4 and 5; this phase builds
what they will call.

### No token machinery

Vinculum authenticates with two static headers and issues no token, so the
client has no token source, no cache and no retry-on-401 loop. The EasyEcom
client has all three because EasyEcom's JWT expires; copying that shape here
would have produced a refresh path that can never fire.

A 401 from Vinculum means the credentials are wrong. Asking again with the
same headers would only be told the same thing, so it is reported at once.

### The envelope is checked separately from the HTTP status

Vinculum reports a business rejection as **HTTP 200 with a non-zero
`responseCode`**. A caller that checked only the status would read a rejection
as a successful page carrying no rows — and a stock sweep that reads "no rows"
as "no stock" zeroes a catalogue.

Envelope errors are non-retryable: they describe a decision about the request,
not a transient condition. Transient conditions arrive as 5xx and the HTTP
client retries them.

### Paging has a ceiling

`hasMore` is the vendor's claim, not ours. A vendor defect that leaves it true
forever would spin a sweep indefinitely, so `FetchAllWhInventory` and
`FetchAllShipmentDetail` stop at 500 and 200 pages and report it. An empty
page also ends the loop regardless of `hasMore`.

A stock sweep that ends early returns an **error**, not a short result. Half a
catalogue taken as the whole is worse than no update at all.

### A1 is one expression, on purpose

`mapper.SellableQuantity(qty, committed)` returns `qty - committed`, clamped
at zero. That is assumption A1 of the plan, and BCPL have indicated
`committedQty` may itself be the net figure, in which case the body becomes
`return committed`.

One function, one caller, one line to change. Getting it wrong is systematic
oversell in one direction and a catalogue reading as out of stock in the
other, so it must be observed, not assumed — read both values for a SKU,
place one order for one unit, read again.

### Mappers take the clock as an argument

`ToDomainStock` and `ToDomainShipments` are passed the observation time
instead of calling `time.Now`. That keeps them pure, per the constraint
carried over from the architecture, and means a mapper test needs nothing but
a payload.

Both drop bad rows rather than failing the batch, and return a summary
(`Filtered`, `Skipped`, `NotShipped`) so the drops can be logged instead of
vanishing. An order with no `shipDetail` block is **not** an error: an order
waiting in BCPL's warehouse is what the sweep expects to find most of the
time.

### Changed: `tracking.Shipment` gains `InvoiceNumber` and `SellerGSTIN`

Under dropship BCPL invoices the customer under its own GST registration, and
`shipmentDetail` returns `sellerGstNo`. The origin platform needs both to show
the correct seller on the customer's invoice.

`delivereddate`, `deliveryNumber` and `ewbNo` are decoded but deliberately not
given domain fields: no consumer needs them yet, and Phase 6 carries them in
the workflow state.

### Changed: `config.Vinculum` and the `VINCULUM_*` block

Six variables, all optional — an intake-only instance needs none of them, and
the client validates what it needs when it is built.

### Revisited: the two EasyEcom `TODO(VERIFY)` markers

Plan task, now closed with a negative result.

`GetInventoryDetails` and `GetTrackingDetails` remain **unverified**. The
EasyEcom Postman collection read on 29 September 2026 documents the *write*
endpoints — Bulk Inventory Update, AssignShipmentDetails, updateTrackingStatus
— not these two reads. The markers now say so, and say that nothing calls
them: under dropship the vendor owns stock and dispatch, so both flow into
EasyEcom rather than out of it.

Phases 3 and 6 decide whether they are reworked into the sink adapter or
deleted. Until then they stay unused rather than being confirmed on a guess.

---

## Exit criteria

- [x] Table-driven mapper tests against recorded payloads
- [x] Client tests against an `httptest` server: success, envelope error,
      paging, timeout
- [x] No live calls in tests
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean

The payloads under `mapper/testdata/` are **constructed from the published
specification, not captured from BCPL** — no test credentials have been issued
(open item 5). Each file says so in its own `_comment` field. They cover the
variance the code exists to absorb: quantities as numbers and as strings, a
non-sellable bucket, a row with no SKU, over-commitment, an order that has not
shipped, a record identified only by Vinculum's own order number, and three
date layouts.

---

## Still blocked

Live testing needs BCPL open items 2 and 5 (test credentials, seeded SKUs).
The unit work did not need them and did not wait.

Unverified and marked in code, listed in
[integrations.md](../integrations.md): the `responseCode` success value, the
request date format, the dispatch status values, `reqType` / `filterBy` /
`fulfillmentLocation` / `status[]`, the sellable bucket, and A1.

---

## Next

Phase 2 — the scheduler. Periodic work enters the existing queue, single-firing
across replicas, with a watermark so a missed tick does not lose work. Blocked
only by Phase 0, which is done.
