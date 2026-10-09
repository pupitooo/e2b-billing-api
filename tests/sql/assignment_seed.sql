\set ON_ERROR_STOP on

\if :{?test_timezone}
\else
    \set test_timezone UTC
\endif

-- Rebuild the catalog in a private, rolled-back schema so user edits stay intact.
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';
CREATE SCHEMA e2b_assignment_seed_test;
SET LOCAL search_path TO e2b_assignment_seed_test;
-- make test runs psql in the PostgreSQL container with this read-only mount.
\ir /migrations/001_usage_inbox.sql
\ir /migrations/002_billing_model.sql
\ir /migrations/003_assignment_seed.sql
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
    scenario => 'assignment customers and billing addresses',
    input_sql => $input$
        SELECT count(*) AS count, jsonb_agg(jsonb_build_object(
            'customer_id', customer_id, 'name', name, 'country', country,
            'billing_address', billing_address) ORDER BY customer_id) AS customers
        FROM customers
    $input$,
    want_result => $want$
    {
        "count": 2,
        "customers": [
            {
                "customer_id": "acme",
                "name": "Acme Inc.",
                "country": "US",
                "billing_address": "1 Market St, San Francisco, CA 94105"
            },
            {
                "customer_id": "cyberdyne",
                "name": "Cyberdyne Systems Corporation",
                "country": "US",
                "billing_address": "18144 El Camino Real, Sunnyvale, CA 94087"
            }
        ]
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'opening billing state has no credit or spending limit',
    input_sql => $input$
        SELECT count(*) AS count, bool_and(credit_balance_ticks = 0
            AND spend_limit_cents IS NULL AND state_version = 0) AS all_empty
        FROM customer_billing_state
    $input$,
    want_result => $want$
    {
        "count": 2,
        "all_empty": true
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'assignment exposes only CPU seconds',
    input_sql => $input$
        SELECT array_agg(metric) AS metrics FROM metrics
    $input$,
    want_result => $want$
    {
        "metrics": [
            "cpu_seconds"
        ]
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'default price history and Acme override use UTC effective instants',
    input_sql => $input$
        SELECT count(*) AS count, jsonb_agg(jsonb_build_object(
            'customer_id', customer_id, 'metric', metric, 'price_per_million_cents', price_per_million_cents,
            'effective_from', to_char(effective_from AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
            ORDER BY customer_id NULLS FIRST, effective_from) AS prices
        FROM price_versions
    $input$,
    want_result => $want$
    {
        "count": 3,
        "prices": [
            {
                "customer_id": null,
                "metric": "cpu_seconds",
                "price_per_million_cents": 5,
                "effective_from": "2026-10-01T00:00:00Z"
            },
            {
                "customer_id": null,
                "metric": "cpu_seconds",
                "price_per_million_cents": 6,
                "effective_from": "2026-10-15T00:00:00Z"
            },
            {
                "customer_id": "acme",
                "metric": "cpu_seconds",
                "price_per_million_cents": 4,
                "effective_from": "2026-10-01T00:00:00Z"
            }
        ]
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'concurrency pack costs twenty USD per month',
    input_sql => $input$
        SELECT count(*) AS count, jsonb_agg(jsonb_build_object(
            'addon_name', addon_name, 'monthly_price_cents', monthly_price_cents)) AS addons FROM addons
    $input$,
    want_result => $want$
    {
        "count": 1,
        "addons": [
            {
                "addon_name": "concurrency_pack",
                "monthly_price_cents": 2000
            }
        ]
    }
    $want$::jsonb
);

SELECT pg_temp.assert_result(
    scenario => 'seeding does not perform example business actions',
    input_sql => $input$
        SELECT (SELECT count(*) FROM usage_inbox) AS inbox,
            (SELECT count(*) FROM usage_ratings) AS ratings,
            (SELECT count(*) FROM rated_usage_groups) AS groups,
            (SELECT count(*) FROM monthly_usage) AS monthly_usage,
            (SELECT count(*) FROM credit_entries) AS credit_entries,
            (SELECT count(*) FROM addon_subscriptions) AS subscriptions
    $input$,
    want_result => $want$
    {
        "inbox": 0,
        "ratings": 0,
        "groups": 0,
        "monthly_usage": 0,
        "credit_entries": 0,
        "subscriptions": 0
    }
    $want$::jsonb
);

ROLLBACK;
