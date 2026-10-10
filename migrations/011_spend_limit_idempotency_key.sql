-- Keep existing request identities and append-only history while matching the
-- JSON API name. Credit ledger operation_id remains an internal grant/debit key.
ALTER TABLE spend_limit_operations RENAME COLUMN operation_id TO idempotency_key;
