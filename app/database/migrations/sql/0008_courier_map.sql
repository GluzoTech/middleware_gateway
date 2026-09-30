-- Vinculum names a carrier with a free-text `transporter` string; EasyEcom
-- identifies one with a numeric companyCarrierId it assigns when the carrier
-- is registered on the account. Neither knows the other's, so the
-- correspondence is configuration.
--
-- Scoped to an integration for the same reason the SKU map is: two vendors
-- may both ship by "Delhivery" while their accounts carry different carrier
-- ids, and a mapping must never be used by a pipeline it was not configured
-- for.
CREATE TABLE courier_map (
    id                 UUID        PRIMARY KEY,
    integration_id     UUID        NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    transporter        TEXT        NOT NULL,
    company_carrier_id TEXT        NOT NULL,
    courier_name       TEXT        NOT NULL DEFAULT '',
    status             TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (integration_id, transporter)
);

COMMENT ON COLUMN courier_map.transporter IS
    'The carrier name exactly as the vendor reports it. Matched case-insensitively, because carriers arrive spelled inconsistently and a mapping that failed on case would report a configuration error that is not one.';

COMMENT ON COLUMN courier_map.company_carrier_id IS
    'EasyEcom''s own identifier for the carrier, obtained once the carrier is registered on the account. Stored as text because it is an opaque identifier, not a number to do arithmetic on.';

COMMENT ON COLUMN courier_map.courier_name IS
    'The name to present to EasyEcom, where it differs from the vendor''s spelling. Empty sends the vendor''s.';
