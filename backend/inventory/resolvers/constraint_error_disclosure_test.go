package resolvers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// rawConstraintText is what a Postgres integrity-constraint error carries and
// a client must never see: the driver prefix, the violation wording, and the
// table and constraint names it quotes.
var rawConstraintText = []string{
	"pq:", "violates", "constraint \"", "item_tenant_id_sku", "item_movements", "gen: constraint failed",
}

// TestConstraintErrorsHideSchema pins that a database constraint violation
// reaches the client as a generic message. The raw Postgres error names the
// table and the constraint, which maps the schema for any caller.
func TestConstraintErrorsHideSchema(t *testing.T) {
	t.Parallel()

	t.Run("duplicate sku", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)
		item := te.newItem(ctx, userA).Sku("constraint-dup-sku").Create()

		res := resolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, ctx, createItem, map[string]any{
			"Sku":        item.Sku,
			"DataTypeID": item.DataTypeID,
		})
		require.NotEmpty(t, res.Errors, "a duplicate sku must be refused")
		msg := res.Errors[0].Message
		assert.Equal(t, "duplicate key: record already exists (SQLSTATE 23505)", msg)
		for _, raw := range rawConstraintText {
			assert.NotContains(t, msg, raw, "the refusal leaks database internals: %q", msg)
		}
	})

	t.Run("unknown item on a collection movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)
		from := te.newRepository(ctx, userA).Virtual(true).Create()
		to := te.newRepository(ctx, userA).Create()
		ghost := uuid.New()

		res := resolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, ctx, createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": []collectionMovementItem{{
				ItemID: &ghost, FromID: from.ID, ToID: to.ID, Handler: "constraint-ghost", Quantity: ptrInt64(1),
			}},
		})
		require.NotEmpty(t, res.Errors, "an unknown item must be refused")
		msg := res.Errors[0].Message
		for _, raw := range rawConstraintText {
			assert.NotContains(t, msg, raw, "the refusal leaks database internals: %q", msg)
		}
	})
}
