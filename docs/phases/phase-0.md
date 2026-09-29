# Phase 0 — Remove Dabur, add the vendor abstraction

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 29 September 2026.

**Goal:** the engine runs on a role-based vendor abstraction, with the Dabur
pipeline removed and no vendor implemented yet.

---

## Why

`ordersync` defined `Source` (FetchOrder, FetchInventory, FetchTracking) and
`Destination` (PrepareOrder, SubmitOrder, UpdateInventory). That shape
encodes an assumption — Gluzo owns the stock and pushes it outward — which is
true for Dabur and false for a dropship vendor. BCPL owns its stock and its
dispatch, so both flow inward.

Keeping the old contracts would have produced adapters whose method names lie
about which way data moves.

---

## What changed

### Added: `app/vendor`

Six role interfaces. Direction is fixed by the role.

| Side | Role | Methods |
| --- | --- | --- |
| Vendor | `OrderReceiver` | `PrepareOrder`, `SubmitOrder` |
| Vendor | `StockProvider` | `FetchStock` |
| Vendor | `FulfilmentProvider` | `FetchShipments` |
| Origin | `Origin` | `FetchOrder` |
| Origin | `StockSink` | `PushStock` |
| Origin | `ShipmentSink` | `PushShipment` |

Plus `Route`, `OrderAck`, `StockCursor`, `StockPage`, `Window`,
`ShipmentPage`, `StockResult`, and `Registry`.

`Registry.Register` type-asserts an adapter against each role and files it
under the ones it satisfies. Two deliberate behaviours:

- An adapter implementing no role is rejected at registration. Such a vendor
  would otherwise resolve at routing time and fail at the first action.
- Asking for a role a registered vendor does not implement gives a different
  error from asking for an unknown vendor. Those are different mistakes and
  should not be diagnosed the same way.

`app/vendor` imports only `app/domain/*` and `app/event`. It does not import
the workflow engine, which is why `Route` is its own type rather than
`workflow.RouteInfo`.

### Changed: `ORDER_SYNC` is four actions, not seven

```text
RESOLVE_INTEGRATION → FETCH_ORDER → MAP_ORDER → SUBMIT_VENDOR_ORDER
```

`FETCH_INVENTORY`, `UPDATE_INVENTORY` and `FETCH_TRACKING` were removed.

**This is a correction to the plan**, which had scheduled their removal for
Phase 5. That was wrong: `UPDATE_INVENTORY` was backed by
`Destination.UpdateInventory`, which dies with Dabur, and under dropship
EasyEcom holds no tracking until the gateway pushes it. Neither step had
anything left to call. They return as `STOCK_SYNC` (Phase 4) and
`SHIPMENT_SYNC` (Phase 6), on their own schedules.

Coupling them to an order would have meant a customer order's success
depending on a stock sweep, and stock going stale for any SKU that happened
not to sell.

Renamed state keys: `destination_order_request` → `vendor_order_request`,
`destination_order_created` → `vendor_order_created`.

### Changed: `workflow.RouteInfo` left alone, converted at the boundary

`ordersync` converts `workflow.RouteInfo` into `vendor.Route` in one place.

`RouteInfo` still says `SourcePlatform` / `DestinationPlatform` /
`DestinationReference`. Renaming it to origin/vendor terms would touch
persisted workflow state, the admin viewer and the execution log in the same
commit as a contract change. Deferred deliberately; see Follow-ups.

### Removed

```text
app/integrations/dabur/          whole package tree
config.Dabur, the DABUR_* block, the DABUR_TIMEOUT duration check
the Dabur client and destination wiring in cmd/server/main.go
the fake Uniware HTTP server in tests/e2e_test.go
the DABUR_* section of .env.example
```

`.env.example` gains a commented `VINCULUM_*` block as a placeholder.

### Changed: `buildWorker` tolerates having no vendor

Phase 0 leaves the gateway with Dabur gone and Vinculum not yet written, so
the registry is empty. Rather than fail start-up, `buildWorker` logs a
warning and returns a worker with `ORDER_SYNC` unregistered.

This is a real state, not a placeholder, and the binary should be honest
about it rather than crash-loop.

### Changed: e2e test uses an in-process stub vendor

The fake Uniware HTTP server is replaced by `stubVendor`, which implements
all three vendor roles so the registry's capability discovery is exercised,
and fails submission on demand.

This trades transport-level coverage for the ability to run Phase 0 at all.
Phase 5 restores it with a fake Vinculum HTTP server behind the real adapter.

---

### Rewritten: the docs that described Dabur as the live destination

`README.md`, `docs/architecture.md`, `docs/integrations.md`,
`docs/authentication.md`, `docs/deployment.md`, `docs/routing.md`,
`docs/logging.md` and `docs/webhook-flow.md` named Dabur, Uniware or the
`DABUR_*` variables as current fact. All rewritten.

`docs/integrations.md` loses its Uniware section and gains a **Vendor side**
section: the six roles, the registry, the contract rules every adapter must
honour, and the two route references. It says plainly that no vendor adapter
exists yet.

`docs/architecture.md` gains **ADR-017**, which supersedes ADR-013 and
records why the source/destination split was replaced. ADR-013 is marked
superseded rather than deleted; it explains decisions that still hold.

Test fixtures and log examples used `easyecom-dabur`, `dabur`, `DABUR-DEL`,
`dabur_request` and Dabur product names as sample data. Renamed to
`easyecom-vinculum`, `vinculum`, `DEL`, `vendor_request` and neutral product
names, so a grep for the old partner returns only the documents that record
its removal.

---

## Exit criteria

- [x] `gofmt -l .` clean
- [x] `go vet ./...` clean
- [x] `go test ./...` passing, including the e2e scenario
- [x] no Dabur code, configuration, wiring or test fixture anywhere in the
      tree

The name survives in three places, deliberately: this note, the
[plan](../vinculum-integration-plan.md), and the original briefs
(`implementation.md`, `git-guide.md`). Those record history; rewriting them
would falsify it.

---

## Follow-ups

1. **Rename `workflow.RouteInfo` fields** to `OriginPlatform` /
   `VendorPlatform` / `VendorReference`, and add `OriginReference`. Touches
   persisted state, the admin viewer and the log. Do it as its own commit.
2. **`Route.OriginReference` is unset.** Routing has no column for it yet.
   Phase 3 adds it, with EasyEcom's per-location credentials.
3. **`easyecom.Source` still carries `FetchInventory` and `FetchTracking`.**
   Unused by `ORDER_SYNC` now. Phase 3 and Phase 6 decide whether they are
   reworked into the sink adapter or deleted.
4. **`auth.PlatformTypeDestination`** still uses destination wording.
   Cosmetic; fold into follow-up 1.

---

## Next

Phase 1 — Vinculum client and read paths. Read-only against BCPL, so nothing
can be broken on their side. Blocked only for live testing (open items 2 and
5); the unit work can start now.
