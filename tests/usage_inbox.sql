\set ON_ERROR_STOP on

-- Fixtures are rolled back, leaving the developer database unchanged.
BEGIN;

DO $tests$
DECLARE
    fixture_source constant text := 'usage-inbox-schema-test';
    fixture_id constant text := 'acme-cpu-001';
    stored usage_inbox%ROWTYPE;
BEGIN
    INSERT INTO usage_inbox (
        source, event_id, customer_id, sandbox_id, metric,
        period_start, period_end, units
    ) VALUES (
        fixture_source, fixture_id, 'acme', 'sb-001', 'cpu_seconds',
        '2026-10-10T12:00:00Z', '2026-10-10T13:00:00Z', 100000000
    );

    SELECT * INTO STRICT stored
    FROM usage_inbox
    WHERE source = fixture_source AND event_id = fixture_id;

    IF stored.schema_version <> 1 OR stored.received_at IS NULL
        OR stored.processed_at IS NOT NULL OR stored.processing_error IS NOT NULL THEN
        RAISE EXCEPTION 'New input must have a version, receipt time, and pending state';
    END IF;

    -- An identity cannot be inserted twice or overwritten with changed units.
    BEGIN
        INSERT INTO usage_inbox (
            source, event_id, customer_id, sandbox_id, metric,
            period_start, period_end, units
        ) VALUES (
            fixture_source, fixture_id, 'acme', 'sb-001', 'cpu_seconds',
            '2026-10-10T12:00:00Z', '2026-10-10T13:00:00Z', 200000000
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
        source, event_id, customer_id, sandbox_id, metric,
        period_start, period_end, units
    ) VALUES (
        fixture_source || '-other', fixture_id, 'acme', 'sb-002', 'cpu_seconds',
        '2026-10-10T12:00:00Z', '2026-10-10T13:00:00Z', 3000000000
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

    BEGIN
        UPDATE usage_inbox SET metric = '   '
        WHERE source = fixture_source AND event_id = fixture_id;
        RAISE EXCEPTION 'A blank metric was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    UPDATE usage_inbox SET processing_error = 'Price not found'
    WHERE source = fixture_source AND event_id = fixture_id;

    BEGIN
        UPDATE usage_inbox SET processed_at = now()
        WHERE source = fixture_source AND event_id = fixture_id;
        RAISE EXCEPTION 'Errored input was marked as successfully processed';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    UPDATE usage_inbox SET processing_error = NULL, processed_at = now()
    WHERE source = fixture_source AND event_id = fixture_id;

    RAISE NOTICE 'usage_inbox integrity tests passed';
END;
$tests$;

ROLLBACK;
