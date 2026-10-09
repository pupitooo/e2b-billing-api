-- A durable cohort captures committed receipts, rather than using receipt time
-- as a watermark for requests whose transactions may still be in flight.
CREATE TABLE invoice_closings (
    customer_id text NOT NULL REFERENCES customers (customer_id),
    billing_month date NOT NULL CHECK (isfinite(billing_month) AND EXTRACT(DAY FROM billing_month) = 1),
    started_at timestamptz NOT NULL CHECK (isfinite(started_at)),
    PRIMARY KEY (customer_id, billing_month)
);
CREATE TABLE invoice_closing_receipts (
    customer_id text NOT NULL,
    billing_month date NOT NULL,
    source text NOT NULL,
    event_id text NOT NULL,
    PRIMARY KEY (customer_id, billing_month, source, event_id),
    FOREIGN KEY (customer_id, billing_month) REFERENCES invoice_closings,
    FOREIGN KEY (source, event_id) REFERENCES usage_inbox
);

ALTER TABLE customer_billing_state ADD COLUMN next_invoice_number bigint NOT NULL DEFAULT 1 CHECK (next_invoice_number > 0);

CREATE TABLE invoices (
    customer_id text NOT NULL,
    billing_month date NOT NULL,
    invoice_number text NOT NULL,
    total_cents bigint NOT NULL CHECK (total_cents >= 0),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    PRIMARY KEY (customer_id, billing_month),
    UNIQUE (customer_id, invoice_number),
    FOREIGN KEY (customer_id, billing_month) REFERENCES closed_billing_months
);
CREATE TABLE invoiced_usage_groups (
    group_id text PRIMARY KEY REFERENCES rated_usage_groups (group_id),
    customer_id text NOT NULL,
    billing_month date NOT NULL,
    FOREIGN KEY (customer_id, billing_month) REFERENCES invoices
);

CREATE TRIGGER invoice_closings_append_only BEFORE UPDATE OR DELETE ON invoice_closings
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
CREATE TRIGGER invoice_closing_receipts_append_only BEFORE UPDATE OR DELETE ON invoice_closing_receipts
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
CREATE TRIGGER invoices_append_only BEFORE UPDATE OR DELETE ON invoices
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
CREATE TRIGGER invoiced_usage_groups_append_only BEFORE UPDATE OR DELETE ON invoiced_usage_groups
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();

CREATE FUNCTION reject_frozen_group_change() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    IF EXISTS (SELECT 1 FROM invoiced_usage_groups WHERE group_id = OLD.group_id) THEN
        RAISE EXCEPTION 'An invoiced usage group is immutable' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$function$;
CREATE TRIGGER frozen_usage_group BEFORE UPDATE OR DELETE ON rated_usage_groups
    FOR EACH ROW EXECUTE FUNCTION reject_frozen_group_change();
