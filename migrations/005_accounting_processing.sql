-- Closing records are read under the same account lock as usage accounting.
CREATE TABLE closed_billing_months (
    customer_id text NOT NULL REFERENCES customers (customer_id),
    billing_month date NOT NULL CHECK (isfinite(billing_month) AND EXTRACT(DAY FROM billing_month) = 1),
    closed_at timestamptz NOT NULL CHECK (isfinite(closed_at)),
    PRIMARY KEY (customer_id, billing_month)
);

CREATE TRIGGER closed_billing_months_append_only
    BEFORE UPDATE OR DELETE ON closed_billing_months
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
