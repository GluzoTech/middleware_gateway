-- Gluzo sells BCPL's products under Gluzo's own SKUs; BCPL hold them under
-- theirs. Nothing in either system knows about the other's codes, so the
-- mapping is configuration.
--
-- Scoped to an integration, so two vendors may both use the item code
-- "HONEY-250" without colliding, and a mapping can never be used by a
-- pipeline it was not configured for.
--
-- Both directions are unique within an integration because both are used:
-- stock arrives under the vendor's code and is published under Gluzo's, and
-- an order is placed under Gluzo's code and sent under the vendor's.
CREATE TABLE sku_map (
    id             UUID        PRIMARY KEY,
    integration_id UUID        NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    gluzo_sku      TEXT        NOT NULL,
    vendor_sku     TEXT        NOT NULL,
    safety_buffer  INTEGER     NOT NULL DEFAULT 0 CHECK (safety_buffer >= 0),
    status         TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (integration_id, gluzo_sku),
    UNIQUE (integration_id, vendor_sku)
);

COMMENT ON COLUMN sku_map.safety_buffer IS
    'Quantity withheld from the storefront as protection against oversell. Subtracted from the vendor quantity before publishing, but never applied to zero: an item the vendor has none of is out of stock immediately, with no buffer to work through.';

COMMENT ON COLUMN sku_map.status IS
    'Disabling a mapping stops that SKU syncing without deleting the configuration. It does not make the SKU unmapped: an unmapped SKU is an error, a disabled one is a decision.';

CREATE INDEX sku_map_vendor_lookup_idx
    ON sku_map (integration_id, vendor_sku) WHERE status = 'active';
