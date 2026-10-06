//go:build integration

package relationfilterisolation_test

// mUpdateOrderEdges resolves pickingOrder.orderItems on a mutation payload.
// The payload's order is not eager-loaded, so the connection falls back to
// QueryOrderItems().Paginate(...): an edge traversal, not a root query.
const mUpdateOrderEdges = `mutation($id:ID!){updatePickingOrder(id:$id,input:{}){pickingOrder{
	all: orderItems{totalCount edges{node{id}}}
	filtered: orderItems(where:{hasOrderWith:[{id:$id}]}){totalCount edges{node{id}}}
	plain: orderItems(where:{hasOrder:true}){totalCount edges{node{id}}}
}}}`

// TestRelationFilterOnEdgeConnection checks that a relation filter on an
// edge connection matches the caller's own rows. The neighbour scopes read
// the caller from the query context; on an edge traversal ent v0.14.6 left
// the root selector without it, so every such filter matched nothing and the
// connection came back empty with totalCount 0.
func (s *RelationFilterSuite) TestRelationFilterOnEdgeConnection() {
	a := s.provision("A")

	d := s.ok(a.PAT, a.RT.ID, mCreateOrder, nil)
	order := digStr(d, "createPickingOrder", "pickingOrder", "id")
	s.Require().NotEmpty(order)
	d = s.ok(a.PAT, a.RT.ID, mCreateOrderItem, map[string]any{"o": order, "sku": uniq("rf-edge")})
	item := digStr(d, "createPickingOrderItem", "pickingOrderItem", "id")
	s.Require().NotEmpty(item)

	d = s.ok(a.PAT, a.RT.ID, mUpdateOrderEdges, map[string]any{"id": order})
	po := dig(d, "updatePickingOrder", "pickingOrder")
	s.Require().NotNil(po, "mutation payload carries the order")

	for _, alias := range []string{"all", "filtered", "plain"} {
		s.Run(alias, func() {
			s.Equal(1, count(po, alias), "totalCount")
			s.Equal(item, digStr(po, alias, "edges", "0", "node", "id"), "the order's item")
		})
	}
}
