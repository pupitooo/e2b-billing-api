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
    \ir 002_application_text_validation.sql
    INSERT INTO schema_migrations (version, name)
    VALUES (2, 'application_text_validation');
\endif

COMMIT;
