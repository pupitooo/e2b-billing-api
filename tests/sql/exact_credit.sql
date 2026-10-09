\set ON_ERROR_STOP on

\if :{?test_timezone}
\else
    \set test_timezone UTC
\endif

-- Exercise the real cent-to-tick migration on legacy financial records in an
-- isolated transaction. Neither application data nor migration versions change.
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';
CREATE SCHEMA e2b_exact_credit_test;
SET LOCAL search_path TO e2b_exact_credit_test;
\ir /migrations/001_usage_inbox.sql
\ir /migrations/002_billing_model.sql
\ir /migrations/003_assignment_seed.sql

INSERT INTO rated_usage_groups (
    group_id, customer_id, price_version_id, metric, usage_month, billing_month,
    total_units, exact_charge_ticks, booked_charge_cents, allocated_credit_cents
) VALUES ('credit-migration-group', 'acme', 'cpu-acme-2026-10-01', 'cpu_seconds',
    '2026-10-01', '2026-10-01', 300000000, 1200000000, 1200, 1200);
INSERT INTO credit_entries (credit_entry_id, customer_id, operation_id, group_id, amount_cents, recorded_at)
VALUES
    ('credit-migration-grant', 'acme', 'grant', NULL, 2500, '2026-10-01T00:00:00Z'),
    ('credit-migration-debit', 'acme', 'debit', 'credit-migration-group', -1200, '2026-10-10T12:00:00Z');
UPDATE customer_billing_state SET credit_balance_cents = 1300, state_version = 2 WHERE customer_id = 'acme';

-- The previous maximum bigint balance must convert without bigint multiplication.
INSERT INTO customers VALUES ('credit-large-customer', 'Large credit', 'US', 'Test address');
INSERT INTO customer_billing_state VALUES ('credit-large-customer', 9223372036854775807, NULL, 0);
INSERT INTO credit_entries VALUES ('credit-large-grant', 'credit-large-customer', 'large-grant', NULL,
    9223372036854775807, '2026-10-01T00:00:00Z');

\ir /migrations/004_exact_credit.sql

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

SELECT pg_temp.assert_result(
    scenario => 'legacy cent migration preserves exact ledger and account state',
    input_sql => $input$
        SELECT (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'acme') AS balance_ticks,
            (SELECT state_version FROM customer_billing_state WHERE customer_id = 'acme') AS state_version,
            (SELECT sum(amount_ticks) FROM credit_entries WHERE customer_id = 'acme') AS ledger_ticks,
            (SELECT allocated_credit_ticks FROM rated_usage_groups WHERE group_id = 'credit-migration-group') AS allocated_ticks,
            (SELECT count(*) FROM credit_entries) AS entry_count,
            (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'credit-large-customer') AS large_balance_ticks,
            (SELECT amount_ticks FROM credit_entries WHERE credit_entry_id = 'credit-large-grant') AS large_grant_ticks
    $input$,
    want_result => $want$
    {
        "balance_ticks": 1300000000,
        "state_version": 2,
        "ledger_ticks": 1300000000,
        "allocated_ticks": 1200000000,
        "entry_count": 3,
        "large_balance_ticks": 9223372036854775807000000,
        "large_grant_ticks": 9223372036854775807000000
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'legacy debit retains identity, sign, timestamp, and amount',
    input_sql => $input$
        SELECT operation_id, group_id, amount_ticks,
            to_char(recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS recorded_at
        FROM credit_entries WHERE credit_entry_id = 'credit-migration-debit'
    $input$,
    want_result => $want$
    {
        "operation_id": "debit",
        "group_id": "credit-migration-group",
        "amount_ticks": -1200000000,
        "recorded_at": "2026-10-10T12:00:00Z"
    }
    $want$::jsonb
);

-- A single fractional-cent debit and remaining balance are representable.
INSERT INTO rated_usage_groups VALUES (
    'credit-small-group', 'cyberdyne', 'cpu-default-2026-10-01', 'cpu_seconds',
    '2026-10-01', '2026-10-01', 1000, 5000, 0, 5000
);
INSERT INTO credit_entries VALUES
    ('credit-small-grant', 'cyberdyne', 'small-grant', NULL, 1000000, '2026-10-01T00:00:00Z'),
    ('credit-small-debit', 'cyberdyne', 'small-debit', 'credit-small-group', -5000, '2026-10-10T12:00:00Z');
UPDATE customer_billing_state SET credit_balance_ticks = 995000 WHERE customer_id = 'cyberdyne';

SELECT pg_temp.assert_result(
    scenario => 'fractional-cent debit retains exact remaining balance',
    input_sql => $input$
        SELECT (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'cyberdyne') AS balance_ticks,
            (SELECT sum(amount_ticks) FROM credit_entries WHERE customer_id = 'cyberdyne') AS ledger_ticks
    $input$,
    want_result => $want$
    {
        "balance_ticks": 995000,
        "ledger_ticks": 995000
    }
    $want$::jsonb
);

-- Invalid numeric representations must fail for both balances and allocation.
DO $tests$
DECLARE
    invalid_number text;
BEGIN
    FOREACH invalid_number IN ARRAY ARRAY['-1', '0.5', 'NaN', 'Infinity', '-Infinity'] LOOP
        PERFORM pg_temp.assert_rejection(
            scenario => 'Invalid balance ticks: ' || invalid_number,
            input_sql => format('UPDATE customer_billing_state SET credit_balance_ticks = %L::numeric WHERE customer_id = %L', invalid_number, 'cyberdyne'),
            want_sqlstate => '23514');
        PERFORM pg_temp.assert_rejection(
            scenario => 'Invalid allocated ticks: ' || invalid_number,
            input_sql => format('UPDATE rated_usage_groups SET allocated_credit_ticks = %L::numeric WHERE group_id = %L', invalid_number, 'credit-small-group'),
            want_sqlstate => '23514');
    END LOOP;
    FOREACH invalid_number IN ARRAY ARRAY['0', '-1', '0.5', 'NaN', 'Infinity', '-Infinity'] LOOP
        PERFORM pg_temp.assert_rejection(
            scenario => 'Invalid grant ticks: ' || invalid_number,
            input_sql => format('INSERT INTO credit_entries VALUES (%L, %L, %L, NULL, %L::numeric, %L)',
                'invalid-credit', 'cyberdyne', 'invalid-credit', invalid_number, '2026-10-01T00:00:00Z'),
            want_sqlstate => '23514');
    END LOOP;
    PERFORM pg_temp.assert_rejection(
            scenario => 'Allocation exceeds exact charge',
            input_sql => $$UPDATE rated_usage_groups SET allocated_credit_ticks = 5001 WHERE group_id = 'credit-small-group'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Debit has a positive amount',
            input_sql => $$INSERT INTO credit_entries VALUES ('invalid-debit', 'cyberdyne', 'invalid-debit', 'credit-small-group', 1, '2026-10-01T00:00:00Z')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Credit history update',
            input_sql => $$UPDATE credit_entries SET amount_ticks = 1 WHERE credit_entry_id = 'credit-small-grant'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Credit history deletion',
            input_sql => $$DELETE FROM credit_entries WHERE credit_entry_id = 'credit-small-debit'$$,
            want_sqlstate => '23514');
    RAISE NOTICE 'Exact credit migration tests passed (TimeZone: %)', current_setting('TimeZone');
END;
$tests$;

ROLLBACK;
