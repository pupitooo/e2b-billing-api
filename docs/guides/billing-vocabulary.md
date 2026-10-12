# Billing vocabulary

Use the database table name, or its exact description, for persisted data. Use `usage event` for a platform measurement and `usage_inbox row` for that event after ingestion. There is no additional documentation entity called a receipt. A row lock locks a whole row; `table.column` identifies the key used to select it, not a separately locked cell.

| Preferred term | Meaning and storage |
| --- | --- |
| `usage event` | One platform measurement, identified by `source` and `event_id`. Before persistence it is an input event, not a database row. |
| `usage batch` | The transport envelope containing usage events for `POST /usage/batches`. There is no batch table. |
| `usage_inbox` / `usage_inbox row` | Stored measurement plus its original `received_at` and processing state. Primary key: `usage_inbox.source`, `usage_inbox.event_id`. |
| Pending `usage_inbox row` | Both `usage_inbox.processed_at` and `usage_inbox.processing_error` are NULL. |
| Processed `usage_inbox row` | `usage_inbox.processed_at` is set; financial effects committed with it. |
| Quarantined `usage_inbox row` | `usage_inbox.processing_error` is non-NULL; the worker does not select it until explicit release. |
| `rating` | The Go calculation that selects a historical price and calculates exact charges. It is an operation, not a table. |
| `price_versions` | Historical prices and customer overrides. Rows are identified by `price_versions.price_version_id`. |
| `rated_usage_groups` | Stored totals keyed by customer, price version, `usage_month` and `billing_month`. Their primary key is `rated_usage_groups.group_id`; the worker performs the calculation. |
| `usage_ratings` | Links from `usage_ratings.source` and `usage_ratings.event_id` to `usage_ratings.group_id`. |
| `customer_billing_state` | The account row selected by `customer_billing_state.customer_id`, holding credit, limit, `state_version` and `next_invoice_number`. |
| Credit balance | `customer_billing_state.credit_balance_microcents`. |
| Credit grant / credit allocation | Adding available credit / consuming exact available credit during rating. Their audit records are `credit_entries` rows. |
| `credit_entries` | Append-only grant and debit history, identified by `credit_entries.credit_entry_id`. |
| `monthly_usage` | Gross usage by `monthly_usage.customer_id` and original `monthly_usage.usage_month`, before credit and add-ons. |
| Spend limit / limit status | Configuration in `customer_billing_state.spend_limit_cents` / calculated status returned by the limit-status endpoint. Status is not another persisted table. |
| `addons` | The add-on catalog selected by `addons.addon_name`. |
| `addon_subscriptions` / `addon_cancellations` | Purchased activation periods / immutable terminations, selected by their `subscription_id`. |
| `closed_billing_months` | Committed closures identified by `closed_billing_months.customer_id` and `closed_billing_months.billing_month`. |
| `invoices` | Immutable invoice rows identified by `invoices.customer_id` and `invoices.billing_month`. |
| Invoice line | An item in `invoices.snapshot.lines`, not a separate table. |
| `invoiced_usage_groups` | Frozen group links selected by `invoiced_usage_groups.group_id`. |
| `api_idempotency_operations` | Successful command inputs and results keyed by `api_idempotency_operations.operation_scope` and `api_idempotency_operations.idempotency_key`. |
| `schema_migrations` | Applied migration versions selected by `schema_migrations.version`. |
| `usage_month` / `billing_month` | Original UTC consumption month / assigned UTC invoice month. The names are columns, not two separate kinds of invoice. |
| Monthly closing / invoice catch-up | Publishing one month's invoice / recovering missing consecutive monthly invoices. |
| Customer / sandbox / metric | A customer is represented by `customers`; metrics by `metrics`. `usage_inbox.sandbox_id` is an event attribute; this schema has no sandbox table. |

Use phrases such as **row lock on `customer_billing_state.customer_id`**, **row lock on `(usage_inbox.source, usage_inbox.event_id)`** and **exclusive catalog advisory lock `(65_102, 2)`**. Advisory locks are keyed PostgreSQL resources, not column locks. The request advisory lock uses the scope/key identity associated with `api_idempotency_operations`, while its full primary key determines replay correctness.

SQL column names, public JSON fields, Go identifiers, test filters and existing file paths retain their actual spellings. For example, `received_at` is the arrival timestamp, and an internal Go type named `receipt` is a storage helper for a `usage_inbox row`, not an additional domain concept. Logical proposal diagrams remain labeled as proposals; their fields are not the implemented schema.
