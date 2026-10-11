\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

-- One durable history owns all API keys; the ledger contains only internal IDs.
DO $schema$
DECLARE
    actual jsonb;
    wanted jsonb := '{"separate_limit_history":false,"ledger_operation_id":false,"ledger_idempotency_key":false,"shared_idempotency_key":true}';
BEGIN
    SELECT jsonb_build_object(
        'separate_limit_history', to_regclass('spend_limit_operations') IS NOT NULL,
        'ledger_operation_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='credit_entries' AND column_name='operation_id'),
        'ledger_idempotency_key', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='credit_entries' AND column_name='idempotency_key'),
        'shared_idempotency_key', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='api_idempotency_operations' AND column_name='idempotency_key')
    ) INTO actual;

    IF actual IS DISTINCT FROM wanted THEN
        RAISE EXCEPTION 'Unified command schema: actual %, expected %', actual, wanted;
    END IF;
END;
$schema$;

INSERT INTO api_idempotency_operations VALUES
    ('credits/sql-unified','shared','{}','{}','2026-10-01T00:00:00Z'),
    ('spend-limit/sql-unified','shared','{"limit_cents":null}','{"limit_cents":null}','2026-10-01T00:00:00Z');

DO $scenarios$
DECLARE
    scenario jsonb;
    actual_state text;
BEGIN
    FOR scenario IN SELECT value FROM jsonb_array_elements($inputs$[
        {"name":"credit key retains uniqueness","input_sql":"INSERT INTO api_idempotency_operations VALUES ('credits/sql-unified','shared','{}','{}','2026-10-01T00:00:00Z')","want_sqlstate":"23505"},
        {"name":"another customer may reuse a key","input_sql":"INSERT INTO api_idempotency_operations VALUES ('credits/sql-other','shared','{}','{}','2026-10-01T00:00:00Z')","want_sqlstate":"00000"},
        {"name":"limit key remains required","input_sql":"INSERT INTO api_idempotency_operations VALUES ('spend-limit/sql-unified',NULL,'{}','{}','2026-10-01T00:00:00Z')","want_sqlstate":"23502"},
        {"name":"command history remains immutable","input_sql":"UPDATE api_idempotency_operations SET response_payload='{}' WHERE operation_scope='spend-limit/sql-unified'","want_sqlstate":"23514"},
        {"name":"credit command cannot be deleted","input_sql":"DELETE FROM api_idempotency_operations WHERE operation_scope='credits/sql-unified'","want_sqlstate":"23514"},
        {"name":"ledger operation_id is unavailable","input_sql":"SELECT operation_id FROM credit_entries","want_sqlstate":"42703"},
        {"name":"separate limit history is unavailable","input_sql":"SELECT * FROM spend_limit_operations","want_sqlstate":"42P01"}
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
                scenario->>'name', actual_state, scenario->>'want_sqlstate', scenario->>'input_sql';
        END IF;
        RAISE NOTICE 'Passed: % (SQLSTATE %)', scenario->>'name', actual_state;
    END LOOP;
END;
$scenarios$;
ROLLBACK;
