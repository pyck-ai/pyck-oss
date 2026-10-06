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

	entfile "github.com/pyck-ai/pyck/backend/file/ent/gen/file"
)

// entitiesOwnerFiles is the representation the router sends to resolve
// <owner>.file: the owner's id and its tenantID.
var entitiesOwnerFiles = resolver.ParseTemplate(`query {
	_entities(representations: [{__typename: "{{.Typename}}", id: "{{.ID}}", tenantID: "{{.TenantID}}"}]) {
		... on Customer { file { id tenantID } }
		... on Supplier { file { id tenantID } }
		... on InventoryItem { file { id tenantID } }
		... on Repository { file { id tenantID } }
		... on PickingOrder { file { id tenantID } }
	}
}`)

type entitiesOwnerFilesData struct {
	Entities []struct {
		File []struct {
			ID       string `json:"id"`
			TenantID string `json:"tenantID"`
		} `json:"file"`
	} `json:"_entities"`
}

// TestOwnerFilesStayInOwnerTenant pins that <owner>.file only lists files of
// the owner's own tenant. A file's refid is not a foreign key, so a file of
// tenant A can name tenant B's customer. A reader limited to B never saw A's
// file, but the system user skips the tenant filter and a reader acting in A
// and B sees both tenants, so the lookup by refid alone listed A's file on
// B's customer.
func TestOwnerFilesStayInOwnerTenant(t *testing.T) {
	t.Parallel()

	tenantB := resolver.TenantB
	systemUser := &authn.User{ID: uuid.Max, TenantID: uuid.Max}
	bothUser := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantB,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER, tenantB: authn.ROLE_READER},
	}

	owners := []struct {
		typename string
		reftype  entfile.Reftype
	}{
		{"Customer", entfile.ReftypeCustomer},
		{"Supplier", entfile.ReftypeSupplier},
		{"InventoryItem", entfile.ReftypeItem},
		{"Repository", entfile.ReftypeRepository},
		{"PickingOrder", entfile.ReftypeOrder},
	}

	te := setup(t)

	for _, owner := range owners {
		// The owner row lives in tenant B; both tenants hold a file naming it.
		ownerID := uuid.New()
		fileB := te.newFile(te.ctx(userB), userB).RefID(ownerID).RefType(owner.reftype).Create()
		fileA := te.newFile(te.ctx(userA), userA).RefID(ownerID).RefType(owner.reftype).Create()

		readers := map[string]context.Context{
			"system user":             request.Context(te.ctx(userB), systemUser),
			"user acting in A and B":  request.Context(te.ctx(userB), bothUser, tenantA, tenantB),
			"single-tenant user of B": te.ctx(userB),
		}

		for name, ctx := range readers {
			t.Run(owner.typename+"/"+name, func(t *testing.T) {
				t.Parallel()

				data := execOK[entitiesOwnerFilesData](te, ctx, entitiesOwnerFiles,
					map[string]any{"Typename": owner.typename, "ID": ownerID, "TenantID": tenantB})
				require.Len(t, data.Entities, 1)

				var ids []string
				for _, f := range data.Entities[0].File {
					ids = append(ids, f.ID)
					assert.Equal(t, tenantB.String(), f.TenantID, "owner of tenant B lists file %s of another tenant", f.ID)
				}
				assert.Contains(t, ids, fileB.ID.String(), "control: B's own file is listed")
				assert.NotContains(t, ids, fileA.ID.String(), "tenant A's file is listed on tenant B's %s", owner.typename)
			})
		}
	}
}
