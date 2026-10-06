//go:build integration

package relationfilterisolation_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const (
	mCreateOrder     = `mutation{createPickingOrder(input:{}){pickingOrder{id}}}`
	mCreateOrderItem = `mutation($o:ID!,$sku:String!){createPickingOrderItem(input:{orderID:$o,sku:$sku,quantity:1}){pickingOrderItem{id}}}`
	mDeleteOrderItem = `mutation($id:ID!){deletePickingOrderItem(id:$id){deletedID}}`
	qOrderHasItem    = `query($id:ID!,$sku:String!){pickingOrders(where:{id:$id,hasOrderItemsWith:[{sku:$sku}]}){totalCount}}`
	qOrderItemBySku  = `query($sku:String!){pickingOrderItems(where:{sku:$sku}){totalCount}}`
	qOrderHasItems   = `query($id:ID!){pickingOrders(where:{id:$id,hasOrderItems:true}){totalCount}}`
)

// TestRelationFilterSkipsSoftDeletedNeighbours checks the soft-delete half of
// the same gap: a soft-deleted row is hidden from a direct query, so a
// relation filter must not match through it either — unless the caller asks
// for deleted rows with X-Pyck-Feature: showdeleted, in which case both the
// direct query and the relation filter see it.
func (s *RelationFilterSuite) TestRelationFilterSkipsSoftDeletedNeighbours() {
	a := s.provision("A")

	d := s.ok(a.PAT, a.RT.ID, mCreateOrder, nil)
	order := digStr(d, "createPickingOrder", "pickingOrder", "id")
	s.Require().NotEmpty(order)

	sku := uniq("rf-sku")
	d = s.ok(a.PAT, a.RT.ID, mCreateOrderItem, map[string]any{"o": order, "sku": sku})
	item := digStr(d, "createPickingOrderItem", "pickingOrderItem", "id")
	s.Require().NotEmpty(item)

	s.Run("positive control: the filter matches the live item", func() {
		d := s.ok(a.PAT, a.RT.ID, qOrderHasItem, map[string]any{"id": order, "sku": sku})
		s.Equal(1, count(d, "pickingOrders"))
		d = s.ok(a.PAT, a.RT.ID, qOrderHasItems, map[string]any{"id": order})
		s.Equal(1, count(d, "pickingOrders"), "plain hasOrderItems")
	})

	s.ok(a.PAT, a.RT.ID, mDeleteOrderItem, map[string]any{"id": item})

	s.Run("control: the soft-deleted item is hidden from a direct query", func() {
		d := s.ok(a.PAT, a.RT.ID, qOrderItemBySku, map[string]any{"sku": sku})
		s.Equal(0, count(d, "pickingOrderItems"))
	})

	s.Run("the filter does not match through the soft-deleted item", func() {
		d := s.ok(a.PAT, a.RT.ID, qOrderHasItem, map[string]any{"id": order, "sku": sku})
		s.Equal(0, count(d, "pickingOrders"), "pickingOrders.hasOrderItemsWith matched a soft-deleted item")
	})

	s.Run("plain hasOrderItems does not count the deleted item", func() {
		d := s.ok(a.PAT, a.RT.ID, qOrderHasItems, map[string]any{"id": order})
		s.Equal(0, count(d, "pickingOrders"), "pickingOrders.hasOrderItems counted a soft-deleted item")
	})

	s.Run("showdeleted makes the direct query and the filter agree again", func() {
		d := s.showDeleted(a, qOrderItemBySku, map[string]any{"sku": sku})
		s.Equal(1, count(d, "pickingOrderItems"))
		d = s.showDeleted(a, qOrderHasItem, map[string]any{"id": order, "sku": sku})
		s.Equal(1, count(d, "pickingOrders"))
		d = s.showDeleted(a, qOrderHasItems, map[string]any{"id": order})
		s.Equal(1, count(d, "pickingOrders"), "plain hasOrderItems with showdeleted")
	})
}

// showDeleted runs a document as t with X-Pyck-Feature: showdeleted.
func (s *RelationFilterSuite) showDeleted(t *tenantCtx, query string, vars map[string]any) map[string]any {
	s.T().Helper()
	r := s.Require()

	buf, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	r.NoError(err)
	req, err := http.NewRequestWithContext(s.Ctx, http.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(buf))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.PAT)
	req.Header.Set("X-Pyck-Tenant-Id", t.RT.ID)
	req.Header.Set("X-Pyck-Feature", "showdeleted")

	resp, err := httpClient.Do(req)
	r.NoError(err, "gateway request")
	body, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	r.NoError(err)
	r.NoError(closeErr, "close gateway response")

	var env struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	r.NoError(json.Unmarshal(body, &env), "decode (%d): %s", resp.StatusCode, truncate(string(body)))
	r.Empty(env.Errors, "showdeleted query failed: %s", truncate(string(body)))
	return env.Data
}
