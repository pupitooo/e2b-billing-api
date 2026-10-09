-- Load only the assignment's initial catalog, not the example's business actions.
INSERT INTO customers (customer_id, name, country, billing_address)
VALUES
    ('acme', 'Acme Inc.', 'US', '1 Market St, San Francisco, CA 94105'),
    ('cyberdyne', 'Cyberdyne Systems Corporation', 'US', '18144 El Camino Real, Sunnyvale, CA 94087');

INSERT INTO customer_billing_state (
    customer_id, credit_balance_cents, spend_limit_cents, state_version
)
VALUES ('acme', 0, NULL, 0), ('cyberdyne', 0, NULL, 0);

INSERT INTO metrics (metric)
VALUES ('cpu_seconds');

INSERT INTO price_versions (
    price_version_id, customer_id, metric, price_per_million_cents, effective_from
)
VALUES
    ('cpu-default-2026-10-01', NULL, 'cpu_seconds', 5, '2026-10-01T00:00:00Z'),
    ('cpu-default-2026-10-15', NULL, 'cpu_seconds', 6, '2026-10-15T00:00:00Z'),
    ('cpu-acme-2026-10-01', 'acme', 'cpu_seconds', 4, '2026-10-01T00:00:00Z');

INSERT INTO addons (addon_name, monthly_price_cents)
VALUES ('concurrency_pack', 2000);
