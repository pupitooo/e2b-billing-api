-- Exact credit is consumed before rounding, including fractional cents.
-- Legacy cent allocations could cover more than the exact gross charge after
-- half-up rounding. Stop atomically rather than rewriting that financial history.
DO $migration$
BEGIN
    IF EXISTS (
        SELECT 1 FROM rated_usage_groups
        WHERE allocated_credit_cents::numeric * 1000000 > exact_charge_ticks
    ) THEN
        RAISE EXCEPTION 'Legacy credit allocation exceeds exact gross usage'
            USING ERRCODE = '23514',
                HINT = 'Review the affected groups before migrating to exact credit; no financial history has been changed.';
    END IF;
END;
$migration$;

-- Ledger amounts can be signed, but must remain finite, exact integers.
CREATE DOMAIN billing_signed_ticks AS numeric
    CHECK (VALUE > '-Infinity'::numeric AND VALUE < 'Infinity'::numeric AND VALUE = trunc(VALUE));

ALTER TABLE customer_billing_state
    ALTER COLUMN credit_balance_cents TYPE billing_ticks
    USING credit_balance_cents::numeric * 1000000;
ALTER TABLE customer_billing_state RENAME COLUMN credit_balance_cents TO credit_balance_ticks;

-- Migration 002's multi-column credit CHECK has this PostgreSQL-generated name.
ALTER TABLE rated_usage_groups DROP CONSTRAINT rated_usage_groups_check;
ALTER TABLE rated_usage_groups
    ALTER COLUMN allocated_credit_cents TYPE billing_ticks
    USING allocated_credit_cents::numeric * 1000000;
ALTER TABLE rated_usage_groups RENAME COLUMN allocated_credit_cents TO allocated_credit_ticks;
ALTER TABLE rated_usage_groups
    ADD CONSTRAINT rated_usage_groups_credit_within_gross
    CHECK (allocated_credit_ticks <= exact_charge_ticks);

-- ALTER TYPE converts storage without UPDATE triggers or new ledger operations.
-- Existing identities, timestamps, signs, references, and operation keys survive.
ALTER TABLE credit_entries
    ALTER COLUMN amount_cents TYPE billing_signed_ticks
    USING amount_cents::numeric * 1000000;
ALTER TABLE credit_entries RENAME COLUMN amount_cents TO amount_ticks;
