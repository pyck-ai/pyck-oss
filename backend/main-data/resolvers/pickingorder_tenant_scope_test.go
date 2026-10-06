package resolvers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// entitiesPickingOrderCustomer is the representation the router sends to
// resolve PickingOrder.customer: the order's customerID and its tenantID.
var entitiesPickingOrderCustomer = resolver.ParseTemplate(`query {
	_entities(representations: [{__typename: "PickingOrder", customerID: "{{.CustomerID}}", tenantID: "{{.TenantID}}"}]) {
		... on PickingOrder { customerID customer { id tenantID } }
	}
}`)

type entitiesPickingOrderCustomerData struct {
	Entities []struct {
		CustomerID string `json:"customerID"`
		Customer   *struct {
			ID       string `json:"id"`
			TenantID string `json:"tenantID"`
		} `json:"customer"`
	} `json:"_entities"`
}

// TestPickingOrderCustomerStaysInOrderTenant pins that PickingOrder.customer
// only resolves a customer of the order's own tenant. Cross-service ids are
// not foreign keys, so an order of tenant A can store tenant B's customer id.
// A reader limited to A never saw B's customer, but the system user skips the
// tenant filter and a reader acting in A and B sees both tenants, so the
// lookup by id alone returned B's customer under A's order.
func TestPickingOrderCustomerStaysInOrderTenant(t *testing.T) {
	t.Parallel()

	systemUser := &authn.User{ID: uuid.Max, TenantID: uuid.Max}
	bothUser := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER, tenantB: authn.ROLE_READER},
	}

	te := setup(t)
	customerA := te.newCustomer(te.ctx(userA), userA).Create()
	customerB := te.newCustomer(te.ctx(userB), userB).Create()

	readers := map[string]context.Context{
		"system user":             request.Context(te.ctx(userA), systemUser),
		"user acting in A and B":  request.Context(te.ctx(userA), bothUser, tenantA, tenantB),
		"single-tenant user of A": te.ctx(userA),
	}

	for name, ctx := range readers {
		t.Run(name+": B's customer is not resolved under A's order", func(t *testing.T) {
			t.Parallel()

			data := execOK[entitiesPickingOrderCustomerData](te, ctx, entitiesPickingOrderCustomer,
				map[string]any{"CustomerID": customerB.ID, "TenantID": tenantA})
			require.Len(t, data.Entities, 1)
			assert.Equal(t, customerB.ID.String(), data.Entities[0].CustomerID, "customerID pointer is retained")
			assert.Nil(t, data.Entities[0].Customer, "order of tenant A resolved tenant B's customer")
		})

		t.Run(name+": control, A's customer resolves under A's order", func(t *testing.T) {
			t.Parallel()

			data := execOK[entitiesPickingOrderCustomerData](te, ctx, entitiesPickingOrderCustomer,
				map[string]any{"CustomerID": customerA.ID, "TenantID": tenantA})
			require.Len(t, data.Entities, 1)
			require.NotNil(t, data.Entities[0].Customer)
			assert.Equal(t, customerA.ID.String(), data.Entities[0].Customer.ID)
			assert.Equal(t, tenantA.String(), data.Entities[0].Customer.TenantID)
		})
	}
}
