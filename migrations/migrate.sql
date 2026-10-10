\set ON_ERROR_STOP on

BEGIN;

-- Serialize migration runners in this database until the transaction ends.
SELECT pg_advisory_xact_lock(65102, 1);

CREATE TABLE IF NOT EXISTS schema_migrations (
    version integer PRIMARY KEY CHECK (version > 0),
    name text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);

SELECT NOT EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = 1
) AS apply_001 \gset

\if :apply_001
    \ir 001_usage_inbox.sql
    INSERT INTO schema_migrations (version, name)
    VALUES (1, 'usage_inbox');
\endif

SELECT NOT EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = 2
) AS apply_002 \gset

\if :apply_002
    \ir 002_billing_model.sql
    INSERT INTO schema_migrations (version, name)
    VALUES (2, 'billing_model');
\endif

SELECT NOT EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = 3
) AS apply_003 \gset

\if :apply_003
    \ir 003_assignment_seed.sql
    INSERT INTO schema_migrations (version, name)
    VALUES (3, 'assignment_seed');
\endif

SELECT NOT EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = 4
) AS apply_004 \gset

\if :apply_004
    \ir 004_exact_credit.sql
    INSERT INTO schema_migrations (version, name)
    VALUES (4, 'exact_credit');
\endif

SELECT NOT EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = 5
) AS apply_005 \gset

\if :apply_005
    \ir 005_accounting_processing.sql
    INSERT INTO schema_migrations (version, name)
    VALUES (5, 'accounting_processing');
\endif

SELECT NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 6) AS apply_006 \gset
\if :apply_006
    \ir 006_spend_limit_operations.sql
    INSERT INTO schema_migrations (version, name) VALUES (6, 'spend_limit_operations');
\endif

SELECT NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 7) AS apply_007 \gset
\if :apply_007
    \ir 007_monthly_invoices.sql
    INSERT INTO schema_migrations (version, name) VALUES (7, 'monthly_invoices');
\endif

SELECT NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 8) AS apply_008 \gset
\if :apply_008
    \ir 008_invoice_closing_exclusions.sql
    INSERT INTO schema_migrations (version, name) VALUES (8, 'invoice_closing_exclusions');
\endif

COMMIT;
