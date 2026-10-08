CREATE TABLE usage_inbox (
    source text NOT NULL,
    event_id text NOT NULL,
    schema_version integer NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    customer_id text NOT NULL,
    sandbox_id text NOT NULL,
    metric text NOT NULL,
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL,
    units bigint NOT NULL CHECK (units >= 0),
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    processing_error text,
    PRIMARY KEY (source, event_id),
    CHECK (period_end > period_start),
    CHECK (processed_at IS NULL OR processing_error IS NULL)
);

-- Supports polling unprocessed events without processing errors.
CREATE INDEX usage_inbox_pending_idx
    ON usage_inbox (received_at, source, event_id)
    WHERE processed_at IS NULL AND processing_error IS NULL;
