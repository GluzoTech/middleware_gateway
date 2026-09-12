-- Platforms are the external systems the gateway exchanges data with.
-- A platform that sends webhooks authenticates with a platform API key;
-- only the SHA-256 hex digest of that key is stored.
CREATE TABLE platforms (
    id           UUID        PRIMARY KEY,
    name         TEXT        NOT NULL UNIQUE,
    type         TEXT        NOT NULL CHECK (type IN ('source', 'destination', 'both')),
    api_key_hash TEXT        UNIQUE,
    status       TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN platforms.api_key_hash IS
    'SHA-256 hex digest of the platform API key; the plaintext is never stored';

-- An integration is one configured pipeline from a source platform to a
-- destination platform (for example EasyEcom -> Dabur).
CREATE TABLE integrations (
    id                      UUID        PRIMARY KEY,
    name                    TEXT        NOT NULL UNIQUE,
    source_platform_id      UUID        NOT NULL REFERENCES platforms (id),
    destination_platform_id UUID        NOT NULL REFERENCES platforms (id),
    status                  TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Access tokens authorise a caller to submit events for one integration.
-- Only the SHA-256 hex digest is stored; the plaintext is shown once when
-- the token is issued.
CREATE TABLE integration_access_tokens (
    id             UUID        PRIMARY KEY,
    integration_id UUID        NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    name           TEXT        NOT NULL,
    token_hash     TEXT        NOT NULL UNIQUE,
    expires_at     TIMESTAMPTZ,
    revoked_at     TIMESTAMPTZ,
    last_used_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX integration_access_tokens_integration_id_idx
    ON integration_access_tokens (integration_id);
