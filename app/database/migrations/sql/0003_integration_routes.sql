-- Routes map an attribute of an inbound event (for example an EasyEcom
-- warehouse id) to the integration that should process it. Nothing about a
-- warehouse or marketplace is hard-coded; operators manage rows with
-- gatewayctl.
CREATE TABLE integration_routes (
    id                    UUID        PRIMARY KEY,
    integration_id        UUID        NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    route_type            TEXT        NOT NULL,
    route_value           TEXT        NOT NULL,
    destination_reference TEXT,
    status                TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (integration_id, route_type, route_value)
);

COMMENT ON COLUMN integration_routes.destination_reference IS
    'Destination-side identifier for this route, e.g. the Uniware facility code receiving orders from this warehouse';

CREATE INDEX integration_routes_lookup_idx
    ON integration_routes (route_type, route_value) WHERE status = 'active';
