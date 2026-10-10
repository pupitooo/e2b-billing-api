-- Closing now snapshots committed rated groups in one account-locked transaction.
-- Keep usage, financial history and issued invoices; retire the old pending-usage
-- capture and exclusion bookkeeping, including unfinished closing attempts.
DROP TABLE invoice_closing_exclusions;
DROP TABLE invoice_closing_receipts;
DROP TABLE invoice_closings;

-- Check whether the first invoice would skip already known earlier usage,
-- without scanning the backlog or processing it during closing.
CREATE INDEX usage_inbox_customer_period_start_idx ON usage_inbox (customer_id, period_start);
