package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/main-data/resolvers"
)

// TestFindPickingOrderByCustomerID covers the federated PickingOrder.customer
// relation resolver (issue #1202): a live customer resolves the relation, while
// a soft-deleted or missing customer resolves to a nil relation with the
// customerID pointer always retained.
func TestFindPickingOrderByCustomerID(t *testing.T) {
	t.Parallel()

	t.Run("live customer resolves the relation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)
		entity := resolvers.NewResolver("main-data", te.Ent, validator.NewValidator(te.DataTypeProvider)).Entity()

		customer := te.newCustomer(ctx, userA).Create()

		order, err := entity.FindPickingOrderByCustomerID(ctx, customer.ID)
		require.NoError(t, err)
		assert.Equal(t, customer.ID, order.CustomerID, "customerID pointer is retained")
		require.NotNil(t, order.Customer, "relation should resolve for a live customer")
		assert.Equal(t, customer.ID, order.Customer.ID)
	})

	t.Run("soft-deleted customer resolves to a nil relation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)
		entity := resolvers.NewResolver("main-data", te.Ent, validator.NewValidator(te.DataTypeProvider)).Entity()

		customer := te.newCustomer(ctx, userA).Deleted().Create()

		order, err := entity.FindPickingOrderByCustomerID(ctx, customer.ID)
		require.NoError(t, err)
		assert.Equal(t, customer.ID, order.CustomerID, "customerID pointer is retained after soft-delete")
		assert.Nil(t, order.Customer, "relation should be nil for a soft-deleted customer")
	})

	t.Run("missing customer resolves to a nil relation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)
		entity := resolvers.NewResolver("main-data", te.Ent, validator.NewValidator(te.DataTypeProvider)).Entity()

		missingID := uuid.New()

		order, err := entity.FindPickingOrderByCustomerID(ctx, missingID)
		require.NoError(t, err)
		assert.Equal(t, missingID, order.CustomerID)
		assert.Nil(t, order.Customer, "relation should be nil for an unknown customerID")
	})
}
