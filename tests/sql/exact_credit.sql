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

-- Legacy amounts must multiply exactly by one million, without changing ledger
-- identities, signs, timestamps, row counts, limits, or account versions.
DO $tests$
BEGIN
    IF (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'acme') <> 1300000000
        OR (SELECT state_version FROM customer_billing_state WHERE customer_id = 'acme') <> 2
        OR (SELECT sum(amount_ticks) FROM credit_entries WHERE customer_id = 'acme') <> 1300000000
        OR (SELECT allocated_credit_ticks FROM rated_usage_groups WHERE group_id = 'credit-migration-group') <> 1200000000
        OR (SELECT count(*) FROM credit_entries) <> 3
        OR (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'credit-large-customer') <> 9223372036854775807000000
        OR (SELECT amount_ticks FROM credit_entries WHERE credit_entry_id = 'credit-large-grant') <> 9223372036854775807000000
        OR NOT EXISTS (
            SELECT 1 FROM credit_entries WHERE credit_entry_id = 'credit-migration-debit'
                AND operation_id = 'debit' AND group_id = 'credit-migration-group'
                AND amount_ticks = -1200000000 AND recorded_at = '2026-10-10T12:00:00Z'::timestamptz
        ) THEN
        RAISE EXCEPTION 'Credit migration must preserve exact financial history and state';
    END IF;
END;
$tests$;

-- Context: invalid exact credit fixtures must fail with the expected SQLSTATE;
-- the helper rolls back its attempted write, preserving subsequent test cases.
CREATE FUNCTION pg_temp.expect_credit_rejection(statement text, expected_state text) RETURNS void
LANGUAGE plpgsql AS $function$
DECLARE
    actual_state text;
BEGIN
    BEGIN
        EXECUTE statement;
    EXCEPTION WHEN OTHERS THEN
        GET STACKED DIAGNOSTICS actual_state = RETURNED_SQLSTATE;
        IF actual_state = expected_state THEN
            RETURN;
        END IF;
        RAISE EXCEPTION 'Expected SQLSTATE %, received % for %', expected_state, actual_state, statement;
    END;
    RAISE EXCEPTION 'Expected rejection for %', statement;
END;
$function$;

-- A single fractional-cent debit and remaining balance are representable.
INSERT INTO rated_usage_groups VALUES (
    'credit-small-group', 'cyberdyne', 'cpu-default-2026-10-01', 'cpu_seconds',
    '2026-10-01', '2026-10-01', 1000, 5000, 0, 5000
);
INSERT INTO credit_entries VALUES
    ('credit-small-grant', 'cyberdyne', 'small-grant', NULL, 1000000, '2026-10-01T00:00:00Z'),
    ('credit-small-debit', 'cyberdyne', 'small-debit', 'credit-small-group', -5000, '2026-10-10T12:00:00Z');
UPDATE customer_billing_state SET credit_balance_ticks = 995000 WHERE customer_id = 'cyberdyne';

-- Negative balances, fractional ticks, non-finite values, excessive allocation,
-- invalid ledger signs, and history rewrites must still be rejected.
DO $tests$
DECLARE
    invalid_number text;
BEGIN
    IF (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'cyberdyne') <> 995000
        OR (SELECT sum(amount_ticks) FROM credit_entries WHERE customer_id = 'cyberdyne') <> 995000 THEN
        RAISE EXCEPTION 'A 5,000 tick debit must retain a 995,000 tick balance';
    END IF;
    FOREACH invalid_number IN ARRAY ARRAY['-1', '0.5', 'NaN', 'Infinity', '-Infinity'] LOOP
        PERFORM pg_temp.expect_credit_rejection(
            format('UPDATE customer_billing_state SET credit_balance_ticks = %L::numeric WHERE customer_id = %L', invalid_number, 'cyberdyne'), '23514');
        PERFORM pg_temp.expect_credit_rejection(
            format('UPDATE rated_usage_groups SET allocated_credit_ticks = %L::numeric WHERE group_id = %L', invalid_number, 'credit-small-group'), '23514');
    END LOOP;
    FOREACH invalid_number IN ARRAY ARRAY['0', '-1', '0.5', 'NaN', 'Infinity', '-Infinity'] LOOP
        PERFORM pg_temp.expect_credit_rejection(
            format('INSERT INTO credit_entries VALUES (%L, %L, %L, NULL, %L::numeric, %L)',
                'invalid-credit', 'cyberdyne', 'invalid-credit', invalid_number, '2026-10-01T00:00:00Z'), '23514');
    END LOOP;
    PERFORM pg_temp.expect_credit_rejection(
        $$UPDATE rated_usage_groups SET allocated_credit_ticks = 5001 WHERE group_id = 'credit-small-group'$$, '23514');
    PERFORM pg_temp.expect_credit_rejection(
        $$INSERT INTO credit_entries VALUES ('invalid-debit', 'cyberdyne', 'invalid-debit', 'credit-small-group', 1, '2026-10-01T00:00:00Z')$$, '23514');
    PERFORM pg_temp.expect_credit_rejection(
        $$UPDATE credit_entries SET amount_ticks = 1 WHERE credit_entry_id = 'credit-small-grant'$$, '23514');
    PERFORM pg_temp.expect_credit_rejection(
        $$DELETE FROM credit_entries WHERE credit_entry_id = 'credit-small-debit'$$, '23514');
    RAISE NOTICE 'Exact credit migration tests passed (TimeZone: %)', current_setting('TimeZone');
END;
$tests$;

ROLLBACK;
