\set ON_ERROR_STOP on

\if :{?test_timezone}
\else
    \set test_timezone UTC
\endif

-- Fixtures are rolled back, leaving the developer database unchanged.
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

CREATE TEMP TABLE test_assertion_context (unused boolean) ON COMMIT DROP;

-- assert_result executes one scenario's read and compares the complete named
-- result with its explicit JSON expectation; NULL and missing rows also differ.
CREATE FUNCTION pg_temp.assert_result(scenario text, input_sql text, want_result jsonb)
RETURNS void LANGUAGE plpgsql AS $function$
DECLARE
    got_result jsonb;
BEGIN
    EXECUTE 'SELECT to_jsonb(result) FROM (' || input_sql || ') AS result' INTO STRICT got_result;
    IF got_result IS DISTINCT FROM want_result THEN
        RAISE EXCEPTION '%: result = %, want %; input SQL: %', scenario, got_result, want_result, input_sql;
    END IF;
END;
$function$;

-- assert_rejection executes the explicit invalid write in a subtransaction.
-- Its expected failure rolls back the write, keeping later scenarios isolated.
CREATE FUNCTION pg_temp.assert_rejection(scenario text, input_sql text, want_sqlstate text)
RETURNS void LANGUAGE plpgsql AS $function$
BEGIN
    BEGIN
        EXECUTE input_sql;
    EXCEPTION WHEN OTHERS THEN
        IF SQLSTATE = want_sqlstate THEN
            RETURN;
        END IF;
        RAISE EXCEPTION '%: SQLSTATE = %, want % (%); input SQL: %', scenario, SQLSTATE, want_sqlstate, SQLERRM, input_sql;
    END;
    RAISE EXCEPTION '%: write succeeded, want SQLSTATE %; input SQL: %', scenario, want_sqlstate, input_sql;
END;
$function$;

-- Input: one UTC month-boundary event, explicit version two, and receipt time.
INSERT INTO usage_inbox (
    source, event_id, schema_version, customer_id, sandbox_id, metric,
    period_start, period_end, units, received_at
) VALUES (
    'usage-inbox-schema-test', 'acme-cpu-001', 2, 'acme', 'sb-001', 'cpu_seconds',
    '2026-10-31T23:00:00Z', '2026-10-31T23:30:00Z', 100000000, '2026-10-31T23:30:05.123456Z'
);

SELECT pg_temp.assert_result(
    scenario => 'explicit inbox values and UTC instants survive both session time zones',
    input_sql => $input$
        SELECT schema_version, units,
            to_char(period_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS period_start,
            to_char(period_end AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS period_end,
            to_char(received_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS received_at,
            processed_at IS NULL AS pending, processing_error IS NULL AS error_free
        FROM usage_inbox WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'
    $input$,
    want_result => $want$
    {
        "schema_version": 2,
        "units": 100000000,
        "period_start": "2026-10-31T23:00:00.000000Z",
        "period_end": "2026-10-31T23:30:00.000000Z",
        "received_at": "2026-10-31T23:30:05.123456Z",
        "pending": true,
        "error_free": true
    }
    $want$::jsonb
);

SELECT pg_temp.assert_rejection(
    scenario => 'schema_version must be supplied by the application',
    input_sql => $input$
        INSERT INTO usage_inbox (
            source, event_id, customer_id, sandbox_id, metric, period_start, period_end, units, received_at
        ) VALUES ('usage-inbox-schema-test', 'missing-version', 'acme', 'sb-001', 'cpu_seconds',
            '2026-10-31T23:00:00Z', '2026-10-31T23:30:00Z', 100000000, '2026-10-31T23:30:05.123456Z')
    $input$,
    want_sqlstate => '23502'
);

SELECT pg_temp.assert_rejection(
    scenario => 'received_at must be supplied by the application',
    input_sql => $input$
        INSERT INTO usage_inbox (
            source, event_id, schema_version, customer_id, sandbox_id, metric, period_start, period_end, units
        ) VALUES ('usage-inbox-schema-test', 'missing-receipt', 2, 'acme', 'sb-001', 'cpu_seconds',
            '2026-10-31T23:00:00Z', '2026-10-31T23:30:00Z', 100000000)
    $input$,
    want_sqlstate => '23502'
);

SELECT pg_temp.assert_rejection(
    scenario => 'duplicate source and event identity cannot overwrite units',
    input_sql => $input$
        INSERT INTO usage_inbox (
            source, event_id, schema_version, customer_id, sandbox_id, metric,
            period_start, period_end, units, received_at
        ) VALUES ('usage-inbox-schema-test', 'acme-cpu-001', 2, 'acme', 'sb-001', 'cpu_seconds',
            '2026-10-31T23:00:00Z', '2026-10-31T23:30:00Z', 200000000, '2026-10-31T23:30:05.123456Z')
    $input$,
    want_sqlstate => '23505'
);

SELECT pg_temp.assert_result(
    scenario => 'duplicate rejection preserves the original measurement',
    input_sql => $input$
        SELECT units FROM usage_inbox WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'
    $input$,
    want_result => $want$
    {
        "units": 100000000
    }
    $want$::jsonb
);

-- Input: another source uses the same event ID and units above the int32 range.
INSERT INTO usage_inbox (
    source, event_id, schema_version, customer_id, sandbox_id, metric,
    period_start, period_end, units, received_at
) VALUES ('usage-inbox-schema-test-other', 'acme-cpu-001', 2, 'acme', 'sb-002', 'cpu_seconds',
    '2026-10-31T23:00:00Z', '2026-10-31T23:30:00Z', 3000000000, '2026-10-31T23:30:05.123456Z');

SELECT pg_temp.assert_result(
    scenario => 'independent source retains large exact units',
    input_sql => $input$
        SELECT units FROM usage_inbox WHERE source = 'usage-inbox-schema-test-other' AND event_id = 'acme-cpu-001'
    $input$,
    want_result => $want$
    {
        "units": 3000000000
    }
    $want$::jsonb
);

SELECT pg_temp.assert_rejection(
    scenario => 'usage units cannot be negative',
    input_sql => $input$
        UPDATE usage_inbox SET units = -1 WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'
    $input$,
    want_sqlstate => '23514'
);

SELECT pg_temp.assert_rejection(
    scenario => 'usage period must have positive duration',
    input_sql => $input$
        UPDATE usage_inbox SET period_end = period_start WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'
    $input$,
    want_sqlstate => '23514'
);

-- Input: an unresolved accounting error remains on the event.
UPDATE usage_inbox SET processing_error = 'Price not found' WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001';
SELECT pg_temp.assert_rejection(
    scenario => 'processed_at cannot coexist with an unresolved processing error',
    input_sql => $input$
        UPDATE usage_inbox SET processed_at = '2026-10-31T23:30:10Z' WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'
    $input$,
    want_sqlstate => '23514'
);

-- Input: clear the error before marking successful processing.
UPDATE usage_inbox SET processing_error = NULL, processed_at = '2026-10-31T23:30:10Z' WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001';
SELECT pg_temp.assert_result(
    scenario => 'successful processing is accepted after clearing the error',
    input_sql => $input$
        SELECT to_char(processed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS processed_at, processing_error
        FROM usage_inbox WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'
    $input$,
    want_result => $want$
    {
        "processed_at": "2026-10-31T23:30:10Z",
        "processing_error": null
    }
    $want$::jsonb
);

UPDATE usage_inbox SET schema_version = 1 WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001';
SELECT pg_temp.assert_result(scenario => 'minimum positive schema version is valid', input_sql => $$SELECT schema_version FROM usage_inbox WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'$$, want_result => '{"schema_version":1}'::jsonb);
SELECT pg_temp.assert_rejection(scenario => 'zero schema version is rejected', input_sql => $$UPDATE usage_inbox SET schema_version = 0 WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'$$, want_sqlstate => '23514');
SELECT pg_temp.assert_rejection(scenario => 'negative schema version is rejected', input_sql => $$UPDATE usage_inbox SET schema_version = -1 WHERE source = 'usage-inbox-schema-test' AND event_id = 'acme-cpu-001'$$, want_sqlstate => '23514');

ROLLBACK;
