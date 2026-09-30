# Routing

Routing answers "which integration, and which destination location, should
this event go to?" without a single warehouse or marketplace identifier in
the code.

## Model

```text
event.RoutingKey  (warehouse_id=12345)
        │
        ▼
integration_routes  WHERE integration_id = <token's integration>
                      AND route_type = 'warehouse_id'
                      AND route_value = '12345'
                      AND status = 'active'
        │
        ▼
Resolution {
  IntegrationName:      easyecom-vinculum
  SourcePlatform:       easyecom
  DestinationPlatform:  vinculum
  Route.DestinationReference: DEL                 <- the vendor-side location code
  Route.OriginReference:      bcpl_location_key   <- the EasyEcom location stock is written under
}
```

- The **route type** is whatever attribute the platform parser puts on the
  event. EasyEcom order events carry `warehouse_id`; `marketplace_id`,
  `channel` and `company` are reserved for parsers that emit them.
- Resolution is **scoped to the authenticated integration**. A token can
  only steer events into its own pipeline, and two integrations can map the
  same warehouse independently.
- `destination_reference` carries the vendor-side identifier for the route,
  so "EasyEcom warehouse 12345 is fulfilled from vendor location DEL" is
  configuration, not code.
- `origin_reference` carries the origin-side one: the EasyEcom `location_key`
  a stock push authenticates for. The two are separate because they name
  different ends of the same pipeline, and because the origin one is a
  security boundary — EasyEcom scopes its JWT to a location, so
  authenticating for BCPL's makes the gateway structurally incapable of
  writing quantities into Gluzo's own warehouse.
- Disabling a route pauses one warehouse; disabling the integration pauses
  the whole pipeline. Neither deletes configuration.

## Outcome in the workflow

`RESOLVE_INTEGRATION` is the first action of the order workflow. A key with
no active route ends the workflow as `SKIPPED` rather than `FAILED`: the
event was well-formed, it simply is not configured for synchronisation. The
skip is recorded in the execution log with the routing key so operators can
spot warehouses that need a route.

## Managing routes

```bash
go run ./cmd/gatewayctl route add --integration easyecom-vinculum --type warehouse_id --value 12345 --destination-ref DEL --origin-ref bcpl_location_key
go run ./cmd/gatewayctl route list --integration easyecom-vinculum
go run ./cmd/gatewayctl route set-status --id <route id> --status disabled
go run ./cmd/gatewayctl route remove --id <route id>
```

## Verification

- `app/routing` unit tests cover the in-memory resolver: unknown key, other
  integration, wrong type, disabled route.
- `tests/routing_store_test.go` runs the real SQL: add, duplicate, resolve,
  scoping to the integration, disabled route, disabled integration, list and
  remove.
