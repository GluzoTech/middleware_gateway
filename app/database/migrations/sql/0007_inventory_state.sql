-- The quantity last pushed to the origin platform, per SKU.
--
-- A stock sweep reads the vendor's whole catalogue every time, but most SKUs
-- have not moved. Pushing all of them would spend the origin platform's rate
-- limit on writes that change nothing, so a sweep sends only what differs
-- from this table.
--
-- Keyed by origin_reference as well as integration and SKU: the same SKU
-- pushed to two origin locations has two independent last-pushed values, and
-- collapsing them would make a write to one location look like it had
-- already happened to the other.
CREATE TABLE inventory_state (
    integration_id   UUID        NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    origin_reference TEXT        NOT NULL DEFAULT '',
    gluzo_sku        TEXT        NOT NULL,
    quantity         INTEGER     NOT NULL,
    pushed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, origin_reference, gluzo_sku)
);

COMMENT ON TABLE inventory_state IS
    'What the gateway believes the origin platform holds. It is a cache of our own writes, never a source of truth: a nightly full push ignores it entirely, which is what repairs drift after a run that failed between writing the platform and recording it here.';

COMMENT ON COLUMN inventory_state.quantity IS
    'The quantity last sent, after the safety buffer and the platform cap were applied. Storing the sent figure rather than the vendor figure is what makes the comparison meaningful: two different vendor quantities that clamp to the same sent value are genuinely the same write.';
