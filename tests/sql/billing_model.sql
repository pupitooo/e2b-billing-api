\set ON_ERROR_STOP on

\if :{?test_timezone}
\else
    \set test_timezone UTC
\endif

-- All fixtures and test helpers disappear on rollback.
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';
CREATE TEMP TABLE billing_model_test_context (unused boolean) ON COMMIT DROP;

-- Execute an invalid write in a subtransaction and require its intended SQLSTATE.
CREATE FUNCTION pg_temp.expect_billing_rejection(statement text, expected_state text, scenario text)
RETURNS void LANGUAGE plpgsql AS $function$
BEGIN
    BEGIN
        EXECUTE statement;
    EXCEPTION WHEN OTHERS THEN
        IF SQLSTATE = expected_state THEN
            RETURN;
        END IF;
        RAISE EXCEPTION '%: expected SQLSTATE %, got % (%)', scenario, expected_state, SQLSTATE, SQLERRM;
    END;
    RAISE EXCEPTION '%: invalid write was accepted (expected SQLSTATE %)', scenario, expected_state;
END;
$function$;

INSERT INTO customers (customer_id, name, country, billing_address)
VALUES
    ('billing-model-acme', 'Test Acme', 'US', 'Test address'),
    ('billing-model-cyberdyne', 'Test Cyberdyne', 'US', 'Test address');
INSERT INTO customer_billing_state (customer_id, credit_balance_ticks, spend_limit_cents)
VALUES ('billing-model-acme', 0, NULL);
INSERT INTO customer_billing_state (customer_id, credit_balance_ticks, spend_limit_cents, state_version)
VALUES ('billing-model-cyberdyne', 0, 1500, 7);

-- New accounts start at version zero unless an explicit initial version is supplied.
DO $tests$
BEGIN
    IF (SELECT state_version FROM customer_billing_state WHERE customer_id = 'billing-model-acme')
        IS DISTINCT FROM 0::bigint THEN
        RAISE EXCEPTION 'An omitted account state version must default to zero';
    END IF;
    IF (SELECT state_version FROM customer_billing_state WHERE customer_id = 'billing-model-cyberdyne')
        IS DISTINCT FROM 7::bigint THEN
        RAISE EXCEPTION 'An explicit initial account state version must be preserved';
    END IF;
END;
$tests$;

INSERT INTO metrics (metric) VALUES ('billing-model-cpu'), ('billing-model-other');
INSERT INTO price_versions (
    price_version_id, customer_id, metric, price_per_million_cents, effective_from
) VALUES
    ('billing-model-default', NULL, 'billing-model-cpu', 5, '2026-10-01T00:00:00Z'),
    ('billing-model-default-new', NULL, 'billing-model-cpu', 6, '2026-10-15T00:00:00Z'),
    ('billing-model-acme-price', 'billing-model-acme', 'billing-model-cpu', 4, '2026-10-01T00:00:00Z');

INSERT INTO rated_usage_groups (
    group_id, customer_id, price_version_id, metric, usage_month, billing_month,
    total_units, exact_charge_ticks, booked_charge_cents, allocated_credit_ticks
) VALUES
    ('billing-model-acme-oct', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-cpu',
        '2026-10-01', '2026-10-01', 300000000, 1200000000, 1200, 1200000000),
    ('billing-model-cyberdyne-oct', 'billing-model-cyberdyne', 'billing-model-default', 'billing-model-cpu',
        '2026-10-01', '2026-10-01', 123456789, 617283945, 617, 0),
    ('billing-model-acme-late', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-cpu',
        '2026-10-01', '2026-11-01', 50000000, 200000000, 200, 0);

-- These instants are October in UTC and November in Asia/Shanghai.
INSERT INTO usage_inbox (
    source, event_id, schema_version, customer_id, sandbox_id, metric,
    period_start, period_end, units, received_at
) VALUES
    ('billing-model-test', 'acme-1', 1, 'billing-model-acme', 'sb-1', 'billing-model-cpu',
        '2026-10-31T23:00:00Z', '2026-10-31T23:30:00Z', 100000000, '2026-11-01T00:00:05Z'),
    ('billing-model-test', 'acme-2', 1, 'billing-model-acme', 'sb-2', 'billing-model-cpu',
        '2026-10-31T23:30:00Z', '2026-11-01T00:00:00Z', 200000000, '2026-11-01T00:00:06Z'),
    ('billing-model-test', 'acme-late', 1, 'billing-model-acme', 'sb-3', 'billing-model-cpu',
        '2026-10-30T12:00:00Z', '2026-10-30T13:00:00Z', 50000000, '2026-11-02T00:00:00Z');
INSERT INTO usage_ratings (source, event_id, group_id) VALUES
    ('billing-model-test', 'acme-1', 'billing-model-acme-oct'),
    ('billing-model-test', 'acme-2', 'billing-model-acme-oct'),
    ('billing-model-test', 'acme-late', 'billing-model-acme-late');

INSERT INTO monthly_usage (customer_id, usage_month, gross_charge_ticks) VALUES
    ('billing-model-acme', '2026-10-01', 1400000000),
    ('billing-model-cyberdyne', '2026-10-01', 617283945);
INSERT INTO credit_entries (credit_entry_id, customer_id, operation_id, group_id, amount_ticks, recorded_at)
VALUES
    ('billing-model-grant', 'billing-model-acme', 'grant-1', NULL, 2500000000, '2026-10-01T00:00:00Z'),
    ('billing-model-debit', 'billing-model-acme', 'debit-1', 'billing-model-acme-oct', -1200000000, '2026-11-01T00:01:00Z');
UPDATE customer_billing_state SET credit_balance_ticks = 1300000000, state_version = 1
WHERE customer_id = 'billing-model-acme';
INSERT INTO addons (addon_name, monthly_price_cents) VALUES ('billing-model-addon', 2000);
INSERT INTO addon_subscriptions (
    subscription_id, customer_id, addon_name, monthly_price_cents, purchased_at, start_month
) VALUES (
    'billing-model-subscription', 'billing-model-acme', 'billing-model-addon', 2000,
    '2026-10-31T23:59:59Z', '2026-10-01'
);

-- Exact sub-cent totals, late usage, and independent tick balances remain representable.
DO $tests$
BEGIN
    IF (SELECT exact_charge_ticks FROM rated_usage_groups WHERE group_id = 'billing-model-cyberdyne-oct')
        IS DISTINCT FROM 617283945::numeric THEN
        RAISE EXCEPTION 'Cyberdyne sub-cent charges must retain exact ticks';
    END IF;
    IF (SELECT count(*) FROM usage_ratings WHERE group_id = 'billing-model-acme-oct') <> 2 THEN
        RAISE EXCEPTION 'Multiple sandbox events must be able to share one rounding group';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM rated_usage_groups WHERE group_id = 'billing-model-acme-late'
            AND usage_month = '2026-10-01' AND billing_month = '2026-11-01'
    ) THEN
        RAISE EXCEPTION 'Late usage must preserve its original and later billing months';
    END IF;
    IF (SELECT sum(amount_ticks) FROM credit_entries WHERE customer_id = 'billing-model-acme') <> 1300000000
        OR (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'billing-model-acme') <> 1300000000 THEN
        RAISE EXCEPTION 'The model must represent 25 USD granted and 12 USD consumed with 13 USD remaining';
    END IF;

    UPDATE rated_usage_groups SET total_units = 9223372036854775807,
        exact_charge_ticks = 46116860184273879035, booked_charge_cents = 46116860184274
    WHERE group_id = 'billing-model-cyberdyne-oct';
    IF (SELECT exact_charge_ticks FROM rated_usage_groups WHERE group_id = 'billing-model-cyberdyne-oct')
        IS DISTINCT FROM 46116860184273879035::numeric THEN
        RAISE EXCEPTION 'Integer tick products beyond bigint must remain exact';
    END IF;

    UPDATE addons SET monthly_price_cents = 2500 WHERE addon_name = 'billing-model-addon';
    IF (SELECT monthly_price_cents FROM addon_subscriptions WHERE subscription_id = 'billing-model-subscription') <> 2000 THEN
        RAISE EXCEPTION 'A catalog price change must preserve the purchased subscription price';
    END IF;
END;
$tests$;

-- Invalid balances, duplicate histories, and mismatched references must be rejected.
DO $tests$
DECLARE
    invalid_number text;
BEGIN
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE customer_billing_state SET credit_balance_ticks = -1 WHERE customer_id = 'billing-model-acme'$$,
        '23514', 'Negative credit balance');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE customer_billing_state SET spend_limit_cents = -1 WHERE customer_id = 'billing-model-acme'$$,
        '23514', 'Negative spend limit');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE customer_billing_state SET state_version = -1 WHERE customer_id = 'billing-model-acme'$$,
        '23514', 'Negative state version');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE customer_billing_state SET state_version = NULL WHERE customer_id = 'billing-model-acme'$$,
        '23502', 'Explicit null state version');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO customer_billing_state (customer_id, spend_limit_cents, state_version) VALUES ('billing-model-acme', NULL, 0)$$,
        '23502', 'Application must supply opening balance');

    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO price_versions VALUES ('billing-model-duplicate-default', NULL, 'billing-model-cpu', 7, '2026-10-01T00:00:00Z')$$,
        '23505', 'Duplicate default effective time');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO price_versions VALUES ('billing-model-duplicate-override', 'billing-model-acme', 'billing-model-cpu', 7, '2026-10-01T00:00:00Z')$$,
        '23505', 'Duplicate customer effective time');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO price_versions VALUES ('billing-model-negative-price', NULL, 'billing-model-cpu', -1, '2026-11-01T00:00:00Z')$$,
        '23514', 'Negative price');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO price_versions VALUES ('billing-model-infinite-price', NULL, 'billing-model-cpu', 5, 'infinity')$$,
        '23514', 'Non-finite price time');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO price_versions VALUES ('billing-model-unknown-owner', 'billing-model-missing', 'billing-model-cpu', 5, '2026-11-01T00:00:00Z')$$,
        '23503', 'Unknown price customer');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE price_versions SET price_per_million_cents = 9 WHERE price_version_id = 'billing-model-default'$$,
        '23514', 'Historical price update');
    PERFORM pg_temp.expect_billing_rejection(
        $$DELETE FROM price_versions WHERE price_version_id = 'billing-model-default-new'$$,
        '23514', 'Historical price deletion');

    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO rated_usage_groups VALUES ('billing-model-wrong-owner', 'billing-model-cyberdyne', 'billing-model-acme-price', 'billing-model-cpu', '2026-10-01', '2026-10-01', 1, 4, 0, 0)$$,
        '23514', 'Another customer override');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO rated_usage_groups VALUES ('billing-model-wrong-metric', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-other', '2026-10-01', '2026-12-01', 1, 4, 0, 0)$$,
        '23503', 'Price metric mismatch');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO rated_usage_groups SELECT 'billing-model-duplicate-group', customer_id, price_version_id, metric, usage_month, billing_month, total_units, exact_charge_ticks, booked_charge_cents, allocated_credit_ticks FROM rated_usage_groups WHERE group_id = 'billing-model-acme-oct'$$,
        '23505', 'Duplicate rounding group');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE rated_usage_groups SET customer_id = 'billing-model-cyberdyne' WHERE group_id = 'billing-model-acme-oct'$$,
        '23514', 'Changing group ownership');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE rated_usage_groups SET allocated_credit_ticks = 1200000001 WHERE group_id = 'billing-model-acme-oct'$$,
        '23514', 'Credit exceeding exact gross usage');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO rated_usage_groups VALUES ('billing-model-bad-month', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-cpu', '2026-10-02', '2026-11-01', 1, 4, 0, 0)$$,
        '23514', 'Month must start on day one');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO rated_usage_groups VALUES ('billing-model-earlier-month', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-cpu', '2026-11-01', '2026-10-01', 1, 4, 0, 0)$$,
        '23514', 'Billing before original usage month');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO monthly_usage VALUES ('billing-model-acme', 'infinity', 0)$$,
        '23514', 'Non-finite month');

    FOREACH invalid_number IN ARRAY ARRAY['-1', '0.5', 'NaN', 'Infinity', '-Infinity'] LOOP
        PERFORM pg_temp.expect_billing_rejection(
            format('UPDATE monthly_usage SET gross_charge_ticks = %L::numeric WHERE customer_id = %L', invalid_number, 'billing-model-acme'),
            '23514', 'Invalid exact ticks: ' || invalid_number);
        PERFORM pg_temp.expect_billing_rejection(
            format('UPDATE rated_usage_groups SET total_units = %L::numeric WHERE group_id = %L', invalid_number, 'billing-model-acme-oct'),
            '23514', 'Invalid aggregate units: ' || invalid_number);
    END LOOP;

    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'acme-1', 'billing-model-acme-late')$$,
        '23505', 'An event cannot contribute twice');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE usage_ratings SET group_id = 'billing-model-acme-late' WHERE source = 'billing-model-test' AND event_id = 'acme-1'$$,
        '23514', 'Changing an event contribution');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO usage_ratings VALUES ('billing-model-missing', 'acme-1', 'billing-model-acme-oct')$$,
        '23503', 'Missing original receipt');

    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO credit_entries VALUES ('billing-model-grant-retry', 'billing-model-acme', 'grant-1', NULL, 2500, '2026-10-01T00:00:00Z')$$,
        '23505', 'Duplicate credit operation');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO credit_entries VALUES ('billing-model-cross-customer', 'billing-model-cyberdyne', 'debit-2', 'billing-model-acme-oct', -1, '2026-11-01T00:00:00Z')$$,
        '23503', 'Credit debit for another customer group');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO credit_entries VALUES ('billing-model-negative-grant', 'billing-model-acme', 'grant-2', NULL, -1, '2026-10-01T00:00:00Z')$$,
        '23514', 'Negative grant without usage');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO credit_entries VALUES ('billing-model-positive-debit', 'billing-model-acme', 'debit-2', 'billing-model-acme-oct', 1, '2026-11-01T00:00:00Z')$$,
        '23514', 'Positive usage debit');
    PERFORM pg_temp.expect_billing_rejection(
        $$UPDATE credit_entries SET amount_ticks = 2600000000 WHERE credit_entry_id = 'billing-model-grant'$$,
        '23514', 'Editing credit history');
    PERFORM pg_temp.expect_billing_rejection(
        $$DELETE FROM credit_entries WHERE credit_entry_id = 'billing-model-debit'$$,
        '23514', 'Deleting credit history');

    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO addon_subscriptions VALUES ('billing-model-duplicate-subscription', 'billing-model-acme', 'billing-model-addon', 2000, '2026-11-01T00:00:00Z', '2026-11-01')$$,
        '23505', 'Duplicate customer add-on');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO addon_subscriptions VALUES ('billing-model-wrong-start', 'billing-model-cyberdyne', 'billing-model-addon', 2000, '2026-10-31T23:59:59Z', '2026-11-01')$$,
        '23514', 'Add-on purchase month must use UTC');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO addon_subscriptions VALUES ('billing-model-negative-subscription', 'billing-model-cyberdyne', 'billing-model-addon', -1, '2026-10-05T00:00:00Z', '2026-10-01')$$,
        '23514', 'Negative subscription price');

END;
$tests$;

-- Cross-customer, wrong-metric, and cross-month receipts cannot enter a group.
DO $tests$
BEGIN
    INSERT INTO usage_inbox (
        source, event_id, schema_version, customer_id, sandbox_id, metric,
        period_start, period_end, units, received_at
    ) VALUES
        ('billing-model-test', 'wrong-customer', 1, 'billing-model-cyberdyne', 'sb-1', 'billing-model-cpu',
            '2026-10-10T12:00:00Z', '2026-10-10T13:00:00Z', 1, '2026-10-10T13:01:00Z'),
        ('billing-model-test', 'wrong-metric', 1, 'billing-model-acme', 'sb-1', 'billing-model-other',
            '2026-10-10T12:00:00Z', '2026-10-10T13:00:00Z', 1, '2026-10-10T13:01:00Z'),
        ('billing-model-test', 'cross-month', 1, 'billing-model-acme', 'sb-1', 'billing-model-cpu',
            '2026-10-31T23:59:00Z', '2026-11-01T00:01:00Z', 1, '2026-11-01T00:02:00Z');

    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'wrong-customer', 'billing-model-acme-oct')$$,
        '23514', 'Receipt customer mismatch');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'wrong-metric', 'billing-model-acme-oct')$$,
        '23514', 'Receipt metric mismatch');
    PERFORM pg_temp.expect_billing_rejection(
        $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'cross-month', 'billing-model-acme-oct')$$,
        '23514', 'Receipt spans two UTC months');

    RAISE NOTICE 'Billing model integrity tests passed (TimeZone: %)', current_setting('TimeZone');
END;
$tests$;

ROLLBACK;
