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

-- The assignment's exact catalog and empty opening balances must be reproducible.
DO $tests$
BEGIN
    IF (SELECT count(*) FROM customers) <> 2 OR NOT EXISTS (
        SELECT 1 FROM customers
        WHERE customer_id = 'acme' AND name = 'Acme Inc.' AND country = 'US'
            AND billing_address = '1 Market St, San Francisco, CA 94105'
    ) OR NOT EXISTS (
        SELECT 1 FROM customers
        WHERE customer_id = 'cyberdyne' AND name = 'Cyberdyne Systems Corporation' AND country = 'US'
            AND billing_address = '18144 El Camino Real, Sunnyvale, CA 94087'
    ) THEN
        RAISE EXCEPTION 'Seed customers must exactly match the assignment';
    END IF;

    IF (SELECT count(*) FROM customer_billing_state) <> 2 OR EXISTS (
        SELECT 1 FROM customer_billing_state
        WHERE credit_balance_cents <> 0 OR spend_limit_cents IS NOT NULL OR state_version <> 0
    ) THEN
        RAISE EXCEPTION 'Seed accounts must start with no credit, no limit, and version zero';
    END IF;

    IF (SELECT array_agg(metric) FROM metrics) IS DISTINCT FROM ARRAY['cpu_seconds'] THEN
        RAISE EXCEPTION 'Seed metrics must contain only cpu_seconds';
    END IF;

    IF (SELECT count(*) FROM price_versions) <> 3 OR NOT EXISTS (
        SELECT 1 FROM price_versions WHERE customer_id IS NULL AND metric = 'cpu_seconds'
            AND price_per_million_cents = 5 AND effective_from = '2026-10-01T00:00:00Z'::timestamptz
    ) OR NOT EXISTS (
        SELECT 1 FROM price_versions WHERE customer_id IS NULL AND metric = 'cpu_seconds'
            AND price_per_million_cents = 6 AND effective_from = '2026-10-15T00:00:00Z'::timestamptz
    ) OR NOT EXISTS (
        SELECT 1 FROM price_versions WHERE customer_id = 'acme' AND metric = 'cpu_seconds'
            AND price_per_million_cents = 4 AND effective_from = '2026-10-01T00:00:00Z'::timestamptz
    ) THEN
        RAISE EXCEPTION 'Seed prices must preserve the default history and Acme override in UTC';
    END IF;

    IF (SELECT count(*) FROM addons) <> 1 OR NOT EXISTS (
        SELECT 1 FROM addons WHERE addon_name = 'concurrency_pack' AND monthly_price_cents = 2000
    ) THEN
        RAISE EXCEPTION 'Seed add-ons must contain the 20 USD concurrency_pack';
    END IF;

    IF EXISTS (SELECT 1 FROM usage_inbox) OR EXISTS (SELECT 1 FROM usage_ratings)
        OR EXISTS (SELECT 1 FROM rated_usage_groups) OR EXISTS (SELECT 1 FROM monthly_usage)
        OR EXISTS (SELECT 1 FROM credit_entries) OR EXISTS (SELECT 1 FROM addon_subscriptions) THEN
        RAISE EXCEPTION 'Seeding must not perform the example business actions or create usage';
    END IF;

    RAISE NOTICE 'Assignment seed tests passed (TimeZone: %)', current_setting('TimeZone');
END;
$tests$;

ROLLBACK;
