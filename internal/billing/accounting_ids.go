package billing

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const (
	groupIDSequence       = "rated_usage_group_id_seq"
	groupIDPrefix         = "grp_"
	creditEntryIDSequence = "credit_entry_id_seq"
	creditEntryIDPrefix   = "crd_"
)

// nextAccountingID allocates a noncycling PostgreSQL sequence value. Existing
// rows are found by their business identity before allocation; no random
// collision lookup or regeneration is required.
func nextAccountingID(ctx context.Context, tx pgx.Tx, sequence, prefix string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, "SELECT $1::text || nextval($2::regclass)::text", prefix, sequence).Scan(&id)

	return id, err
}
