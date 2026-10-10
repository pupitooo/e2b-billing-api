-- Quarantined receipts remain auditable but never delay invoice publication.
-- Keep exclusion separate from immutable cohort membership and mutable inbox errors.
CREATE TABLE invoice_closing_exclusions (
    customer_id text NOT NULL,
    billing_month date NOT NULL,
    source text NOT NULL,
    event_id text NOT NULL,
    processing_error text NOT NULL,
    PRIMARY KEY (customer_id, billing_month, source, event_id),
    FOREIGN KEY (customer_id, billing_month, source, event_id)
        REFERENCES invoice_closing_receipts
);

INSERT INTO invoice_closing_exclusions
SELECT c.customer_id, c.billing_month, c.source, c.event_id, i.processing_error
FROM invoice_closing_receipts c JOIN usage_inbox i USING (source, event_id)
WHERE i.processing_error IS NOT NULL;

CREATE TRIGGER invoice_closing_exclusions_append_only
    BEFORE UPDATE OR DELETE ON invoice_closing_exclusions
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();
