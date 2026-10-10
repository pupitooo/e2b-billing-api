# Accounting quarantine and historical price recovery

## Investigate and release a receipt

The worker accounts for accepted usage asynchronously. Financial effects and
inbox completion commit together; database failures roll back for retry.
See the [financial rules — Transaction and closing contract](../../docs/architecture/accounting-rules.md#transaction-and-closing-contract)
for locking, atomicity, and closing guarantees, and the
[usage-to-invoice guide — Historical rating](../../docs/architecture/usage-to-invoice.md#4-rating-the-cost-under-the-historical-price)
for pricing and supported interval boundaries.

Unsupported input remains in `usage_inbox.processing_error` until explicitly
released. A missing valid price is a **P0 catalog incident**, reported in worker
JSON logs with `priority=P0` and `error_code=missing_valid_price`. It quarantines the
receipt without financial effects and never blocks invoice issuance. Follow the
[financial rules — Catalog provisioning and P0 recovery](../../docs/architecture/accounting-rules.md#catalog-provisioning-and-p0-recovery)
for the complete diagnostic and recovery contract.

To recover, correct the cause first. Missing historical prices require the
[controlled database repair](../guides/accounting-recovery.md#controlled-historical-price-repair); ordinary
`POST /prices` rejects backdated versions. Then explicitly release only the
investigated receipt:

```sql
UPDATE usage_inbox SET processing_error = NULL
WHERE source = 'investigated-source' AND event_id = 'investigated-event'
  AND processed_at IS NULL;
```

Adding a price or replaying the usage request does not clear a processing error.

### Controlled historical price repair

Initial seed data and investigated missing-price repairs are the only historical
catalog provisioning paths. Ordinary API commands cannot backdate a version.
The example below must be adapted to the investigated customer, metric, receipt,
price, and original effective time. It locks the account before the catalog,
checks the resulting catalog against every affected rated receipt, and rolls
back if it would reprice or invalidate an interval. It does not recalculate
charges, credit, or issued invoices. Run it through `make psql`.

```sql
BEGIN;
SELECT customer_id FROM customer_billing_state
WHERE customer_id = 'investigated-customer' FOR UPDATE;
SELECT pg_advisory_xact_lock(65102, 2);

INSERT INTO price_versions VALUES (
    'investigated-price', NULL, 'investigated-metric', 5,
    '2026-10-01T00:00:00Z'
);

DO $repair$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM usage_ratings r
        JOIN usage_inbox i USING (source, event_id)
        JOIN rated_usage_groups g USING (group_id)
        LEFT JOIN LATERAL (
            SELECT p.* FROM price_versions p
            WHERE p.metric = i.metric
              AND (p.customer_id IS NULL OR p.customer_id = i.customer_id)
              AND p.effective_from <= i.period_start
            ORDER BY (p.customer_id IS NOT NULL) DESC, p.effective_from DESC
            LIMIT 1
        ) chosen ON true
        WHERE i.metric = 'investigated-metric'
          AND (chosen.price_version_id IS DISTINCT FROM g.price_version_id
               OR EXISTS (
                   SELECT 1 FROM price_versions boundary
                   WHERE boundary.metric = i.metric
                     AND (boundary.customer_id IS NULL OR boundary.customer_id = i.customer_id)
                     AND boundary.effective_from > i.period_start
                     AND boundary.effective_from < i.period_end
                     AND (chosen.customer_id IS NULL OR boundary.customer_id = chosen.customer_id)
               ))
    ) THEN
        RAISE EXCEPTION 'Repair would invalidate rated history';
    END IF;
END;
$repair$;

UPDATE usage_inbox SET processing_error = NULL
WHERE source = 'investigated-source' AND event_id = 'investigated-event'
  AND customer_id = 'investigated-customer' AND processed_at IS NULL;
COMMIT;
```

Review the transaction's affected rows before committing. Repaired usage retains its historical price and usage month but routes to an
eligible open billing month; issued invoices remain immutable.
