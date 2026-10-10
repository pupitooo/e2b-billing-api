\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

-- Reconstruct the pre-011 column in this rolled-back transaction. Existing
-- histories and triggers remain present, as they do on an upgraded volume.
ALTER TABLE spend_limit_operations RENAME COLUMN idempotency_key TO operation_id;
INSERT INTO spend_limit_operations (customer_id, operation_id, limit_cents, recorded_at) VALUES
    ('acme', 'sql-key-migration-finite', 1500, '2026-10-01T08:00:00+08:00'),
    ('acme', 'sql-key-migration-unlimited', NULL, '2026-10-02T00:00:00Z');

\ir /migrations/011_spend_limit_idempotency_key.sql

DO $migration$
DECLARE
    actual jsonb;
    wanted jsonb := '[
        {"customer_id":"acme","idempotency_key":"sql-key-migration-finite","limit_cents":1500,"recorded_at":"2026-10-01T00:00:00Z"},
        {"customer_id":"acme","idempotency_key":"sql-key-migration-unlimited","limit_cents":null,"recorded_at":"2026-10-02T00:00:00Z"}
    ]';
BEGIN
    SELECT jsonb_agg(jsonb_build_object(
        'customer_id', customer_id,
        'idempotency_key', idempotency_key,
        'limit_cents', limit_cents,
        'recorded_at', to_char(recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
    ) ORDER BY idempotency_key)
    INTO actual
    FROM spend_limit_operations WHERE idempotency_key LIKE 'sql-key-migration-%';

    IF actual IS DISTINCT FROM wanted THEN
        RAISE EXCEPTION 'Migration 011 history: actual %, expected %', actual, wanted;
    END IF;
    RAISE NOTICE 'Passed: migration preserves keys, nullable limits, and UTC timestamps';
END;
$migration$;

DO $scenarios$
DECLARE
    scenario jsonb;
    actual_state text;
BEGIN
    FOR scenario IN SELECT value FROM jsonb_array_elements($inputs$[
        {"name":"renamed key retains uniqueness","input_sql":"INSERT INTO spend_limit_operations VALUES ('acme','sql-key-migration-finite',2000,'2026-10-03T00:00:00Z')","want_sqlstate":"23505"},
        {"name":"new key remains usable","input_sql":"INSERT INTO spend_limit_operations VALUES ('acme','sql-key-migration-new',2000,'2026-10-03T00:00:00Z')","want_sqlstate":"00000"},
        {"name":"renamed key is required","input_sql":"INSERT INTO spend_limit_operations VALUES ('acme',NULL,2000,'2026-10-03T00:00:00Z')","want_sqlstate":"23502"},
        {"name":"history remains immutable","input_sql":"UPDATE spend_limit_operations SET idempotency_key='changed' WHERE idempotency_key='sql-key-migration-finite'","want_sqlstate":"23514"},
        {"name":"history cannot be deleted","input_sql":"DELETE FROM spend_limit_operations WHERE idempotency_key='sql-key-migration-finite'","want_sqlstate":"23514"},
        {"name":"old column name is unavailable","input_sql":"SELECT operation_id FROM spend_limit_operations","want_sqlstate":"42703"}
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
