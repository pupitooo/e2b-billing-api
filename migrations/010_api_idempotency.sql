-- Successful API commands retain their input and result indefinitely. Their
-- insertion shares the financial transaction, so retries survive process restarts.
CREATE TABLE api_idempotency_operations (
    operation_scope text NOT NULL CHECK (length(operation_scope) > 0),
    idempotency_key text NOT NULL CHECK (
        octet_length(idempotency_key) BETWEEN 1 AND 256
        AND idempotency_key COLLATE "C" ~ '^[!-~]+$'
    ),
    request_payload jsonb NOT NULL CHECK (jsonb_typeof(request_payload) = 'object'),
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL CHECK (isfinite(created_at)),
    PRIMARY KEY (operation_scope, idempotency_key)
);

CREATE TRIGGER api_idempotency_operations_append_only
    BEFORE UPDATE OR DELETE ON api_idempotency_operations
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
