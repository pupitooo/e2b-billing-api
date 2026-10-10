\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

-- Transaction-owned fixtures exercise the same checks in both session zones.
INSERT INTO api_idempotency_operations VALUES
    ('sql-idempotency','original','{}','{"ID":"sql-generated-price"}','2026-11-01T00:00:00Z');

DO $scenarios$
DECLARE
    scenario jsonb;
    actual_state text;
BEGIN
    FOR scenario IN SELECT value FROM jsonb_array_elements($inputs$[
    {
        "name": "same scoped key is unique",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','original','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23505"
    },
    {
        "name": "another scope may reuse the key",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-other-scope','original','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "00000"
    },
    {
        "name": "case-sensitive keys are distinct",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','Original','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "00000"
    },
    {
        "name": "empty key is rejected",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "space is rejected",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','a b','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "tab is rejected",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency',E'a\\tb','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "Unicode is rejected",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','ž','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "exact key maximum is accepted",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency',repeat('a',256),'{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "00000"
    },
    {
        "name": "above key maximum is rejected",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency',repeat('a',257),'{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "empty scope is rejected",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('','scope-test','{}','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "request must be an object",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','array-input','[]','{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "response must be an object",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','array-output','{}','[]','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23514"
    },
    {
        "name": "request cannot be missing",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','null-input',NULL,'{}','2026-11-01T00:00:00Z')",
        "want_sqlstate": "23502"
    },
    {
        "name": "creation time must be finite",
        "input_sql": "INSERT INTO api_idempotency_operations VALUES ('sql-idempotency','infinity','{}','{}','infinity')",
        "want_sqlstate": "23514"
    },
    {
        "name": "result history cannot change",
        "input_sql": "UPDATE api_idempotency_operations SET response_payload='{\"ID\":\"different\"}' WHERE operation_scope='sql-idempotency' AND idempotency_key='original'",
        "want_sqlstate": "23514"
    },
    {
        "name": "key history cannot be deleted",
        "input_sql": "DELETE FROM api_idempotency_operations WHERE operation_scope='sql-idempotency' AND idempotency_key='original'",
        "want_sqlstate": "23514"
    }
]$inputs$::jsonb)
    LOOP
        actual_state := '00000';
        BEGIN
            EXECUTE scenario->>'input_sql';
        EXCEPTION WHEN OTHERS THEN
            GET STACKED DIAGNOSTICS actual_state = RETURNED_SQLSTATE;
        END;
        IF actual_state <> scenario->>'want_sqlstate' THEN
            RAISE EXCEPTION '%: SQLSTATE %, expected %, input %',
                scenario->>'name',actual_state,scenario->>'want_sqlstate',scenario->>'input_sql';
        END IF;
        RAISE NOTICE 'Passed: % (SQLSTATE %)',scenario->>'name',actual_state;
    END LOOP;
END;
$scenarios$;
ROLLBACK;
