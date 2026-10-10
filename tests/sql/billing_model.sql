\set ON_ERROR_STOP on

\if :{?test_timezone}
\else
    \set test_timezone UTC
\endif

-- All fixtures and test helpers disappear on rollback.
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

INSERT INTO customers (customer_id, name, country, billing_address)
VALUES
    ('billing-model-acme', 'Test Acme', 'US', 'Test address'),
    ('billing-model-cyberdyne', 'Test Cyberdyne', 'US', 'Test address');
INSERT INTO customer_billing_state (customer_id, credit_balance_ticks, spend_limit_cents)
VALUES ('billing-model-acme', 0, NULL);
INSERT INTO customer_billing_state (customer_id, credit_balance_ticks, spend_limit_cents, state_version)
VALUES ('billing-model-cyberdyne', 0, 1500, 7);

SELECT pg_temp.assert_result(
    scenario => 'default and explicit initial account versions',
    input_sql => $input$
        SELECT (SELECT state_version FROM customer_billing_state WHERE customer_id = 'billing-model-acme') AS default_version,
            (SELECT state_version FROM customer_billing_state WHERE customer_id = 'billing-model-cyberdyne') AS explicit_version
    $input$,
    want_result => $want$
    {
        "default_version": 0,
        "explicit_version": 7
    }
    $want$::jsonb
);

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

SELECT pg_temp.assert_result(
    scenario => 'receipt ending exactly at the UTC month boundary enters October',
    input_sql => $input$
        SELECT g.usage_month::text, g.billing_month::text
        FROM usage_ratings r JOIN rated_usage_groups g USING (group_id)
        WHERE r.source = 'billing-model-test' AND r.event_id = 'acme-2'
    $input$,
    want_result => $want$
    {
        "usage_month": "2026-10-01",
        "billing_month": "2026-10-01"
    }
    $want$::jsonb
);

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

SELECT pg_temp.assert_result(
    scenario => 'exact sub-cent charge survives storage',
    input_sql => $input$
        SELECT exact_charge_ticks FROM rated_usage_groups WHERE group_id = 'billing-model-cyberdyne-oct'
    $input$,
    want_result => $want$
    {
        "exact_charge_ticks": 617283945
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'two sandbox events share one rounding group',
    input_sql => $input$
        SELECT count(*) AS count FROM usage_ratings WHERE group_id = 'billing-model-acme-oct'
    $input$,
    want_result => $want$
    {
        "count": 2
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'late October usage is billed in November',
    input_sql => $input$
        SELECT usage_month::text, billing_month::text FROM rated_usage_groups WHERE group_id = 'billing-model-acme-late'
    $input$,
    want_result => $want$
    {
        "usage_month": "2026-10-01",
        "billing_month": "2026-11-01"
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'twenty-five USD credit less twelve USD usage leaves thirteen USD',
    input_sql => $input$
        SELECT (SELECT sum(amount_ticks) FROM credit_entries WHERE customer_id = 'billing-model-acme') AS ledger_ticks,
            (SELECT credit_balance_ticks FROM customer_billing_state WHERE customer_id = 'billing-model-acme') AS balance_ticks
    $input$,
    want_result => $want$
    {
        "ledger_ticks": 1300000000,
        "balance_ticks": 1300000000
    }
    $want$::jsonb
);

-- Input: the maximum bigint usage multiplied by a five-cent rate exceeds bigint.
UPDATE rated_usage_groups SET total_units = 9223372036854775807,
    exact_charge_ticks = 46116860184273879035, booked_charge_cents = 46116860184274
WHERE group_id = 'billing-model-cyberdyne-oct';
SELECT pg_temp.assert_result(
    scenario => 'tick products above bigint remain exact',
    input_sql => $input$
        SELECT exact_charge_ticks FROM rated_usage_groups WHERE group_id = 'billing-model-cyberdyne-oct'
    $input$,
    want_result => $want$
    {
        "exact_charge_ticks": 46116860184273879035
    }
    $want$::jsonb
);

-- Input: change the current catalog price after a subscription was purchased.
UPDATE addons SET monthly_price_cents = 2500 WHERE addon_name = 'billing-model-addon';
SELECT pg_temp.assert_result(
    scenario => 'purchased subscription retains its original price',
    input_sql => $input$
        SELECT monthly_price_cents FROM addon_subscriptions WHERE subscription_id = 'billing-model-subscription'
    $input$,
    want_result => $want$
    {
        "monthly_price_cents": 2000
    }
    $want$::jsonb
);

-- Invalid balances, duplicate histories, and mismatched references must be rejected.
DO $tests$
DECLARE
    invalid_number text;
BEGIN
    PERFORM pg_temp.assert_rejection(
            scenario => 'Negative credit balance',
            input_sql => $$UPDATE customer_billing_state SET credit_balance_ticks = -1 WHERE customer_id = 'billing-model-acme'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Negative spend limit',
            input_sql => $$UPDATE customer_billing_state SET spend_limit_cents = -1 WHERE customer_id = 'billing-model-acme'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Negative state version',
            input_sql => $$UPDATE customer_billing_state SET state_version = -1 WHERE customer_id = 'billing-model-acme'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Explicit null state version',
            input_sql => $$UPDATE customer_billing_state SET state_version = NULL WHERE customer_id = 'billing-model-acme'$$,
            want_sqlstate => '23502');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Application must supply opening balance',
            input_sql => $$INSERT INTO customer_billing_state (customer_id, spend_limit_cents, state_version) VALUES ('billing-model-acme', NULL, 0)$$,
            want_sqlstate => '23502');

    PERFORM pg_temp.assert_rejection(
            scenario => 'Duplicate default effective time',
            input_sql => $$INSERT INTO price_versions VALUES ('billing-model-duplicate-default', NULL, 'billing-model-cpu', 7, '2026-10-01T00:00:00Z')$$,
            want_sqlstate => '23505');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Duplicate customer effective time',
            input_sql => $$INSERT INTO price_versions VALUES ('billing-model-duplicate-override', 'billing-model-acme', 'billing-model-cpu', 7, '2026-10-01T00:00:00Z')$$,
            want_sqlstate => '23505');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Negative price',
            input_sql => $$INSERT INTO price_versions VALUES ('billing-model-negative-price', NULL, 'billing-model-cpu', -1, '2026-11-01T00:00:00Z')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Non-finite price time',
            input_sql => $$INSERT INTO price_versions VALUES ('billing-model-infinite-price', NULL, 'billing-model-cpu', 5, 'infinity')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Unknown price customer',
            input_sql => $$INSERT INTO price_versions VALUES ('billing-model-unknown-owner', 'billing-model-missing', 'billing-model-cpu', 5, '2026-11-01T00:00:00Z')$$,
            want_sqlstate => '23503');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Historical price update',
            input_sql => $$UPDATE price_versions SET price_per_million_cents = 9 WHERE price_version_id = 'billing-model-default'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Historical price deletion',
            input_sql => $$DELETE FROM price_versions WHERE price_version_id = 'billing-model-default-new'$$,
            want_sqlstate => '23514');

    PERFORM pg_temp.assert_rejection(
            scenario => 'Another customer override',
            input_sql => $$INSERT INTO rated_usage_groups VALUES ('billing-model-wrong-owner', 'billing-model-cyberdyne', 'billing-model-acme-price', 'billing-model-cpu', '2026-10-01', '2026-10-01', 1, 4, 0, 0)$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Price metric mismatch',
            input_sql => $$INSERT INTO rated_usage_groups VALUES ('billing-model-wrong-metric', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-other', '2026-10-01', '2026-12-01', 1, 4, 0, 0)$$,
            want_sqlstate => '23503');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Duplicate rounding group',
            input_sql => $$INSERT INTO rated_usage_groups SELECT 'billing-model-duplicate-group', customer_id, price_version_id, metric, usage_month, billing_month, total_units, exact_charge_ticks, booked_charge_cents, allocated_credit_ticks FROM rated_usage_groups WHERE group_id = 'billing-model-acme-oct'$$,
            want_sqlstate => '23505');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Changing group ownership',
            input_sql => $$UPDATE rated_usage_groups SET customer_id = 'billing-model-cyberdyne' WHERE group_id = 'billing-model-acme-oct'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Credit exceeding exact gross usage',
            input_sql => $$UPDATE rated_usage_groups SET allocated_credit_ticks = 1200000001 WHERE group_id = 'billing-model-acme-oct'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Month must start on day one',
            input_sql => $$INSERT INTO rated_usage_groups VALUES ('billing-model-bad-month', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-cpu', '2026-10-02', '2026-11-01', 1, 4, 0, 0)$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Billing before original usage month',
            input_sql => $$INSERT INTO rated_usage_groups VALUES ('billing-model-earlier-month', 'billing-model-acme', 'billing-model-acme-price', 'billing-model-cpu', '2026-11-01', '2026-10-01', 1, 4, 0, 0)$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Non-finite month',
            input_sql => $$INSERT INTO monthly_usage VALUES ('billing-model-acme', 'infinity', 0)$$,
            want_sqlstate => '23514');

    FOREACH invalid_number IN ARRAY ARRAY['-1', '0.5', 'NaN', 'Infinity', '-Infinity'] LOOP
        PERFORM pg_temp.assert_rejection(
            scenario => 'Invalid exact ticks: ' || invalid_number,
            input_sql => format('UPDATE monthly_usage SET gross_charge_ticks = %L::numeric WHERE customer_id = %L', invalid_number, 'billing-model-acme'),
            want_sqlstate => '23514');
        PERFORM pg_temp.assert_rejection(
            scenario => 'Invalid aggregate units: ' || invalid_number,
            input_sql => format('UPDATE rated_usage_groups SET total_units = %L::numeric WHERE group_id = %L', invalid_number, 'billing-model-acme-oct'),
            want_sqlstate => '23514');
    END LOOP;

    PERFORM pg_temp.assert_rejection(
            scenario => 'An event cannot contribute twice',
            input_sql => $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'acme-1', 'billing-model-acme-late')$$,
            want_sqlstate => '23505');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Changing an event contribution',
            input_sql => $$UPDATE usage_ratings SET group_id = 'billing-model-acme-late' WHERE source = 'billing-model-test' AND event_id = 'acme-1'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Missing original receipt',
            input_sql => $$INSERT INTO usage_ratings VALUES ('billing-model-missing', 'acme-1', 'billing-model-acme-oct')$$,
            want_sqlstate => '23503');

    PERFORM pg_temp.assert_rejection(
            scenario => 'Duplicate credit operation',
            input_sql => $$INSERT INTO credit_entries VALUES ('billing-model-grant-retry', 'billing-model-acme', 'grant-1', NULL, 2500, '2026-10-01T00:00:00Z')$$,
            want_sqlstate => '23505');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Credit debit for another customer group',
            input_sql => $$INSERT INTO credit_entries VALUES ('billing-model-cross-customer', 'billing-model-cyberdyne', 'debit-2', 'billing-model-acme-oct', -1, '2026-11-01T00:00:00Z')$$,
            want_sqlstate => '23503');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Negative grant without usage',
            input_sql => $$INSERT INTO credit_entries VALUES ('billing-model-negative-grant', 'billing-model-acme', 'grant-2', NULL, -1, '2026-10-01T00:00:00Z')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Positive usage debit',
            input_sql => $$INSERT INTO credit_entries VALUES ('billing-model-positive-debit', 'billing-model-acme', 'debit-2', 'billing-model-acme-oct', 1, '2026-11-01T00:00:00Z')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Editing credit history',
            input_sql => $$UPDATE credit_entries SET amount_ticks = 2600000000 WHERE credit_entry_id = 'billing-model-grant'$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Deleting credit history',
            input_sql => $$DELETE FROM credit_entries WHERE credit_entry_id = 'billing-model-debit'$$,
            want_sqlstate => '23514');

    PERFORM pg_temp.assert_rejection(
            scenario => 'Duplicate customer add-on',
            input_sql => $$INSERT INTO addon_subscriptions VALUES ('billing-model-duplicate-subscription', 'billing-model-acme', 'billing-model-addon', 2000, '2026-11-01T00:00:00Z', '2026-11-01')$$,
            want_sqlstate => '23505');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Add-on purchase month must use UTC',
            input_sql => $$INSERT INTO addon_subscriptions VALUES ('billing-model-wrong-start', 'billing-model-cyberdyne', 'billing-model-addon', 2000, '2026-10-31T23:59:59Z', '2026-11-01')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Negative subscription price',
            input_sql => $$INSERT INTO addon_subscriptions VALUES ('billing-model-negative-subscription', 'billing-model-cyberdyne', 'billing-model-addon', -1, '2026-10-05T00:00:00Z', '2026-10-01')$$,
            want_sqlstate => '23514');

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

    PERFORM pg_temp.assert_rejection(
            scenario => 'Receipt customer mismatch',
            input_sql => $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'wrong-customer', 'billing-model-acme-oct')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Receipt metric mismatch',
            input_sql => $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'wrong-metric', 'billing-model-acme-oct')$$,
            want_sqlstate => '23514');
    PERFORM pg_temp.assert_rejection(
            scenario => 'Receipt spans two UTC months',
            input_sql => $$INSERT INTO usage_ratings VALUES ('billing-model-test', 'cross-month', 'billing-model-acme-oct')$$,
            want_sqlstate => '23514');

    RAISE NOTICE 'Billing model integrity tests passed (TimeZone: %)', current_setting('TimeZone');
END;
$tests$;

ROLLBACK;
