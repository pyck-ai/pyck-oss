package ent_test

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entprivacy "entgo.io/ent/privacy"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	entdevice "github.com/pyck-ai/pyck/backend/management/ent/gen/device"
	entdeviceuser "github.com/pyck-ai/pyck/backend/management/ent/gen/deviceuser"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	entpredicate "github.com/pyck-ai/pyck/backend/management/ent/gen/predicate"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	entuser "github.com/pyck-ai/pyck/backend/management/ent/gen/user"
)

// readerIn returns a request context for a user who may read exactly tenantID.
func readerIn(ctx context.Context, tenantID uuid.UUID) context.Context {
	u := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_READER},
	}
	return request.Context(ctx, u, tenantID)
}

// TestRelationFiltersAreScopedToTheActingTenant pins, on the generated
// client, that a Has<Edge>With sub-select only evaluates rows the caller may
// read. TenantIDQueryFilter scopes the root table; the sub-select is built by
// the generated predicate and must carry the scope itself (see
// mixin.ScopeNeighborToTenants and the haswith template in
// common/cmd/entc/templates). The reference under test is the legitimate one:
// a user homed in tenant B is checked in on tenant A's device, so A's
// device_users row points at B's users row.
func TestRelationFiltersAreScopedToTheActingTenant(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)
	sysB := request.Context(t.Context(), authn.SystemUser(), tenantB)

	// Seed under the system user, which the tenant hooks stamp from the
	// context's acting tenant.
	_, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	_, err = client.Tenant.Create().SetID(tenantB).SetName("tenant-b").SetIdpOrgRef("org-b").Save(sysB)
	require.NoError(t, err)

	aUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-a").SetUsername("a-user").SetEmail("a@a.test").
		SetFirstName("Ann").SetLastName("Alpha").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	bUser, err := client.User.Create().SetTenantID(tenantB).
		SetIdpID("idp-b").SetUsername("b-user").SetEmail("b@b.test").
		SetFirstName("Bob").SetLastName("Beta").SetIsAdmin(true).Save(sysB)
	require.NoError(t, err)

	aDevice, err := client.Device.Create().SetName("a-device").Save(sysA)
	require.NoError(t, err)

	// The cross-tenant reference: A's device_users row → B's users row.
	guest, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(bUser.ID).Save(sysA)
	require.NoError(t, err)
	own, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(aUser.ID).Save(sysA)
	require.NoError(t, err)

	readerA := readerIn(t.Context(), tenantA)

	ids := func(t *testing.T, ctx context.Context, preds ...entpredicate.User) []uuid.UUID {
		t.Helper()
		out, err := client.DeviceUser.Query().Where(entdeviceuser.HasUserWith(preds...)).Limit(10).IDs(ctx)
		require.NoError(t, err)
		return out
	}

	t.Run("control: the referenced users row itself is not readable", func(t *testing.T) {
		t.Parallel()

		n, err := client.User.Query().Where(entuser.Username("b-user")).Count(readerA)
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("positive control: the filter still matches the acting tenant's own user", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{own.ID}, ids(t, readerA, entuser.Username("a-user")))
	})

	t.Run("the filter does not evaluate the B-homed users row", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, ids(t, readerA, entuser.Username("b-user")), "exact username")
		assert.Empty(t, ids(t, readerA, entuser.UsernameHasPrefix("b-")), "username prefix")
		assert.Empty(t, ids(t, readerA, entuser.TenantID(tenantB)), "tenant id")
		assert.Empty(t, ids(t, readerA, entuser.IsAdmin(true)), "is_admin")
	})

	t.Run("chained filters do not reach B's tenant row or B's other users", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, ids(t, readerA, entuser.HasTenantWith(enttenant.Name("tenant-b"))))
		assert.Empty(t, ids(t, readerA, entuser.HasTenantWith(enttenant.HasTenantUsersWith(entuser.Email("b@b.test")))))
		// The same chain keeps working inside the acting tenant.
		assert.Equal(t, []uuid.UUID{own.ID}, ids(t, readerA, entuser.HasTenantWith(enttenant.HasTenantUsersWith(entuser.Email("a@a.test")))))
	})

	t.Run("the system user is not narrowed", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{guest.ID}, ids(t, sysA, entuser.Username("b-user")))
	})

	t.Run("B does not see A's device_users row through its own user", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, ids(t, readerIn(t.Context(), tenantB), entuser.Username("b-user")))
	})

	// The plain edge predicate reads the neighbour table the same way for a
	// one-to-many edge: users → device_users. Without the scope, B's own user
	// answers "yes" because of A's row.
	t.Run("B's plain hasDeviceUsersUsers does not match through A's row", func(t *testing.T) {
		t.Parallel()

		readerB := readerIn(t.Context(), tenantB)
		got, err := client.User.Query().Where(entuser.ID(bUser.ID), entuser.HasDeviceUsersUsers()).Limit(10).IDs(readerB)
		require.NoError(t, err)
		assert.Empty(t, got, "B's user is checked in only in A")
	})

	t.Run("positive control: A's plain hasDeviceUsersUsers matches A's own user", func(t *testing.T) {
		t.Parallel()

		got, err := client.User.Query().Where(entuser.HasDeviceUsersUsers()).Limit(10).IDs(readerA)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{aUser.ID}, got)
	})

	t.Run("the system user is not narrowed by the plain predicate", func(t *testing.T) {
		t.Parallel()

		got, err := client.User.Query().Where(entuser.ID(bUser.ID), entuser.HasDeviceUsersUsers()).Limit(10).IDs(sysB)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{bUser.ID}, got)
	})
}

// TestRelationFiltersSkipSoftDeletedNeighbours pins the same gap for the
// soft-delete filter: HistoryMixinQueryFilter hides a soft-deleted row from a
// direct query, and the relation sub-select must hide it too, for the system
// user as well, unless FEATURE_SHOW_DELETED is set.
func TestRelationFiltersSkipSoftDeletedNeighbours(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA := uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)

	_, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	gone, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-gone").SetUsername("gone-user").SetEmail("gone@a.test").
		SetFirstName("Gil").SetLastName("Gone").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	aDevice, err := client.Device.Create().SetName("a-device").Save(sysA)
	require.NoError(t, err)
	du, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(gone.ID).Save(sysA)
	require.NoError(t, err)

	// Soft-delete the user; the device_users row stays live.
	require.NoError(t, client.User.UpdateOneID(gone.ID).SetDeletedAt(time.Now().UTC()).Exec(sysA))

	readerA := readerIn(t.Context(), tenantA)
	ids := func(t *testing.T, ctx context.Context) []uuid.UUID {
		t.Helper()
		out, err := client.DeviceUser.Query().Where(entdeviceuser.HasUserWith(entuser.Username("gone-user"))).Limit(10).IDs(ctx)
		require.NoError(t, err)
		return out
	}

	t.Run("control: the soft-deleted user is not returned directly", func(t *testing.T) {
		t.Parallel()

		n, err := client.User.Query().Where(entuser.Username("gone-user")).Count(readerA)
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("the filter does not match through the soft-deleted user", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, ids(t, readerA), "tenant reader")
		assert.Empty(t, ids(t, sysA), "system user")
	})

	t.Run("FEATURE_SHOW_DELETED matches it again", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{du.ID}, ids(t, feature.Context(readerA, feature.FEATURE_SHOW_DELETED)))
	})
}

// TestPlainEdgePredicateSkipsSoftDeletedNeighbours pins that the plain
// Has<Edge>() predicate agrees with Has<Edge>With(): a user whose only
// device_users row is soft-deleted has no device users, for a reader and for
// the system user, unless FEATURE_SHOW_DELETED is set.
func TestPlainEdgePredicateSkipsSoftDeletedNeighbours(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA := uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)

	_, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	u, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-u").SetUsername("u").SetEmail("u@a.test").
		SetFirstName("Una").SetLastName("User").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	dev, err := client.Device.Create().SetName("dev").Save(sysA)
	require.NoError(t, err)
	du, err := client.DeviceUser.Create().SetDeviceID(dev.ID).SetUserID(u.ID).Save(sysA)
	require.NoError(t, err)

	readerA := readerIn(t.Context(), tenantA)
	has := func(t *testing.T, ctx context.Context) []uuid.UUID {
		t.Helper()
		got, err := client.User.Query().Where(entuser.ID(u.ID), entuser.HasDeviceUsersUsers()).Limit(10).IDs(ctx)
		require.NoError(t, err)
		return got
	}

	require.Equal(t, []uuid.UUID{u.ID}, has(t, readerA), "positive control: the live check-in matches")

	require.NoError(t, client.DeviceUser.UpdateOneID(du.ID).SetDeletedAt(time.Now().UTC()).Exec(sysA))

	t.Run("a reader no longer matches", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, has(t, readerA))
	})

	t.Run("the system user no longer matches", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, has(t, sysA))
	})

	t.Run("FEATURE_SHOW_DELETED matches again", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{u.ID}, has(t, feature.Context(readerA, feature.FEATURE_SHOW_DELETED)))
	})
}

// TestRelationFiltersOnEdgeTraversals pins that the neighbour scopes see the
// caller's context when the query starts from an edge traversal
// (device.QueryDeviceUsersDevice(), client.Device.QueryDeviceUsersDevice(d),
// query-level QueryX()), not only from client.X.Query(). ent v0.14.6 hands
// sqlgraph the traversal's From selector without the context, so the scopes
// read an anonymous caller and match nothing; GraphQL edge connections that
// fall back to _m.QueryX().Paginate(...) (federation entity resolvers,
// mutation payloads) then return empty edges and totalCount 0. The query
// template under common/cmd/entc/templates sets the context on the From
// selector; the scopes themselves must keep narrowing on the traversal.
func TestRelationFiltersOnEdgeTraversals(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)
	sysB := request.Context(t.Context(), authn.SystemUser(), tenantB)

	aTenant, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	_, err = client.Tenant.Create().SetID(tenantB).SetName("tenant-b").SetIdpOrgRef("org-b").Save(sysB)
	require.NoError(t, err)
	aUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-a").SetUsername("a-user").SetEmail("a@a.test").
		SetFirstName("Ann").SetLastName("Alpha").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	bUser, err := client.User.Create().SetTenantID(tenantB).
		SetIdpID("idp-b").SetUsername("b-user").SetEmail("b@b.test").
		SetFirstName("Bob").SetLastName("Beta").SetIsAdmin(false).Save(sysB)
	require.NoError(t, err)
	aDevice, err := client.Device.Create().SetName("a-device").Save(sysA)
	require.NoError(t, err)
	own, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(aUser.ID).Save(sysA)
	require.NoError(t, err)
	guest, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(bUser.ID).Save(sysA)
	require.NoError(t, err)

	readerA := readerIn(t.Context(), tenantA)

	traversals := map[string]func() *ent.DeviceUserQuery{
		"entity": aDevice.QueryDeviceUsersDevice,
		"client": func() *ent.DeviceUserQuery { return client.Device.QueryDeviceUsersDevice(aDevice) },
		"query-level": func() *ent.DeviceUserQuery {
			return client.Device.Query().Where(entdevice.ID(aDevice.ID)).QueryDeviceUsersDevice()
		},
	}
	// matchesOwn checks Count, All and First: the terminators that run
	// through sqlCount / sqlAll with the traversal's From selector.
	matchesOwn := func(t *testing.T, ctx context.Context, traverse func() *ent.DeviceUserQuery) {
		t.Helper()
		q := func() *ent.DeviceUserQuery {
			return traverse().Where(entdeviceuser.HasUserWith(entuser.Username("a-user")))
		}
		n, err := q().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "Count")
		all, err := q().Limit(10).All(ctx)
		require.NoError(t, err)
		require.Len(t, all, 1, "All")
		assert.Equal(t, own.ID, all[0].ID)
		first, err := q().First(ctx)
		require.NoError(t, err, "First")
		assert.Equal(t, own.ID, first.ID)
	}

	for name, traverse := range traversals {
		t.Run(name+": the filter matches the acting tenant's own user", func(t *testing.T) {
			t.Parallel()

			matchesOwn(t, readerA, traverse)
			matchesOwn(t, sysA, traverse)
		})

		t.Run(name+": the filter still skips the B-homed user for a reader", func(t *testing.T) {
			t.Parallel()

			n, err := traverse().Where(entdeviceuser.HasUserWith(entuser.Username("b-user"))).Count(readerA)
			require.NoError(t, err)
			assert.Zero(t, n)
		})

		t.Run(name+": the system user is not narrowed", func(t *testing.T) {
			t.Parallel()

			all, err := traverse().Where(entdeviceuser.HasUserWith(entuser.Username("b-user"))).Limit(10).All(sysA)
			require.NoError(t, err)
			require.Len(t, all, 1)
			assert.Equal(t, guest.ID, all[0].ID)
		})
	}

	t.Run("the plain one-to-many predicate matches on a traversal", func(t *testing.T) {
		t.Parallel()

		all, err := aTenant.QueryTenantUsers().Where(entuser.HasDeviceUsersUsers()).Limit(10).All(readerA)
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.Equal(t, aUser.ID, all[0].ID)
	})
}

// TestPlainManyToOnePredicateAgreesWithHasWith pins that hasX: true means
// hasXWith: {} on an edge that owns its foreign key too. ent's body only
// tests the row's own column, so a device_users row pointing at a
// soft-deleted user, or at a user of a tenant the caller cannot read,
// answered hasUser: true while hasUserWith: {} did not, and the user edge
// itself resolves to nothing for that caller.
func TestPlainManyToOnePredicateAgreesWithHasWith(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)
	sysB := request.Context(t.Context(), authn.SystemUser(), tenantB)

	_, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	_, err = client.Tenant.Create().SetID(tenantB).SetName("tenant-b").SetIdpOrgRef("org-b").Save(sysB)
	require.NoError(t, err)
	aUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-a").SetUsername("a-user").SetEmail("a@a.test").
		SetFirstName("Ann").SetLastName("Alpha").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	goneUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-gone").SetUsername("gone-user").SetEmail("gone@a.test").
		SetFirstName("Gil").SetLastName("Gone").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	bUser, err := client.User.Create().SetTenantID(tenantB).
		SetIdpID("idp-b").SetUsername("b-user").SetEmail("b@b.test").
		SetFirstName("Bob").SetLastName("Beta").SetIsAdmin(false).Save(sysB)
	require.NoError(t, err)
	aDevice, err := client.Device.Create().SetName("a-device").Save(sysA)
	require.NoError(t, err)
	own, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(aUser.ID).Save(sysA)
	require.NoError(t, err)
	guest, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(bUser.ID).Save(sysA)
	require.NoError(t, err)
	gone, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(goneUser.ID).Save(sysA)
	require.NoError(t, err)
	require.NoError(t, client.User.UpdateOneID(goneUser.ID).SetDeletedAt(time.Now().UTC()).Exec(sysA))

	readerA := readerIn(t.Context(), tenantA)
	ids := func(t *testing.T, ctx context.Context, p entpredicate.DeviceUser) []uuid.UUID {
		t.Helper()
		out, err := client.DeviceUser.Query().Where(p).Limit(10).IDs(ctx)
		require.NoError(t, err)
		return out
	}

	t.Run("a reader matches only the live, readable user", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{own.ID}, ids(t, readerA, entdeviceuser.HasUser()))
		assert.Equal(t, ids(t, readerA, entdeviceuser.HasUserWith()), ids(t, readerA, entdeviceuser.HasUser()), "hasUser agrees with hasUserWith: {}")
	})

	t.Run("the negation matches the rows whose user the reader cannot see", func(t *testing.T) {
		t.Parallel()

		assert.ElementsMatch(t, []uuid.UUID{guest.ID, gone.ID}, ids(t, readerA, entdeviceuser.Not(entdeviceuser.HasUser())))
	})

	t.Run("the system user is not tenant-narrowed but skips the soft-deleted user", func(t *testing.T) {
		t.Parallel()

		assert.ElementsMatch(t, []uuid.UUID{own.ID, guest.ID}, ids(t, sysA, entdeviceuser.HasUser()))
	})

	t.Run("FEATURE_SHOW_DELETED matches the soft-deleted user again", func(t *testing.T) {
		t.Parallel()

		assert.ElementsMatch(t, []uuid.UUID{own.ID, gone.ID}, ids(t, feature.Context(readerA, feature.FEATURE_SHOW_DELETED), entdeviceuser.HasUser()))
	})
}

// TestRelationPredicatesInUpdateOne pins that UpdateOne evaluates a relation
// predicate for the caller in its context. ent v0.14.6 builds the predicate
// selector of a single-row update without the context, so the neighbour
// scopes read an anonymous caller, match nothing, and the update failed with
// not found, for the system user too. The update template under
// common/cmd/entc/templates passes the context; the tenant scope must still
// narrow for a tenant writer.
func TestRelationPredicatesInUpdateOne(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)
	sysB := request.Context(t.Context(), authn.SystemUser(), tenantB)

	_, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	_, err = client.Tenant.Create().SetID(tenantB).SetName("tenant-b").SetIdpOrgRef("org-b").Save(sysB)
	require.NoError(t, err)
	aUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-a").SetUsername("a-user").SetEmail("a@a.test").
		SetFirstName("Ann").SetLastName("Alpha").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	bUser, err := client.User.Create().SetTenantID(tenantB).
		SetIdpID("idp-b").SetUsername("b-user").SetEmail("b@b.test").
		SetFirstName("Bob").SetLastName("Beta").SetIsAdmin(false).Save(sysB)
	require.NoError(t, err)
	aDevice, err := client.Device.Create().SetName("a-device").Save(sysA)
	require.NoError(t, err)
	own, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(aUser.ID).Save(sysA)
	require.NoError(t, err)
	guest, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(bUser.ID).Save(sysA)
	require.NoError(t, err)

	writerA := request.Context(t.Context(), &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_WRITER},
	}, tenantA)

	// The updates run one after another: they write the same rows.
	update := func(ctx context.Context, id uuid.UUID, p entpredicate.DeviceUser) error {
		return client.DeviceUser.UpdateOneID(id).Where(p).SetDeviceID(aDevice.ID).Exec(ctx)
	}
	for name, ctx := range map[string]context.Context{"system user": sysA, "tenant writer": writerA} {
		require.NoError(t, update(ctx, own.ID, entdeviceuser.HasUserWith(entuser.Username("a-user"))), "%s: HasUserWith", name)
		require.NoError(t, update(ctx, own.ID, entdeviceuser.HasUser()), "%s: HasUser", name)
	}

	// The B-homed user stays out of a tenant-A writer's sub-select, so the
	// guest row does not match; the system user is not narrowed.
	err = update(writerA, guest.ID, entdeviceuser.HasUserWith(entuser.Username("b-user")))
	require.True(t, ent.IsNotFound(err), "tenant writer must not match through B's user, got %v", err)
	require.NoError(t, update(sysA, guest.ID, entdeviceuser.HasUserWith(entuser.Username("b-user"))), "system user")
}

// TestRelationPredicatesUnderAllow pins how an entprivacy Allow decision
// interacts with the neighbour scopes of a relation predicate. Allow lifts the
// soft-delete scope, like the root policy. It does NOT lift the tenant scope:
// code under Allow may scope the root by hand, and an unscoped sub-select
// would let a client relation filter test another tenant's rows. Only the
// system user is unnarrowed; Allow without a user fails closed.
func TestRelationPredicatesUnderAllow(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA, tenantB := uuid.New(), uuid.New()
	sysA := request.Context(t.Context(), authn.SystemUser(), tenantA)
	sysB := request.Context(t.Context(), authn.SystemUser(), tenantB)

	_, err := client.Tenant.Create().SetID(tenantA).SetName("tenant-a").SetIdpOrgRef("org-a").Save(sysA)
	require.NoError(t, err)
	_, err = client.Tenant.Create().SetID(tenantB).SetName("tenant-b").SetIdpOrgRef("org-b").Save(sysB)
	require.NoError(t, err)
	aUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-a").SetUsername("a-user").SetEmail("a@a.test").
		SetFirstName("Ann").SetLastName("Alpha").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	goneUser, err := client.User.Create().SetTenantID(tenantA).
		SetIdpID("idp-gone").SetUsername("gone-user").SetEmail("gone@a.test").
		SetFirstName("Gil").SetLastName("Gone").SetIsAdmin(false).Save(sysA)
	require.NoError(t, err)
	bUser, err := client.User.Create().SetTenantID(tenantB).
		SetIdpID("idp-b").SetUsername("b-user").SetEmail("b@b.test").
		SetFirstName("Bob").SetLastName("Beta").SetIsAdmin(false).Save(sysB)
	require.NoError(t, err)
	aDevice, err := client.Device.Create().SetName("a-device").Save(sysA)
	require.NoError(t, err)
	own, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(aUser.ID).Save(sysA)
	require.NoError(t, err)
	guest, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(bUser.ID).Save(sysA)
	require.NoError(t, err)
	gone, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(goneUser.ID).Save(sysA)
	require.NoError(t, err)
	require.NoError(t, client.User.UpdateOneID(goneUser.ID).SetDeletedAt(time.Now().UTC()).Exec(sysA))

	writerA := request.Context(t.Context(), &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_WRITER},
	}, tenantA)

	allow := entprivacy.DecisionContext(t.Context(), entprivacy.Allow)
	allowSys := entprivacy.DecisionContext(sysA, entprivacy.Allow)
	allowWriterA := entprivacy.DecisionContext(writerA, entprivacy.Allow)
	ids := func(t *testing.T, ctx context.Context, p ...entpredicate.DeviceUser) []uuid.UUID {
		t.Helper()
		out, err := client.DeviceUser.Query().Where(p...).Limit(10).IDs(ctx)
		require.NoError(t, err)
		return out
	}

	t.Run("control: the root query under Allow returns every row", func(t *testing.T) {
		t.Parallel()

		assert.ElementsMatch(t, []uuid.UUID{own.ID, guest.ID, gone.ID}, ids(t, allow))
		n, err := client.User.Query().Where(entuser.Username("gone-user")).Count(allow)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "the soft-deleted user is visible at the root")
	})

	t.Run("Allow with the system user: the plain predicate matches every row", func(t *testing.T) {
		t.Parallel()

		assert.ElementsMatch(t, []uuid.UUID{own.ID, guest.ID, gone.ID}, ids(t, allowSys, entdeviceuser.HasUser()))
	})

	t.Run("Allow with the system user: the filter reaches every user", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{own.ID}, ids(t, allowSys, entdeviceuser.HasUserWith(entuser.Username("a-user"))), "own tenant")
		assert.Equal(t, []uuid.UUID{guest.ID}, ids(t, allowSys, entdeviceuser.HasUserWith(entuser.Username("b-user"))), "other tenant")
		assert.Equal(t, []uuid.UUID{gone.ID}, ids(t, allowSys, entdeviceuser.HasUserWith(entuser.Username("gone-user"))), "soft-deleted")
	})

	t.Run("Allow without a user fails closed", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, ids(t, allow, entdeviceuser.HasUserWith(entuser.Username("a-user"))))
		assert.Empty(t, ids(t, allow, entdeviceuser.HasUser()))
	})

	t.Run("Allow does not let a hand-scoped tenant query read another tenant's users", func(t *testing.T) {
		t.Parallel()

		scoped := entdeviceuser.TenantID(tenantA)
		assert.ElementsMatch(t, []uuid.UUID{own.ID, guest.ID, gone.ID}, ids(t, allowWriterA, scoped), "control: the hand-scoped root")
		assert.Empty(t, ids(t, allowWriterA, scoped, entdeviceuser.HasUserWith(entuser.Username("b-user"))), "B's user stays out of the sub-select")
		assert.Equal(t, []uuid.UUID{own.ID}, ids(t, allowWriterA, scoped, entdeviceuser.HasUserWith(entuser.Username("a-user"))), "own tenant's user")
	})

	t.Run("Allow still lifts the soft-delete scope for a tenant user", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, []uuid.UUID{gone.ID}, ids(t, allowWriterA, entdeviceuser.TenantID(tenantA), entdeviceuser.HasUserWith(entuser.Username("gone-user"))))
	})

	t.Run("Allow keeps a tenant user's relation filters on its tenants", func(t *testing.T) {
		t.Parallel()

		assert.ElementsMatch(t, []uuid.UUID{own.ID, gone.ID}, ids(t, allowWriterA, entdeviceuser.HasUser()), "plain predicate")
		assert.Empty(t, ids(t, allowWriterA,
			entdeviceuser.TenantID(tenantA),
			entdeviceuser.HasUserWith(entuser.TenantID(tenantB))), "B's tenant id")
	})
}

// TestRelationPredicatesFollowTheRequestTenants pins that the sub-select of a
// relation filter holds exactly the request's readable tenants, with and
// without an Allow decision: a caller acting for A and B matches through A's
// and B's users, never through C's, and a requested tenant the caller cannot
// read drops out of the sub-select.
func TestRelationPredicatesFollowTheRequestTenants(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log)))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	tenantA, tenantB, tenantC := uuid.New(), uuid.New(), uuid.New()
	sys := func(tenantID uuid.UUID) context.Context {
		return request.Context(t.Context(), authn.SystemUser(), tenantID)
	}

	rows := map[uuid.UUID]uuid.UUID{} // tenant → device_users row pointing at that tenant's user
	for name, tenantID := range map[string]uuid.UUID{"a": tenantA, "b": tenantB, "c": tenantC} {
		_, err := client.Tenant.Create().SetID(tenantID).SetName("tenant-" + name).SetIdpOrgRef("org-" + name).Save(sys(tenantID))
		require.NoError(t, err)
	}
	aDevice, err := client.Device.Create().SetName("a-device").Save(sys(tenantA))
	require.NoError(t, err)
	for name, tenantID := range map[string]uuid.UUID{"a": tenantA, "b": tenantB, "c": tenantC} {
		u, err := client.User.Create().SetTenantID(tenantID).
			SetIdpID("idp-" + name).SetUsername(name + "-user").SetEmail(name + "@" + name + ".test").
			SetFirstName(name).SetLastName(name).SetIsAdmin(false).Save(sys(tenantID))
		require.NoError(t, err)
		du, err := client.DeviceUser.Create().SetDeviceID(aDevice.ID).SetUserID(u.ID).Save(sys(tenantA))
		require.NoError(t, err)
		rows[tenantID] = du.ID
	}

	readerAB := request.Context(t.Context(), &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER, tenantB: authn.ROLE_READER},
	}, tenantA, tenantB)
	// Asks for A, B and C but may read only A: the root filter refuses this
	// caller, Allow skips that refusal, and the sub-select keeps only A.
	readerAOnly := request.Context(t.Context(), &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER},
	}, tenantA, tenantB, tenantC)

	ids := func(t *testing.T, ctx context.Context, p ...entpredicate.DeviceUser) []uuid.UUID {
		t.Helper()
		out, err := client.DeviceUser.Query().Where(p...).Limit(10).IDs(ctx)
		require.NoError(t, err)
		return out
	}
	byName := func(name string) entpredicate.DeviceUser {
		return entdeviceuser.HasUserWith(entuser.Username(name))
	}

	for name, ctx := range map[string]context.Context{
		"no decision": readerAB,
		"Allow":       entprivacy.DecisionContext(readerAB, entprivacy.Allow),
	} {
		t.Run(name+": tenants A and B match, C does not", func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, []uuid.UUID{rows[tenantA]}, ids(t, ctx, byName("a-user")))
			assert.Equal(t, []uuid.UUID{rows[tenantB]}, ids(t, ctx, byName("b-user")))
			assert.Empty(t, ids(t, ctx, byName("c-user")))
			assert.Empty(t, ids(t, ctx, entdeviceuser.HasUserWith(entuser.TenantID(tenantC))))
			assert.ElementsMatch(t, []uuid.UUID{rows[tenantA], rows[tenantB]}, ids(t, ctx, entdeviceuser.HasUser()))
		})
	}

	t.Run("Allow: a requested tenant without reader role drops out", func(t *testing.T) {
		t.Parallel()

		_, err := client.DeviceUser.Query().Limit(10).IDs(readerAOnly)
		require.Error(t, err, "control: the root filter refuses the caller without Allow")

		ctx := entprivacy.DecisionContext(readerAOnly, entprivacy.Allow)
		assert.Equal(t, []uuid.UUID{rows[tenantA]}, ids(t, ctx, byName("a-user")))
		assert.Empty(t, ids(t, ctx, byName("b-user")))
		assert.Empty(t, ids(t, ctx, byName("c-user")))
		assert.Equal(t, []uuid.UUID{rows[tenantA]}, ids(t, ctx, entdeviceuser.HasUser()))
	})
}
