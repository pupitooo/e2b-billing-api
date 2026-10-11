\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

-- Transaction-owned account and purchase isolate every integrity scenario.
INSERT INTO customers VALUES ('sql-life','Lifecycle test','US','Test address');
INSERT INTO customer_billing_state (customer_id,credit_balance_microcents,spend_limit_cents,state_version)
VALUES ('sql-life',0,NULL,0);
INSERT INTO addon_subscriptions VALUES ('sql-life-first','sql-life','concurrency_pack',2000,'2026-10-01T00:00:00Z','2026-10-01');

-- Each named input carries its SQLSTATE and executes in a recoverable subtransaction.
DO $scenarios$
DECLARE
    scenario jsonb;
    actual_state text;
BEGIN
    FOR scenario IN SELECT value FROM jsonb_array_elements($inputs$[
    {
        "name": "cancellation cannot precede purchase",
        "input_sql": "INSERT INTO addon_cancellations VALUES ('sql-life-first','2026-09-30T00:00:00Z','2026-09-01')",
        "want_sqlstate": "23514"
    },
    {
        "name": "end month must follow UTC not session zone",
        "input_sql": "INSERT INTO addon_cancellations VALUES ('sql-life-first','2026-11-01T00:00:00+08:00','2026-11-01')",
        "want_sqlstate": "23514"
    },
    {
        "name": "missing purchase is rejected",
        "input_sql": "INSERT INTO addon_cancellations VALUES ('missing','2026-10-31T00:00:00Z','2026-10-01')",
        "want_sqlstate": "23503"
    },
    {
        "name": "same purchase-day cancellation is valid",
        "input_sql": "INSERT INTO addon_cancellations VALUES ('sql-life-first','2026-10-01T00:00:00Z','2026-10-01')",
        "want_sqlstate": "00000"
    },
    {
        "name": "termination is immutable",
        "input_sql": "UPDATE addon_cancellations SET cancelled_at='2026-11-01T00:00:00Z',end_month='2026-11-01' WHERE subscription_id='sql-life-first'",
        "want_sqlstate": "23514"
    },
    {
        "name": "termination cannot be deleted",
        "input_sql": "DELETE FROM addon_cancellations WHERE subscription_id='sql-life-first'",
        "want_sqlstate": "23514"
    },
    {
        "name": "purchase snapshot cannot change",
        "input_sql": "UPDATE addon_subscriptions SET monthly_price_cents=1 WHERE subscription_id='sql-life-first'",
        "want_sqlstate": "23514"
    },
    {
        "name": "purchase cannot be deleted",
        "input_sql": "DELETE FROM addon_subscriptions WHERE subscription_id='sql-life-first'",
        "want_sqlstate": "23514"
    },
    {
        "name": "reactivation in cancellation month would overlap",
        "input_sql": "INSERT INTO addon_subscriptions VALUES ('sql-life-overlap','sql-life','concurrency_pack',2000,'2026-10-31T00:00:00Z','2026-10-01')",
        "want_sqlstate": "23505"
    },
    {
        "name": "first next-month date may reactivate",
        "input_sql": "INSERT INTO addon_subscriptions VALUES ('sql-life-second','sql-life','concurrency_pack',2000,'2026-11-01T00:00:00Z','2026-11-01')",
        "want_sqlstate": "00000"
    },
    {
        "name": "uncancelled second purchase blocks another",
        "input_sql": "INSERT INTO addon_subscriptions VALUES ('sql-life-third','sql-life','concurrency_pack',2000,'2026-12-01T00:00:00Z','2026-12-01')",
        "want_sqlstate": "23505"
    },
    {
        "name": "cancellation of closed month cannot drop future fees",
        "input_sql": "INSERT INTO closed_billing_months VALUES ('sql-life','2026-11-01','2026-12-01T00:00:00Z')",
        "want_sqlstate": "00000"
    },
    {
        "name": "closed cancellation month is rejected",
        "input_sql": "INSERT INTO addon_cancellations VALUES ('sql-life-second','2026-11-30T00:00:00Z','2026-11-01')",
        "want_sqlstate": "23514"
    },
    {
        "name": "first open month after closed period is valid",
        "input_sql": "INSERT INTO addon_cancellations VALUES ('sql-life-second','2026-12-01T00:00:00Z','2026-12-01')",
        "want_sqlstate": "00000"
    }
]$inputs$::jsonb)
    LOOP
        actual_state := '00000';
        BEGIN
            EXECUTE scenario->>'input_sql';
        EXCEPTION WHEN OTHERS THEN
            GET STACKED DIAGNOSTICS actual_state = RETURNED_SQLSTATE;
        END;
        IF actual_state <> scenario->>'want_sqlstate' THEN
            RAISE EXCEPTION '%: SQLSTATE %, expected %, input %',
                scenario->>'name',actual_state,scenario->>'want_sqlstate',scenario->>'input_sql';
        END IF;
        RAISE NOTICE 'Passed: % (SQLSTATE %)',scenario->>'name',actual_state;
    END LOOP;
END;
$scenarios$;
ROLLBACK;
