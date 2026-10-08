-- Remove text checks from databases initialized before the application validation decision.
ALTER TABLE usage_inbox
    DROP CONSTRAINT IF EXISTS usage_inbox_source_check,
    DROP CONSTRAINT IF EXISTS usage_inbox_event_id_check,
    DROP CONSTRAINT IF EXISTS usage_inbox_customer_id_check,
    DROP CONSTRAINT IF EXISTS usage_inbox_sandbox_id_check,
    DROP CONSTRAINT IF EXISTS usage_inbox_metric_check,
    DROP CONSTRAINT IF EXISTS usage_inbox_processing_error_check;
