\set ON_ERROR_STOP on

\if :{?test_timezone}
\else
    \set test_timezone UTC
\endif

-- Fixtures are rolled back, leaving the developer database unchanged.
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

DO $tests$
DECLARE
    fixture_source constant text := 'usage-inbox-schema-test';
    fixture_id constant text := 'acme-cpu-001';
    fixture_version constant integer := 2;
    -- These UTC instants display in the following month in Asia/Shanghai.
    fixture_period_start constant timestamptz := '2026-10-31T23:00:00Z';
    fixture_period_end constant timestamptz := '2026-10-31T23:30:00Z';
    fixture_received_at constant timestamptz := '2026-10-31T23:30:05.123456Z';
    fixture_processed_at constant timestamptz := '2026-10-31T23:30:10Z';
    stored usage_inbox%ROWTYPE;
BEGIN
    INSERT INTO usage_inbox (
        source, event_id, schema_version, customer_id, sandbox_id, metric,
        period_start, period_end, units, received_at
    ) VALUES (
        fixture_source, fixture_id, fixture_version, 'acme', 'sb-001', 'cpu_seconds',
        fixture_period_start, fixture_period_end, 100000000, fixture_received_at
    );

    SELECT * INTO STRICT stored
    FROM usage_inbox
    WHERE source = fixture_source AND event_id = fixture_id;

    IF stored.schema_version IS DISTINCT FROM fixture_version
        OR stored.received_at IS DISTINCT FROM fixture_received_at
        OR stored.processed_at IS NOT NULL OR stored.processing_error IS NOT NULL THEN
        RAISE EXCEPTION 'New input must preserve the supplied version and receipt time and remain pending';
    END IF;

    -- Epoch comparisons also detect accidentally dropping time zone information.
    IF EXTRACT(EPOCH FROM stored.period_start) IS DISTINCT FROM EXTRACT(EPOCH FROM fixture_period_start)
        OR EXTRACT(EPOCH FROM stored.period_end) IS DISTINCT FROM EXTRACT(EPOCH FROM fixture_period_end)
        OR EXTRACT(EPOCH FROM stored.received_at) IS DISTINCT FROM EXTRACT(EPOCH FROM fixture_received_at) THEN
        RAISE EXCEPTION 'Stored timestamps must preserve the supplied UTC instants across session time zones';
    END IF;

    -- Required application values must not be silently supplied by the database.
    BEGIN
        INSERT INTO usage_inbox (
            source, event_id, customer_id, sandbox_id, metric,
            period_start, period_end, units, received_at
        ) VALUES (
            fixture_source, fixture_id || '-missing-version', 'acme', 'sb-001', 'cpu_seconds',
            fixture_period_start, fixture_period_end, 100000000, fixture_received_at
        );
        RAISE EXCEPTION 'Input without an explicit schema version was accepted';
    EXCEPTION WHEN not_null_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO usage_inbox (
            source, event_id, schema_version, customer_id, sandbox_id, metric,
            period_start, period_end, units
        ) VALUES (
            fixture_source, fixture_id || '-missing-receipt-time', fixture_version, 'acme', 'sb-001', 'cpu_seconds',
            fixture_period_start, fixture_period_end, 100000000
        );
        RAISE EXCEPTION 'Input without an explicit receipt time was accepted';
    EXCEPTION WHEN not_null_violation THEN
        NULL;
    END;

    -- An identity cannot be inserted twice or overwritten with changed units.
    BEGIN
        INSERT INTO usage_inbox (
            source, event_id, schema_version, customer_id, sandbox_id, metric,
            period_start, period_end, units, received_at
        ) VALUES (
            fixture_source, fixture_id, fixture_version, 'acme', 'sb-001', 'cpu_seconds',
            fixture_period_start, fixture_period_end, 200000000, fixture_received_at
        );
        RAISE EXCEPTION 'Duplicate identity was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;

    IF (SELECT units FROM usage_inbox
        WHERE source = fixture_source AND event_id = fixture_id) <> 100000000 THEN
        RAISE EXCEPTION 'Duplicate input changed the original measurement';
    END IF;

    -- Independent sources may use the same event ID; large totals remain exact.
    INSERT INTO usage_inbox (
        source, event_id, schema_version, customer_id, sandbox_id, metric,
        period_start, period_end, units, received_at
    ) VALUES (
        fixture_source || '-other', fixture_id, fixture_version, 'acme', 'sb-002', 'cpu_seconds',
        fixture_period_start, fixture_period_end, 3000000000, fixture_received_at
    );

    IF (SELECT units FROM usage_inbox
        WHERE source = fixture_source || '-other' AND event_id = fixture_id) <> 3000000000 THEN
        RAISE EXCEPTION 'Units must retain values beyond 32-bit integer range';
    END IF;

    BEGIN
        UPDATE usage_inbox SET units = -1
        WHERE source = fixture_source AND event_id = fixture_id;
        RAISE EXCEPTION 'Negative usage was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE usage_inbox SET period_end = period_start
        WHERE source = fixture_source AND event_id = fixture_id;
        RAISE EXCEPTION 'An empty usage interval was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    UPDATE usage_inbox SET processing_error = 'Price not found'
    WHERE source = fixture_source AND event_id = fixture_id;

    BEGIN
        UPDATE usage_inbox SET processed_at = fixture_processed_at
        WHERE source = fixture_source AND event_id = fixture_id;
        RAISE EXCEPTION 'Errored input was marked as successfully processed';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    UPDATE usage_inbox SET processing_error = NULL, processed_at = fixture_processed_at
    WHERE source = fixture_source AND event_id = fixture_id;

    RAISE NOTICE 'usage_inbox integrity tests passed (TimeZone: %)', current_setting('TimeZone');
END;
$tests$;

ROLLBACK;
