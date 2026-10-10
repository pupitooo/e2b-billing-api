\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TIME ZONE :'test_timezone';

-- Private transaction-owned identities avoid assumptions about application state.
INSERT INTO customers VALUES ('sql-financial-test','SQL financial test','US','Test address');
INSERT INTO customer_billing_state (customer_id,credit_balance_ticks,spend_limit_cents,state_version)
VALUES ('sql-financial-test',0,NULL,0);
INSERT INTO spend_limit_operations VALUES ('sql-financial-test','original',1500,'2026-10-01T00:00:00Z');
INSERT INTO closed_billing_months VALUES ('sql-financial-test','2026-10-01','2026-11-01T00:00:00Z');
INSERT INTO invoices VALUES ('sql-financial-test','2026-10-01','SQL-FINANCIAL-0001',0,'{}');

-- Owned cohort and exclusion exercise immutable quarantine audit independently
-- of mutable processing_error in the inbox.
INSERT INTO usage_inbox
(source,event_id,schema_version,customer_id,sandbox_id,metric,period_start,period_end,units,received_at,processing_error)
VALUES ('sql-exclusion','one',1,'sql-financial-test','sandbox','cpu_seconds',
        '2026-10-10T12:00:00Z','2026-10-10T13:00:00Z',0,'2026-10-11T00:00:00Z','investigated error');
INSERT INTO invoice_closings VALUES ('sql-financial-test','2026-10-01','2026-11-01T00:00:00Z');
INSERT INTO invoice_closing_receipts VALUES ('sql-financial-test','2026-10-01','sql-exclusion','one');
INSERT INTO invoice_closing_exclusions VALUES ('sql-financial-test','2026-10-01','sql-exclusion','one','investigated error');

DO $scenarios$
DECLARE
    scenario jsonb;
    actual_state text;
BEGIN
    FOR scenario IN SELECT value FROM jsonb_array_elements($inputs$[
        {"name":"closing exclusion cannot change","input_sql":"UPDATE invoice_closing_exclusions SET processing_error='changed' WHERE source='sql-exclusion'","want_sqlstate":"23514"},
        {"name":"closing exclusion cannot be deleted","input_sql":"DELETE FROM invoice_closing_exclusions WHERE source='sql-exclusion'","want_sqlstate":"23514"},
        {"name":"closing exclusion is unique","input_sql":"INSERT INTO invoice_closing_exclusions VALUES ('sql-financial-test','2026-10-01','sql-exclusion','one','duplicate')","want_sqlstate":"23505"},
        {"name":"closing exclusion requires a cohort member","input_sql":"INSERT INTO invoice_closing_exclusions VALUES ('sql-financial-test','2026-10-01','sql-exclusion','missing','error')","want_sqlstate":"23503"},
        {"name":"closing exclusion requires an error","input_sql":"INSERT INTO invoice_closing_exclusions VALUES ('sql-financial-test','2026-10-01','sql-exclusion','one',NULL)","want_sqlstate":"23502"},
        {"name":"operator release preserves closing exclusion","input_sql":"UPDATE usage_inbox SET processing_error=NULL WHERE source='sql-exclusion' AND event_id='one'","want_sqlstate":"00000"},
        {"name":"negative limit is rejected","input_sql":"INSERT INTO spend_limit_operations VALUES ('sql-financial-test','negative',-1,'2026-10-01T00:00:00Z')","want_sqlstate":"23514"},
        {"name":"limit operation identity is unique","input_sql":"INSERT INTO spend_limit_operations VALUES ('sql-financial-test','original',2000,'2026-10-01T00:00:00Z')","want_sqlstate":"23505"},
        {"name":"limit operation history cannot change","input_sql":"UPDATE spend_limit_operations SET limit_cents=2000 WHERE customer_id='sql-financial-test'","want_sqlstate":"23514"},
        {"name":"issued invoice cannot change","input_sql":"UPDATE invoices SET total_cents=1 WHERE customer_id='sql-financial-test'","want_sqlstate":"23514"},
        {"name":"issued invoice cannot be deleted","input_sql":"DELETE FROM invoices WHERE customer_id='sql-financial-test'","want_sqlstate":"23514"},
        {"name":"month closure cannot change","input_sql":"UPDATE closed_billing_months SET closed_at='2026-12-01T00:00:00Z' WHERE customer_id='sql-financial-test'","want_sqlstate":"23514"},
        {"name":"another closing day cannot close the same customer month again","input_sql":"INSERT INTO closed_billing_months VALUES ('sql-financial-test','2026-10-01','2026-11-20T00:00:00Z')","want_sqlstate":"23505"},
        {"name":"another invoice number cannot invoice the same customer month again","input_sql":"INSERT INTO invoices VALUES ('sql-financial-test','2026-10-01','SQL-FINANCIAL-0002',0,'{}')","want_sqlstate":"23505"},
        {"name":"billing periods cannot start on the twentieth day","input_sql":"INSERT INTO closed_billing_months VALUES ('sql-financial-test','2026-11-20','2026-12-20T00:00:00Z')","want_sqlstate":"23514"},
        {"name":"different billing months may close in the same calendar month","input_sql":"INSERT INTO closed_billing_months VALUES ('sql-financial-test','2026-11-01','2026-11-20T00:00:00Z')","want_sqlstate":"00000"},
        {"name":"a different billing month has its own invoice","input_sql":"INSERT INTO invoices VALUES ('sql-financial-test','2026-11-01','SQL-FINANCIAL-0002',0,'{}')","want_sqlstate":"00000"}
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
DO $audit$
BEGIN
    IF (SELECT processing_error FROM invoice_closing_exclusions WHERE source='sql-exclusion' AND event_id='one') IS DISTINCT FROM 'investigated error' THEN
        RAISE EXCEPTION 'Operator release changed the original exclusion error';
    END IF;
END;
$audit$;

ROLLBACK;
