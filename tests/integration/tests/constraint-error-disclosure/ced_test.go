//go:build integration

package constrainterrordisclosure_test

import (
	"github.com/google/uuid"
)

// rawConstraintText is what a Postgres constraint error carries: the driver
// prefix, the violation wording and the quoted table and constraint names.
var rawConstraintText = []string{"pq:", "violates", "constraint \"", "item_tenant_id_sku", "item_movements", "gen: constraint failed"}

// assertNoRawConstraintText fails when res is not an error, or when its text
// carries any database internals.
func (s *ConstraintErrorSuite) assertNoRawConstraintText(what string, res gqlResp) {
	s.T().Helper()
	s.T().Logf("%s → %q", what, res.errText())
	s.Require().True(res.failed(), "%s must be refused: %s", what, truncate(res.Body))
	for _, raw := range rawConstraintText {
		s.NotContains(res.errText(), raw, "%s: the refusal leaks database internals", what)
	}
}

// TestDuplicateSkuHidesUniqueConstraint: the second item with the same SKU is
// refused without naming the unique index, and keeps "duplicate key" and
// SQLSTATE 23505, which workers match on.
func (s *ConstraintErrorSuite) TestDuplicateSkuHidesUniqueConstraint() {
	a := s.provision("A")
	sku := uniq("ced-sku")
	s.createItem(a, sku)

	res := s.gql(a, mCreateItem, map[string]any{"sku": sku})
	s.assertNoRawConstraintText("duplicate sku", res)
	s.Contains(res.errText(), "23505", "worker SDKs retry a lost race by matching SQLSTATE 23505")
	s.Contains(res.errText(), "duplicate key", "workers treat \"duplicate key\" on a create as \"it already exists\"")
}

// TestUnknownItemHidesForeignKey: an item id that exists nowhere is refused on
// both item movement paths without naming the movement table or its foreign
// key.
func (s *ConstraintErrorSuite) TestUnknownItemHidesForeignKey() {
	a := s.provision("A")
	from := s.createRepo(a, uniq("ced-from"), true)
	to := s.createRepo(a, uniq("ced-to"), false)
	ghost := uuid.NewString()
	movement := map[string]any{"handler": uniq("ced-h"), "fromID": from, "toID": to, "itemID": ghost, "quantity": 1}

	s.Run("item movement", func() {
		s.assertNoRawConstraintText("item movement", s.gql(a, mCreateMov, map[string]any{"in": movement}))
	})
	s.Run("collection movement", func() {
		s.assertNoRawConstraintText("collection movement",
			s.gql(a, mCreateColl, map[string]any{"in": map[string]any{"collection": []map[string]any{movement}}}))
	})
}
