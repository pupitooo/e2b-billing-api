-- Allocate distinct positive IDs without random collision checks. Business keys,
-- existing IDs, foreign keys, accounting values, and frozen invoices stay intact.
CREATE SEQUENCE rated_usage_group_id_seq AS bigint START WITH 1 NO CYCLE
    OWNED BY rated_usage_groups.group_id;

CREATE SEQUENCE credit_entry_id_seq AS bigint START WITH 1 NO CYCLE
    OWNED BY credit_entries.credit_entry_id;

-- Reserve any earlier IDs already using the new canonical decimal format.
-- Larger legacy suffixes cannot be produced by a bigint sequence and stay intact.
WITH existing AS (
    SELECT substring(group_id FROM '^grp_([1-9][0-9]*)$')::numeric AS id
    FROM rated_usage_groups
), reserved AS (
    SELECT max(id) AS last_id FROM existing
    WHERE id <= (SELECT seqmax FROM pg_sequence WHERE seqrelid = 'rated_usage_group_id_seq'::regclass)
)
SELECT setval('rated_usage_group_id_seq', COALESCE(last_id, 1)::bigint, last_id IS NOT NULL)
FROM reserved;

WITH existing AS (
    SELECT substring(credit_entry_id FROM '^crd_([1-9][0-9]*)$')::numeric AS id
    FROM credit_entries
), reserved AS (
    SELECT max(id) AS last_id FROM existing
    WHERE id <= (SELECT seqmax FROM pg_sequence WHERE seqrelid = 'credit_entry_id_seq'::regclass)
)
SELECT setval('credit_entry_id_seq', COALESCE(last_id, 1)::bigint, last_id IS NOT NULL)
FROM reserved;
