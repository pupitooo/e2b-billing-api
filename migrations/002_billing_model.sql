-- One tick is one millionth of a USD cent (0.00000001 USD).
-- Unconstrained numeric retains integer products beyond bigint without rounding.
CREATE DOMAIN billing_ticks AS numeric
    CHECK (VALUE >= 0 AND VALUE < 'Infinity'::numeric AND VALUE = trunc(VALUE));

CREATE TABLE customers (
    customer_id text PRIMARY KEY,
    name text NOT NULL,
    country text NOT NULL,
    billing_address text NOT NULL
);

-- Future financial writers lock this row before changing a customer's account.
CREATE TABLE customer_billing_state (
    customer_id text PRIMARY KEY REFERENCES customers (customer_id),
    credit_balance_cents bigint NOT NULL CHECK (credit_balance_cents >= 0),
    spend_limit_cents bigint CHECK (spend_limit_cents >= 0),
    state_version bigint NOT NULL DEFAULT 0 CHECK (state_version >= 0)
);

CREATE TABLE metrics (
    metric text PRIMARY KEY
);

-- A NULL customer denotes a default price, not a customer named "default".
-- A new version changes future prices while preserving historical versions.
CREATE TABLE price_versions (
    price_version_id text PRIMARY KEY,
    customer_id text REFERENCES customers (customer_id),
    metric text NOT NULL REFERENCES metrics (metric),
    price_per_million_cents bigint NOT NULL CHECK (price_per_million_cents >= 0),
    effective_from timestamptz NOT NULL CHECK (isfinite(effective_from)),
    UNIQUE NULLS NOT DISTINCT (customer_id, metric, effective_from),
    UNIQUE (price_version_id, metric)
);

-- Grouping before cent rounding keeps a charge independent of event splitting.
-- Separate months retain the origin of usage delivered after its month closes.
CREATE TABLE rated_usage_groups (
    group_id text PRIMARY KEY,
    customer_id text NOT NULL REFERENCES customers (customer_id),
    price_version_id text NOT NULL,
    metric text NOT NULL,
    usage_month date NOT NULL CHECK (isfinite(usage_month) AND EXTRACT(DAY FROM usage_month) = 1),
    billing_month date NOT NULL CHECK (isfinite(billing_month) AND EXTRACT(DAY FROM billing_month) = 1),
    total_units numeric NOT NULL CHECK (
        total_units >= 0 AND total_units < 'Infinity'::numeric AND total_units = trunc(total_units)
    ),
    exact_charge_ticks billing_ticks NOT NULL,
    booked_charge_cents bigint NOT NULL CHECK (booked_charge_cents >= 0),
    allocated_credit_cents bigint NOT NULL CHECK (
        allocated_credit_cents >= 0 AND allocated_credit_cents <= booked_charge_cents
    ),
    CHECK (billing_month >= usage_month),
    FOREIGN KEY (price_version_id, metric) REFERENCES price_versions (price_version_id, metric),
    UNIQUE (customer_id, price_version_id, usage_month, billing_month),
    UNIQUE (group_id, customer_id)
);

-- Each inbox identity can contribute to a rated group only once.
CREATE TABLE usage_ratings (
    source text NOT NULL,
    event_id text NOT NULL,
    group_id text NOT NULL REFERENCES rated_usage_groups (group_id),
    PRIMARY KEY (source, event_id),
    FOREIGN KEY (source, event_id) REFERENCES usage_inbox (source, event_id)
);

CREATE INDEX usage_ratings_group_idx ON usage_ratings (group_id);

-- Gross consumption belongs to the usage month, before credit and add-ons.
CREATE TABLE monthly_usage (
    customer_id text NOT NULL REFERENCES customers (customer_id),
    usage_month date NOT NULL CHECK (isfinite(usage_month) AND EXTRACT(DAY FROM usage_month) = 1),
    gross_charge_ticks billing_ticks NOT NULL,
    PRIMARY KEY (customer_id, usage_month)
);

-- Positive grants have no usage group; negative debits explain consumed credit.
CREATE TABLE credit_entries (
    credit_entry_id text PRIMARY KEY,
    customer_id text NOT NULL REFERENCES customers (customer_id),
    operation_id text NOT NULL,
    group_id text,
    amount_cents bigint NOT NULL,
    recorded_at timestamptz NOT NULL CHECK (isfinite(recorded_at)),
    CHECK (
        (group_id IS NULL AND amount_cents > 0)
        OR (group_id IS NOT NULL AND amount_cents < 0)
    ),
    FOREIGN KEY (group_id, customer_id) REFERENCES rated_usage_groups (group_id, customer_id),
    UNIQUE (customer_id, operation_id)
);

CREATE INDEX credit_entries_group_idx ON credit_entries (group_id) WHERE group_id IS NOT NULL;

CREATE TABLE addons (
    addon_name text PRIMARY KEY,
    monthly_price_cents bigint NOT NULL CHECK (monthly_price_cents >= 0)
);

-- The purchase snapshots the monthly price; one subscription per add-on is supported.
CREATE TABLE addon_subscriptions (
    subscription_id text PRIMARY KEY,
    customer_id text NOT NULL REFERENCES customers (customer_id),
    addon_name text NOT NULL REFERENCES addons (addon_name),
    monthly_price_cents bigint NOT NULL CHECK (monthly_price_cents >= 0),
    purchased_at timestamptz NOT NULL CHECK (isfinite(purchased_at)),
    start_month date NOT NULL CHECK (
        isfinite(start_month) AND start_month = date_trunc('month', purchased_at AT TIME ZONE 'UTC')::date
    ),
    UNIQUE (customer_id, addon_name)
);

-- Preserve financial history by rejecting edits; future changes append new records.
CREATE FUNCTION reject_billing_history_change() RETURNS trigger
LANGUAGE plpgsql AS $function$
BEGIN
    RAISE EXCEPTION '% is append-only; append a new record instead', TG_TABLE_NAME
        USING ERRCODE = '23514';
END;
$function$;

CREATE TRIGGER price_versions_append_only
    BEFORE UPDATE OR DELETE ON price_versions
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();

CREATE TRIGGER credit_entries_append_only
    BEFORE UPDATE OR DELETE ON credit_entries
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();

CREATE TRIGGER usage_ratings_append_only
    BEFORE UPDATE OR DELETE ON usage_ratings
    FOR EACH ROW EXECUTE FUNCTION reject_billing_history_change();

-- A default price can rate any customer; an override can rate only its owner.
-- Preserve the grouping identity while allowing its running totals to change.
CREATE FUNCTION check_rated_group_identity() RETURNS trigger
LANGUAGE plpgsql AS $function$
DECLARE
    price_customer text;
BEGIN
    IF TG_OP = 'UPDATE' AND (
        NEW.group_id, NEW.customer_id, NEW.price_version_id, NEW.metric, NEW.usage_month, NEW.billing_month
    ) IS DISTINCT FROM (
        OLD.group_id, OLD.customer_id, OLD.price_version_id, OLD.metric, OLD.usage_month, OLD.billing_month
    ) THEN
        RAISE EXCEPTION 'A rated group identity cannot change'
            USING ERRCODE = '23514';
    END IF;

    SELECT customer_id INTO price_customer FROM price_versions
    WHERE price_version_id = NEW.price_version_id;

    IF price_customer IS NOT NULL AND price_customer <> NEW.customer_id THEN
        RAISE EXCEPTION 'A customer price cannot rate another customer'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER rated_group_identity
    BEFORE INSERT OR UPDATE ON rated_usage_groups
    FOR EACH ROW EXECUTE FUNCTION check_rated_group_identity();

-- Link only matching customer/metric segments wholly inside the group's UTC month.
-- Price selection and price-boundary segmentation belong to the future Go rater.
CREATE FUNCTION check_usage_rating_receipt() RETURNS trigger
LANGUAGE plpgsql AS $function$
DECLARE
    receipt usage_inbox%ROWTYPE;
    rated_group rated_usage_groups%ROWTYPE;
    month_start timestamptz;
    month_end timestamptz;
BEGIN
    SELECT * INTO receipt FROM usage_inbox
    WHERE source = NEW.source AND event_id = NEW.event_id;
    SELECT * INTO rated_group FROM rated_usage_groups WHERE group_id = NEW.group_id;

    -- Foreign keys report missing identities and groups after this trigger.
    IF receipt.source IS NULL OR rated_group.group_id IS NULL THEN
        RETURN NEW;
    END IF;

    month_start := rated_group.usage_month::timestamp AT TIME ZONE 'UTC';
    month_end := (rated_group.usage_month + INTERVAL '1 month') AT TIME ZONE 'UTC';
    IF receipt.customer_id <> rated_group.customer_id OR receipt.metric <> rated_group.metric
        OR NOT isfinite(receipt.period_start) OR NOT isfinite(receipt.period_end)
        OR receipt.period_start < month_start OR receipt.period_end > month_end THEN
        RAISE EXCEPTION 'A usage rating must match its customer, metric, and UTC usage month'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER usage_rating_receipt
    BEFORE INSERT ON usage_ratings
    FOR EACH ROW EXECUTE FUNCTION check_usage_rating_receipt();
