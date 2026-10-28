package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

var disabledTenantsQuery = resolver.ParseTemplate(`query {
	disabledTenants(ids: [{{range $i, $id := .IDs}}{{if $i}}, {{end}}"{{$id}}"{{end}}])
}`)

type disabledTenantsData struct {
	DisabledTenants []uuid.UUID
}

// disabledTenants is the liveness lookup behind both axes: the auth path's
// home-tenant check and the tenant middleware's acting set. It must see
// soft-deleted rows (which the default row filter hides), report every ID a
// live row does not back (soft-deleted, still/never finished registering,
// or absent entirely) and stay closed to non-system callers.
func TestDisabledTenants(t *testing.T) {
	t.Parallel()

	// Only a live row admits. A soft-deleted row is the operator's off
	// switch; an ID no row backs is a token naming a tenant this deployment
	// does not have, which is denied rather than waved through.
	t.Run("denies the soft-deleted row and the ID with no row", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		live, disabled, unknown := uuidgql.GenerateV7UUID(), uuidgql.GenerateV7UUID(), uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, live), live).Name("Live").Create()
		te.newTenant(te.ctxForTenant(systemUser, disabled), disabled).Name("Disabled").Deleted().Create()

		data := execOK[disabledTenantsData](te, te.ctx(systemUser), disabledTenantsQuery,
			map[string]any{"IDs": []uuid.UUID{live, disabled, unknown}})
		assert.ElementsMatch(t, []uuid.UUID{disabled, unknown}, data.DisabledTenants)
	})

	t.Run("empty answer is an empty list, not null", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		live := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, live), live).Name("Live").Create()

		data := execOK[disabledTenantsData](te, te.ctx(systemUser), disabledTenantsQuery,
			map[string]any{"IDs": []uuid.UUID{live}})
		require.NotNil(t, data.DisabledTenants)
		assert.Empty(t, data.DisabledTenants)
	})

	t.Run("handles an acting set larger than the row cap", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// LimitMixin rejects an explicit limit above mixin.Limit; the handler
		// must chunk so an operator granted into hundreds of tenants still
		// gets a verdict instead of an error (which would deny the caller
		// every uncached tenant; the fleet fails closed on faults).
		unknown := make([]uuid.UUID, 0, mixin.Limit+4)
		for range mixin.Limit + 4 {
			unknown = append(unknown, uuidgql.GenerateV7UUID())
		}
		live, disabled := uuidgql.GenerateV7UUID(), uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, live), live).Name("Live").Create()
		te.newTenant(te.ctxForTenant(systemUser, disabled), disabled).Name("Disabled").Deleted().Create()
		ids := append(append([]uuid.UUID{live}, unknown...), disabled)

		data := execOK[disabledTenantsData](te, te.ctx(systemUser), disabledTenantsQuery,
			map[string]any{"IDs": ids})
		// The live row straddles the first chunk boundary and the disabled
		// one the last, so a chunk dropped or merged shows up as a wrong
		// verdict rather than an error.
		assert.ElementsMatch(t, append(append([]uuid.UUID{}, unknown...), disabled), data.DisabledTenants)
	})

	// The row carries the whole verdict now that tenants are registered
	// DB-first: a pending row is mid-provisioning and an error row stopped
	// part-way, both with an organization and an admin user that can already
	// authenticate. Neither may act until the registration finished.
	t.Run("reports rows whose registration did not finish", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		done, pending, failed := uuidgql.GenerateV7UUID(), uuidgql.GenerateV7UUID(), uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, done), done).Name("Done").Status(enttenant.StatusDone).Create()
		te.newTenant(te.ctxForTenant(systemUser, pending), pending).Name("Pending").Status(enttenant.StatusPending).Create()
		te.newTenant(te.ctxForTenant(systemUser, failed), failed).Name("Failed").Status(enttenant.StatusError).Create()

		data := execOK[disabledTenantsData](te, te.ctx(systemUser), disabledTenantsQuery,
			map[string]any{"IDs": []uuid.UUID{done, pending, failed}})
		assert.ElementsMatch(t, []uuid.UUID{pending, failed}, data.DisabledTenants)
	})

	// Rows written before the status column existed took the schema default,
	// so every tenant registered by the pre-DB-first flow must keep serving
	// requests unchanged.
	t.Run("leaves a row that never set a status live", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		legacy := uuidgql.GenerateV7UUID()
		row := te.newTenant(te.ctxForTenant(systemUser, legacy), legacy).Name("Legacy").Create()
		require.Equal(t, enttenant.StatusDone, row.Status, "the column defaults to done")

		data := execOK[disabledTenantsData](te, te.ctx(systemUser), disabledTenantsQuery,
			map[string]any{"IDs": []uuid.UUID{legacy}})
		assert.Empty(t, data.DisabledTenants)
	})

	t.Run("rejects non-system callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		execErr(te, te.ctx(userA), disabledTenantsQuery,
			map[string]any{"IDs": []uuid.UUID{resolver.TenantA}}, "system caller required")
	})
}
