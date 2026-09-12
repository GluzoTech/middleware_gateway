-- One row per inbound event. The primary key makes concurrent duplicate
-- webhooks race on the insert, so exactly one of them starts a workflow.
CREATE TABLE idempotency_records (
    idempotency_key TEXT        PRIMARY KEY,
    correlation_id  TEXT        NOT NULL,
    platform        TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'accepted'
                                CHECK (status IN ('accepted', 'processing', 'completed', 'failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at    TIMESTAMPTZ
);

CREATE INDEX idempotency_records_created_at_idx ON idempotency_records (created_at);
CREATE INDEX idempotency_records_correlation_id_idx ON idempotency_records (correlation_id);
