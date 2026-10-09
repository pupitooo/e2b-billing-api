-- Retain command results so replay of an older change cannot undo a newer limit.
CREATE TABLE spend_limit_operations (
    customer_id text NOT NULL REFERENCES customers (customer_id),
    operation_id text NOT NULL,
    limit_cents bigint CHECK (limit_cents >= 0),
    recorded_at timestamptz NOT NULL CHECK (isfinite(recorded_at)),
    PRIMARY KEY (customer_id, operation_id)
);
CREATE TRIGGER spend_limit_operations_append_only
    BEFORE UPDATE OR DELETE ON spend_limit_operations
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
