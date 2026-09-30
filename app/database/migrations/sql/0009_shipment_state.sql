-- The furthest dispatch state the gateway has pushed for each package.
--
-- A shipment's status must never move backwards, and out-of-order arrival is
-- ordinary under dropship: the gateway polls the vendor on a schedule, and a
-- sweep can read "delivered" before it ever reads "shipped". Deciding whether
-- an incoming notice is an advance needs a record of what was already sent.
--
-- Kept here rather than read back from the origin platform because the
-- origin's own tracking read is unverified and, under dropship, holds only
-- what this gateway put there. Asking it would be asking ourselves through a
-- more expensive route.
CREATE TABLE shipment_state (
    integration_id    UUID        NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    order_external_id TEXT        NOT NULL,
    package_code      TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL,
    progress          INTEGER     NOT NULL,
    tracking_number   TEXT        NOT NULL DEFAULT '',
    carrier           TEXT        NOT NULL DEFAULT '',
    pushed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, order_external_id, package_code)
);

COMMENT ON COLUMN shipment_state.progress IS
    'How far the dispatch had got, from tracking.Status.Progress(). Stored alongside the label so a comparison does not depend on the reading code agreeing with the row about what a status name means.';

COMMENT ON COLUMN shipment_state.package_code IS
    'Identifies the package when an order ships in several. Empty is a legitimate value: most orders ship as one.';
