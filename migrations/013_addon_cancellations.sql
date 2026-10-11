-- Purchases retain their price and identity. Termination appends a separate
-- immutable record; its month remains fully billable without proration.
CREATE TABLE addon_cancellations (
    subscription_id text PRIMARY KEY REFERENCES addon_subscriptions (subscription_id),
    cancelled_at timestamptz NOT NULL CHECK (isfinite(cancelled_at)),
    end_month date NOT NULL CHECK (
        isfinite(end_month) AND end_month = date_trunc('month', cancelled_at AT TIME ZONE 'UTC')::date
    )
);

ALTER TABLE addon_subscriptions DROP CONSTRAINT addon_subscriptions_customer_id_addon_name_key;

CREATE FUNCTION check_addon_purchase_period() RETURNS trigger
LANGUAGE plpgsql AS $function$
BEGIN
    -- Serialize direct SQL and application writes on the same customer boundary.
    PERFORM 1 FROM customer_billing_state WHERE customer_id = NEW.customer_id FOR UPDATE;
    IF EXISTS (
        SELECT 1 FROM addon_subscriptions s LEFT JOIN addon_cancellations c USING (subscription_id)
        WHERE s.customer_id = NEW.customer_id AND s.addon_name = NEW.addon_name
            AND (c.end_month IS NULL OR c.end_month >= NEW.start_month)
    ) THEN
        RAISE EXCEPTION 'An add-on purchase must start after all earlier active months'
            USING ERRCODE = '23505';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER addon_purchase_period
    BEFORE INSERT ON addon_subscriptions
    FOR EACH ROW EXECUTE FUNCTION check_addon_purchase_period();

CREATE FUNCTION check_addon_cancellation() RETURNS trigger
LANGUAGE plpgsql AS $function$
DECLARE
    purchase addon_subscriptions%ROWTYPE;
BEGIN
    SELECT * INTO purchase FROM addon_subscriptions WHERE subscription_id = NEW.subscription_id;
    IF purchase.subscription_id IS NULL THEN
        RETURN NEW; -- The foreign key reports a missing purchase.
    END IF;
    PERFORM 1 FROM customer_billing_state WHERE customer_id = purchase.customer_id FOR UPDATE;
    IF NEW.cancelled_at < purchase.purchased_at THEN
        RAISE EXCEPTION 'Cancellation cannot precede purchase' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM closed_billing_months
        WHERE customer_id = purchase.customer_id AND billing_month >= NEW.end_month) THEN
        RAISE EXCEPTION 'Cancellation cannot alter a closed billing period' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER addon_cancellation_period
    BEFORE INSERT ON addon_cancellations
    FOR EACH ROW EXECUTE FUNCTION check_addon_cancellation();

CREATE TRIGGER addon_cancellations_append_only
    BEFORE UPDATE OR DELETE ON addon_cancellations
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();

CREATE TRIGGER addon_subscriptions_append_only
    BEFORE UPDATE OR DELETE ON addon_subscriptions
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
