-- A route needs a reference on both sides, not just the vendor's.
--
-- destination_reference names the vendor-side location (for Vinculum, the
-- three-character orderLocation). origin_reference names the origin-side one:
-- for EasyEcom, the location_key whose JWT is scoped to that location.
--
-- This is what keeps a stock push inside the vendor's own warehouse. EasyEcom
-- issues a JWT per location, so authenticating for this reference makes the
-- gateway structurally incapable of writing quantities into a location the
-- route does not name: a code defect cannot cause it.
ALTER TABLE integration_routes
    ADD COLUMN origin_reference TEXT;

COMMENT ON COLUMN integration_routes.origin_reference IS
    'Origin-side identifier for this route, e.g. the EasyEcom location_key whose JWT scopes stock writes to the vendor''s own warehouse. NULL uses the process default.';
