package service_test

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/management/service"
)

// writerCtx returns a request context for a writer in the given tenant.
func writerCtx(tenantID uuid.UUID) context.Context {
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_WRITER},
	}
	return request.Context(context.Background(), user, tenantID)
}

// ReadBySlug must scope the lookup to the caller's mutation tenant even for
// system users: TenantMixin's privacy filter SKIPS system users, so relying
// on it alone resolves the globally highest version of a slug — another
// tenant's row on a cross-tenant slug collision.
func TestReadBySlug_TenantScopedForSystemUsers(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()

	// Tenant A: v1 of "shared". Tenant B: v1 + v2 (globally highest).
	ctxA, ctxB := writerCtx(tenantA), writerCtx(tenantB)
	rowA, err := client.DataType.Create().
		SetSlug("shared").SetJSONSchema(`{"type":"object"}`).SetEntity("Customer").SetVersion(1).
		Save(ctxA)
	require.NoError(t, err)
	_, err = client.DataType.Create().
		SetSlug("shared").SetJSONSchema(`{"type":"object"}`).SetEntity("Customer").SetVersion(1).
		Save(ctxB)
	require.NoError(t, err)
	_, err = client.DataType.Create().
		SetSlug("shared").SetJSONSchema(`{"type":"object"}`).SetEntity("Customer").SetVersion(2).
		Save(ctxB)
	require.NoError(t, err)

	p := service.NewDatabaseDataTypeProvider(client)

	// System user acting on tenant A must get A's row, not B's higher version.
	sysCtx := request.Context(context.Background(), authn.SystemUser(), tenantA)
	got, err := p.ReadBySlug(sysCtx, "shared")
	require.NoError(t, err)
	assert.Equal(t, tenantA, got.TenantID, "system caller scoped to tenant A must not resolve tenant B's row")
	assert.Equal(t, rowA.ID, got.ID)
	assert.Equal(t, 1, got.Version)
}

// ReadByID must scope the lookup to the caller's mutation tenant. TenantMixin's
// privacy filter SKIPS system users, so for an id-pinned write by a system
// caller the predicate here is the only thing keeping tenant A from resolving
// tenant B's DataType and validating A's row against B's schema.
func TestReadByID_TenantScopedForSystemUsers(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()

	rowB, err := client.DataType.Create().
		SetSlug("foreign").SetJSONSchema(`{"type":"object"}`).SetEntity("Customer").SetVersion(1).
		Save(writerCtx(tenantB))
	require.NoError(t, err)

	p := service.NewDatabaseDataTypeProvider(client)

	// System user acting on tenant A must not resolve tenant B's row by ID.
	sysCtx := request.Context(context.Background(), authn.SystemUser(), tenantA)
	got, err := p.ReadByID(sysCtx, rowB.ID)
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
	assert.Nil(t, got)

	// The owning tenant still resolves it.
	got, err = p.ReadByID(request.Context(context.Background(), authn.SystemUser(), tenantB), rowB.ID)
	require.NoError(t, err)
	assert.Equal(t, rowB.ID, got.ID)
}

// Both readers refuse a context that names no single acting tenant instead of
// querying unscoped: the privacy filter would not narrow the result for a
// system user, so the row of any tenant could come back.
func TestReads_RefuseTenantlessContext(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	tenantA := uuid.New()
	row, err := client.DataType.Create().
		SetSlug("scoped").SetJSONSchema(`{"type":"object"}`).SetEntity("Customer").SetVersion(1).
		Save(writerCtx(tenantA))
	require.NoError(t, err)

	p := service.NewDatabaseDataTypeProvider(client)
	ctx := request.Context(context.Background(), authn.SystemUser())

	_, err = p.ReadByID(ctx, row.ID)
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)

	_, err = p.ReadBySlug(ctx, "scoped")
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}
