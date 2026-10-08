CREATE TABLE usage_inbox (
    source text NOT NULL CHECK (btrim(source) <> ''),
    event_id text NOT NULL CHECK (btrim(event_id) <> ''),
    schema_version integer NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    customer_id text NOT NULL CHECK (btrim(customer_id) <> ''),
    sandbox_id text NOT NULL CHECK (btrim(sandbox_id) <> ''),
    metric text NOT NULL CHECK (btrim(metric) <> ''),
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL,
    units bigint NOT NULL CHECK (units >= 0),
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    processing_error text CHECK (btrim(processing_error) <> ''),
    PRIMARY KEY (source, event_id),
    CHECK (period_end > period_start),
    CHECK (processed_at IS NULL OR processing_error IS NULL)
);

-- Pending, error-free input is the future worker's polling path.
CREATE INDEX usage_inbox_pending_idx
    ON usage_inbox (received_at, source, event_id)
    WHERE processed_at IS NULL AND processing_error IS NULL;
