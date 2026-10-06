//go:build integration

package relationfilterisolation_test

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	mCreateItem = `mutation($sku:String!){createInventoryItem(input:{sku:$sku}){inventoryItem{id sku}}}`
	mCreateRepo = `mutation($in:CreateRepositoryInput!){createInventoryRepository(input:$in){inventoryRepository{id}}}`
	mCreateMov  = `mutation($in:CreateItemMovementInput!){createInventoryItemMovement(input:$in){inventoryItemMovement{id itemID}}}`
	qMovHasItem = `query($id:ID!,$sku:String!){itemMovements(where:{id:$id,hasItemWith:[{sku:$sku}]}){totalCount}}`
	qMovHasPfx  = `query($id:ID!,$p:String!){itemMovements(where:{id:$id,hasItemWith:[{skuHasPrefix:$p}]}){totalCount}}`

	// dbContainer is the compose service running Postgres.
	dbContainer   = "db"
	dockerTimeout = 30 * time.Second
)

// TestForeignItemReferenceStaysScopedToTheActingTenant covers an item
// movement of A whose item_id is B's item. createInventoryItemMovement
// refuses such an id, but movements stored before that check can still hold
// one, so the reference is written straight into the database: A's movement
// is created with A's item and then repointed to B's. The itemMovements
// relation filter must not evaluate B's item through it, while the same
// filter keeps matching A's own item.
func (s *RelationFilterSuite) TestForeignItemReferenceStaysScopedToTheActingTenant() {
	a := s.provision("A")
	b := s.provision("B")

	bItem, bSku := s.createItem(b)
	aItem, aSku := s.createItem(a)
	aVirt := s.createRepo(a, uniq("rf-a-virt"), true)
	aDst := s.createRepo(a, uniq("rf-a-dst"), false)

	ownMov := s.createMovement(a, aVirt, aDst, aItem)
	crossMov := s.createMovement(a, aVirt, aDst, aItem)
	out := s.psql(`UPDATE inventory.item_movements SET item_id = '` + bItem + `' WHERE id = '` + crossMov + `';`)
	s.Require().Equal("UPDATE 1", strings.TrimSpace(out), "repoint A's movement to B's item")

	s.Run("positive control: hasItemWith still matches A's own item", func() {
		d := s.ok(a.PAT, a.RT.ID, qMovHasItem, map[string]any{"id": ownMov, "sku": aSku})
		s.Equal(1, count(d, "itemMovements"), "the relation filter must keep working inside the acting tenant")
	})

	s.Run("hasItemWith does not evaluate B's item", func() {
		d := s.ok(a.PAT, a.RT.ID, qMovHasItem, map[string]any{"id": crossMov, "sku": bSku})
		s.Equal(0, count(d, "itemMovements"), "itemMovements.hasItemWith matched B's item by sku")
		d = s.ok(a.PAT, a.RT.ID, qMovHasPfx, map[string]any{"id": crossMov, "p": bSku[:12]})
		s.Equal(0, count(d, "itemMovements"), "itemMovements.hasItemWith matched B's item by sku prefix")
	})

	s.Run("plain hasItemMovementItems: B's item does not match through A's movement", func() {
		d := s.ok(b.PAT, b.RT.ID, `query($id:ID!){inventoryItems(where:{id:$id,hasItemMovementItems:true}){totalCount}}`, map[string]any{"id": bItem})
		s.Equal(0, count(d, "inventoryItems"), "B's item matched hasItemMovementItems because of A's movement")
		d = s.ok(a.PAT, a.RT.ID, `query($id:ID!){inventoryItems(where:{id:$id,hasItemMovementItems:true}){totalCount}}`, map[string]any{"id": aItem})
		s.Equal(1, count(d, "inventoryItems"), "positive control: A's item has A's movement")
	})

	s.Run("B's own item is untouched and B sees nothing of A", func() {
		d := s.ok(b.PAT, b.RT.ID, `query($id:ID!){inventoryItems(where:{id:$id}){edges{node{id sku}}} itemMovements{totalCount}}`,
			map[string]any{"id": bItem})
		s.Equal(bSku, digStr(d, "inventoryItems", "edges", "0", "node", "sku"))
		s.Equal(0, count(d, "itemMovements"), "B sees an item movement it never created")
	})
}

func (s *RelationFilterSuite) createItem(t *tenantCtx) (id, sku string) {
	s.T().Helper()
	sku = uniq("rf-sku")
	d := s.ok(t.PAT, t.RT.ID, mCreateItem, map[string]any{"sku": sku})
	id = digStr(d, "createInventoryItem", "inventoryItem", "id")
	s.Require().NotEmpty(id)
	return id, sku
}

func (s *RelationFilterSuite) createRepo(t *tenantCtx, name string, virtual bool) string {
	s.T().Helper()
	d := s.ok(t.PAT, t.RT.ID, mCreateRepo, map[string]any{"in": map[string]any{"name": name, "type": "static", "virtualRepo": virtual}})
	id := digStr(d, "createInventoryRepository", "inventoryRepository", "id")
	s.Require().NotEmpty(id)
	return id
}

func (s *RelationFilterSuite) createMovement(t *tenantCtx, from, to, item string) string {
	s.T().Helper()
	d := s.ok(t.PAT, t.RT.ID, mCreateMov, map[string]any{"in": map[string]any{
		"quantity": 1, "handler": uniq("rf-handler"), "fromID": from, "toID": to, "itemID": item,
	}})
	id := digStr(d, "createInventoryItemMovement", "inventoryItemMovement", "id")
	s.Require().NotEmpty(id)
	return id
}

// psql runs one statement in the compose database and returns its output.
func (s *RelationFilterSuite) psql(query string) string {
	s.T().Helper()
	ctx, cancel := context.WithTimeout(s.Ctx, dockerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", "-i", dbContainer,
		"psql", "-U", "admin", "-d", "pyck_dev", "-tA", "-c", query).CombinedOutput()
	s.Require().NoError(err, "psql: %s", out)
	return string(out)
}
