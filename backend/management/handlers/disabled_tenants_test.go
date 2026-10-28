package handlers_test

import (
	"bytes"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/management/handlers"
)

// The unknown-ID Warn is the operator's only pointer at a registration that
// never produced a row or a token minted for an organization that is not a
// tenant, so its firing rule is part of the contract: an ID with no row is
// logged, an ID whose row is merely soft-deleted is NOT: it is denied for a
// known reason, and logging it as "no row" would send the operator hunting a
// failed registration that never happened. Telling those two cases apart is
// exactly what the handler's FEATURE_SHOW_DELETED context exists for, so
// this test pins both the log and, through it, that context.
func TestDisabledTenants_WarnsOnUnknownButNotOnSoftDeleted(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	seedCtx := authn.Context(t.Context(), authn.SystemUser())
	deleted := uuid.New()
	_, err := client.Tenant.Create().
		SetID(deleted).
		SetName("retired").
		SetIdpOrgRef("org-retired").
		SetDeletedAt(zeroSafeNow()).
		Save(seedCtx)
	require.NoError(t, err)
	unknown := uuid.New()

	var buf bytes.Buffer
	ctx := log.Context(t.Context(), zerolog.New(&buf))

	denied, err := handlers.DisabledTenants(ctx, client, []uuid.UUID{deleted, unknown})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{deleted, unknown}, denied,
		"both the soft-deleted row and the unknown ID are denied")

	logged := buf.String()
	assert.Contains(t, logged, "no row", "the unknown ID must be Warn-logged")
	assert.Contains(t, logged, unknown.String(), "the log must name the unknown ID")
	assert.NotContains(t, logged, deleted.String(),
		"a soft-deleted row is denied for a known reason and must not be logged as unknown")
}

// zeroSafeNow returns a non-zero UTC timestamp for soft-delete seeds.
func zeroSafeNow() time.Time { return time.Now().UTC() }
