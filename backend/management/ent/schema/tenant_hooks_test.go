package schema_test

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/schema"
)

// newTenantClient opens an Ent client on a fresh SQLite schema with the
// generated runtime, and so the schema hooks, in place.
func newTenantClient(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	return client
}

// sysCtx is the system user acting on tenantID, the way the resolvers and
// workflows write tenant rows.
func sysCtx(ctx context.Context, tenantID uuid.UUID) context.Context {
	return feature.Context(request.Context(ctx, authn.SystemUser(), tenantID), feature.FEATURE_SHOW_DELETED)
}

// nameKeyOf reads the stored name_key of the row, bypassing the Go value
// the builder handed back.
func nameKeyOf(t *testing.T, client *ent.Client, id uuid.UUID) string {
	t.Helper()
	row, err := client.Tenant.Query().Where(enttenant.ID(id)).Select(enttenant.FieldNameKey).Only(sysCtx(t.Context(), id))
	require.NoError(t, err)
	return row.NameKey
}

// The tenant_name_key unique index compares rows by name_key, so every
// write that sets name (whatever path makes it) must store the key of
// that name. A create, a single-row update and a bulk update each get it
// from the hook; a caller that never names the key still gets one.
func TestTenantHook_DerivesNameKeyOnEveryWriteThatSetsName(t *testing.T) {
	t.Parallel()
	client := newTenantClient(t)

	id := uuid.New()
	ctx := sysCtx(t.Context(), id)
	created, err := client.Tenant.Create().SetID(id).SetName("Ⓐcme").SetIdpOrgRef("org-" + id.String()).Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ⓐcme", created.NameKey)
	assert.Equal(t, "ⓐcme", nameKeyOf(t, client, id), "create")

	require.NoError(t, client.Tenant.UpdateOneID(id).SetName("Acme One").Exec(ctx))
	assert.Equal(t, "acme one", nameKeyOf(t, client, id), "update one")

	n, err := client.Tenant.Update().Where(enttenant.ID(id)).SetName("ACME TWO").Save(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, "acme two", nameKeyOf(t, client, id), "bulk update")
}

// An update that does not touch name leaves the key as it is: the hook
// must not blank or recompute it from nothing.
func TestTenantHook_LeavesNameKeyAloneWhenNameIsNotSet(t *testing.T) {
	t.Parallel()
	client := newTenantClient(t)

	id := uuid.New()
	ctx := sysCtx(t.Context(), id)
	require.NoError(t, client.Tenant.Create().SetID(id).SetName("Acme").SetIdpOrgRef("org-"+id.String()).Exec(ctx))

	require.NoError(t, client.Tenant.UpdateOneID(id).SetStatus(enttenant.StatusError).Exec(ctx))

	assert.Equal(t, "acme", nameKeyOf(t, client, id))
}

// name_key is derived, never chosen: a key set next to a name is replaced
// by the name's key, and a key set without a name is refused, on every
// operation; otherwise a caller could store a key that lets a second row
// carry a name Zitadel treats as the same org.
func TestTenantHook_RefusesOrOverridesACallerChosenNameKey(t *testing.T) {
	t.Parallel()
	client := newTenantClient(t)

	id := uuid.New()
	ctx := sysCtx(t.Context(), id)
	require.NoError(t, client.Tenant.Create().SetID(id).SetName("Acme").SetNameKey("something-else").SetIdpOrgRef("org-"+id.String()).Exec(ctx))
	assert.Equal(t, "acme", nameKeyOf(t, client, id), "a key next to a name is the name's key")

	err := client.Tenant.UpdateOneID(id).SetNameKey("other").Exec(ctx)
	require.ErrorIs(t, err, schema.ErrTenantNameKeyWithoutName)

	_, err = client.Tenant.Update().Where(enttenant.ID(id)).SetNameKey("other").Save(ctx)
	require.ErrorIs(t, err, schema.ErrTenantNameKeyWithoutName)

	other := uuid.New()
	err = client.Tenant.Create().SetID(other).SetNameKey("lonely").SetIdpOrgRef("org-" + other.String()).Exec(sysCtx(t.Context(), other))
	require.ErrorIs(t, err, schema.ErrTenantNameKeyWithoutName, "a create without a name is refused")

	assert.Equal(t, "acme", nameKeyOf(t, client, id), "the refused updates changed nothing")
}

// Two rows whose names Postgres' libc lower() keeps apart but Zitadel folds
// together collide on the unique name_key index, soft-deleted rows
// included.
func TestTenantHook_GoFoldedNamesCollideOnTheIndex(t *testing.T) {
	t.Parallel()
	client := newTenantClient(t)

	first := uuid.New()
	ctx := sysCtx(t.Context(), first)
	require.NoError(t, client.Tenant.Create().SetID(first).SetName("Ⓐcme-x").SetIdpOrgRef("org-"+first.String()).Exec(ctx))
	require.NoError(t, client.Tenant.UpdateOneID(first).SetDeletedAt(time.Now().UTC()).Exec(ctx))

	second := uuid.New()
	err := client.Tenant.Create().SetID(second).SetName("ⓐCME-X").SetIdpOrgRef("org-" + second.String()).Exec(sysCtx(t.Context(), second))
	require.Error(t, err)
	assert.True(t, ent.IsConstraintError(err), "the index refuses it: %v", err)
}
