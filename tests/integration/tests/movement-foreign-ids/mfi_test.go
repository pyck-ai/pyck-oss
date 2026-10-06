//go:build integration

package movementforeignids_test

import (
	"github.com/google/uuid"
)

const (
	mCreateItem = `mutation($sku:String!){createInventoryItem(input:{sku:$sku}){inventoryItem{id sku}}}`
	mCreateRepo = `mutation($in:CreateRepositoryInput!){createInventoryRepository(input:$in){inventoryRepository{id}}}`
	mCreateMov  = `mutation($in:CreateItemMovementInput!){createInventoryItemMovement(input:$in){inventoryItemMovement{id itemID}}}`
	mCreateColl = `mutation($in:CreateCollectionMovementInput!){createInventoryCollectionMovement(input:$in){id}}`
	mCreateRMov = `mutation($in:CreateRepositoryMovementInput!){createInventoryRepositoryMovement(input:$in){inventoryRepositoryMovement{id toID}}}`
	qItem       = `query($id:ID!){inventoryItems(where:{id:$id}){edges{node{id sku}}} itemMovements{totalCount}}`
	qRepo       = `query($id:ID!){repositories(where:{id:$id}){edges{node{id parentID}}} repositoryMovements{totalCount}}`
	qMovByItem  = `query($i:ID!){itemMovements(where:{itemID:$i}){totalCount}}`
	qRMovByTo   = `query($t:ID!){repositoryMovements(where:{toID:$t}){totalCount}}`
)

// TestItemMovementRefusesForeignItem drives the Postgres stored-procedure
// create path with B's item id.
func (s *MovementForeignIDSuite) TestItemMovementRefusesForeignItem() {
	a, b := s.provision("A"), s.provision("B")
	bItem, bSku := s.createItem(b)
	aItem, _ := s.createItem(a)
	aVirt := s.createRepo(a, uniq("mfi-a-virt"), true, "")
	aDst := s.createRepo(a, uniq("mfi-a-dst"), false, "")

	s.Run("positive control: A's own item", func() {
		d := s.ok(a.PAT, a.RT.ID, mCreateMov, map[string]any{"in": movement(aVirt, aDst, aItem)})
		s.Equal(aItem, digStr(d, "createInventoryItemMovement", "inventoryItemMovement", "itemID"))
	})

	s.Run("B's item is refused", func() {
		res := s.gql(a.PAT, a.RT.ID, mCreateMov, map[string]any{"in": movement(aVirt, aDst, bItem)})
		s.True(res.failed(), "createInventoryItemMovement stored B's item: %s", truncate(res.Body))
	})

	s.Run("an unknown item is refused", func() {
		res := s.gql(a.PAT, a.RT.ID, mCreateMov, map[string]any{"in": movement(aVirt, aDst, uuid.NewString())})
		s.True(res.failed())
	})

	s.Run("nothing references B's item", func() {
		s.Equal(0, count(s.ok(a.PAT, a.RT.ID, qMovByItem, map[string]any{"i": bItem}), "itemMovements"))
		s.victimItemUnchanged(b, bItem, bSku)
	})
}

// TestCollectionMovementRefusesForeignItem drives a collection position with
// B's item id; collections take the Go create path.
func (s *MovementForeignIDSuite) TestCollectionMovementRefusesForeignItem() {
	a, b := s.provision("A"), s.provision("B")
	bItem, bSku := s.createItem(b)
	aItem, _ := s.createItem(a)
	aVirt := s.createRepo(a, uniq("mfi-a-virt"), true, "")
	aDst := s.createRepo(a, uniq("mfi-a-dst"), false, "")

	s.Run("positive control: A's own item", func() {
		d := s.ok(a.PAT, a.RT.ID, mCreateColl, map[string]any{"in": collection(aVirt, aDst, aItem)})
		s.NotEmpty(digStr(d, "createInventoryCollectionMovement", "id"))
	})

	s.Run("B's item in a position is refused", func() {
		res := s.gql(a.PAT, a.RT.ID, mCreateColl, map[string]any{"in": collection(aVirt, aDst, bItem)})
		s.True(res.failed(), "createInventoryCollectionMovement stored B's item: %s", truncate(res.Body))
	})

	s.Run("nothing references B's item", func() {
		s.Equal(0, count(s.ok(a.PAT, a.RT.ID, qMovByItem, map[string]any{"i": bItem}), "itemMovements"))
		s.victimItemUnchanged(b, bItem, bSku)
	})
}

// TestRepositoryMovementRefusesForeignTarget moves A's repository, whose
// parent is static, to B's repository.
func (s *MovementForeignIDSuite) TestRepositoryMovementRefusesForeignTarget() {
	a, b := s.provision("A"), s.provision("B")
	bRepo := s.createRepo(b, uniq("mfi-b-repo"), false, "")
	aParent := s.createRepo(a, uniq("mfi-a-parent"), false, "")
	aTarget := s.createRepo(a, uniq("mfi-a-target"), false, "")
	aChild := s.createRepo(a, uniq("mfi-a-child"), false, aParent)
	aChild2 := s.createRepo(a, uniq("mfi-a-child2"), false, aParent)

	s.Run("positive control: A's own target", func() {
		d := s.ok(a.PAT, a.RT.ID, mCreateRMov, map[string]any{"in": map[string]any{
			"handler": uniq("mfi-h"), "repositoryID": aChild, "toID": aTarget,
		}})
		s.Equal(aTarget, digStr(d, "createInventoryRepositoryMovement", "inventoryRepositoryMovement", "toID"))
	})

	s.Run("B's repository as target is refused", func() {
		res := s.gql(a.PAT, a.RT.ID, mCreateRMov, map[string]any{"in": map[string]any{
			"handler": uniq("mfi-h"), "repositoryID": aChild2, "toID": bRepo,
		}})
		s.True(res.failed(), "createInventoryRepositoryMovement stored B's repository: %s", truncate(res.Body))
	})

	s.Run("nothing targets B's repository and B is unchanged", func() {
		s.Equal(0, count(s.ok(a.PAT, a.RT.ID, qRMovByTo, map[string]any{"t": bRepo}), "repositoryMovements"))
		d := s.ok(b.PAT, b.RT.ID, qRepo, map[string]any{"id": bRepo})
		s.Equal(bRepo, digStr(d, "repositories", "edges", "0", "node", "id"))
		s.Equal(0, count(d, "repositoryMovements"), "B sees a repository movement it never created")
	})
}

func movement(from, to, item string) map[string]any {
	return map[string]any{"quantity": 1, "handler": uniq("mfi-h"), "fromID": from, "toID": to, "itemID": item}
}

func collection(from, to, item string) map[string]any {
	return map[string]any{"collection": []map[string]any{{
		"handler": uniq("mfi-h"), "fromID": from, "toID": to, "itemID": item, "quantity": 1,
	}}}
}

func (s *MovementForeignIDSuite) createItem(t *tenantCtx) (id, sku string) {
	s.T().Helper()
	sku = uniq("mfi-sku")
	d := s.ok(t.PAT, t.RT.ID, mCreateItem, map[string]any{"sku": sku})
	id = digStr(d, "createInventoryItem", "inventoryItem", "id")
	s.Require().NotEmpty(id)
	return id, sku
}

func (s *MovementForeignIDSuite) createRepo(t *tenantCtx, name string, virtual bool, parent string) string {
	s.T().Helper()
	in := map[string]any{"name": name, "type": "static", "virtualRepo": virtual}
	if parent != "" {
		in["parentID"] = parent
	}
	d := s.ok(t.PAT, t.RT.ID, mCreateRepo, map[string]any{"in": in})
	id := digStr(d, "createInventoryRepository", "inventoryRepository", "id")
	s.Require().NotEmpty(id)
	return id
}

// victimItemUnchanged reads B's item as B: it still exists with its SKU, and
// B sees no item movement it did not create.
func (s *MovementForeignIDSuite) victimItemUnchanged(b *tenantCtx, id, sku string) {
	s.T().Helper()
	d := s.ok(b.PAT, b.RT.ID, qItem, map[string]any{"id": id})
	s.Equal(sku, digStr(d, "inventoryItems", "edges", "0", "node", "sku"))
	s.Equal(0, count(d, "itemMovements"), "B sees an item movement it never created")
}
