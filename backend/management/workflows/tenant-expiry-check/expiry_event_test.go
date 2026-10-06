package tenantexpirycheck_test

// Tests that the tenant-expiry sweep emits a tenant.<id>.delete outbox event
// for each tenant it soft-deletes (that event drives DisableTenantWorkflow and
// revocation-cache eviction).
//
// Tenant has no tenant_id column, so the event's tenant comes from either:
//   - the activity's context, built with request.Context(..., tenantID);
//   - the hook treating Tenant as its own tenant (HookConfig.SelfTenantSchemas).
//
// With neither, the hook fails the write (events.ErrNoTenantForEvent) instead
// of committing it without an event.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	entgo "entgo.io/ent"
	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	tenantexpirycheck "github.com/pyck-ai/pyck/backend/management/workflows/tenant-expiry-check"
)

// idsProbe records what the hook sees for Tenant mutations.
// It is registered as a client hook BEFORE the events hook so it runs with the
// exact same ctx, and calls the same IDs(ctx) that extractEntityID calls.
//
// It also counts calls to the hook's EntityFetcher and OutboxInserter. The
// fetcher is only invoked after extractEntityID succeeded (hook.go
// prepareBeforeState), so fetcherCalls>0 proves extraction did NOT fail;
// inserterCalls==0 with fetcherCalls>0 proves the drop happens after
// extraction, inside buildOutboxEntry (tenant ID underivable -> nil entry).
type idsProbe struct {
	mu            sync.Mutex
	records       []string
	fetcherCalls  int
	inserterCalls int
}

func (p *idsProbe) summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return "fetcherCalls=" + strconv.Itoa(p.fetcherCalls) + " inserterCalls=" + strconv.Itoa(p.inserterCalls)
}

func (p *idsProbe) hook() entgo.Hook {
	return func(next entgo.Mutator) entgo.Mutator {
		return entgo.MutateFunc(func(ctx context.Context, m entgo.Mutation) (entgo.Value, error) {
			if m.Type() == "Tenant" && (m.Op().Is(entgo.OpUpdate) || m.Op().Is(entgo.OpUpdateOne)) {
				rec := "op=" + m.Op().String()
				if prov, ok := m.(events.IDsProvider); ok {
					ids, err := prov.IDs(ctx)
					// Same wrapping as extractEntityID.
					if err != nil {
						err = errors.Join(events.ErrExtractEntityID, err)
					}
					rec += " ids=" + idsString(ids) + " err=" + errString(err)
				}
				req := request.ForContext(ctx)
				rec += " ctxTenantIDs=" + idsString(req.TenantIDs()) + " hasMutationTenantID=" + boolString(req.HasMutationTenantID())
				p.mu.Lock()
				p.records = append(p.records, rec)
				p.mu.Unlock()
			}
			return next.Mutate(ctx, m)
		})
	}
}

func idsString(ids []uuid.UUID) string {
	s := make([]string, 0, len(ids))
	for _, id := range ids {
		s = append(s, id.String())
	}
	return "[" + strings.Join(s, ",") + "]"
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// setupWithEvents builds an in-memory client with the same hook wiring as
// management's main.go (events.MutationEventHook + ent outbox inserter).
func setupWithEvents(t *testing.T) (*ent.Client, *idsProbe) {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	probe := &idsProbe{}
	client.Use(probe.hook())
	fetcher := events.BuildEntityFetcher(ent.TxFromContext, events.FieldData)
	inserter := events.NewEntOutboxInserter(ent.TxFromContext)
	client.Use(events.MutationEventHook(events.HookConfig{
		Service:           "management",
		StreamName:        "pyck",
		SelfTenantSchemas: core.SelfTenantSchemas(),
		EntityFetcher: func(ctx context.Context, schema string, id uuid.UUID) (any, error) {
			if schema == "Tenant" {
				probe.mu.Lock()
				probe.fetcherCalls++
				probe.mu.Unlock()
			}
			return fetcher(ctx, schema, id)
		},
		OutboxInserter: func(ctx context.Context, e *events.OutboxEntry) error {
			probe.mu.Lock()
			probe.inserterCalls++
			probe.mu.Unlock()
			return inserter(ctx, e)
		},
	}))
	return client, probe
}

// seedExpiredTenant inserts an already-expired tenant with events suppressed
// so the outbox stays empty and only the sweep's own events are measured.
func seedExpiredTenant(t *testing.T, client *ent.Client) uuid.UUID {
	t.Helper()
	id := uuid.New()
	ctx := feature.Context(authn.Context(context.Background(), authn.SystemUser()), feature.FEATURE_SUPPRESS_EVENTS)
	_, err := client.Tenant.Create().
		SetID(id).
		SetName("expired-" + id.String()).
		SetIdpOrgRef(id.String()).
		SetExpiresAt(time.Now().UTC().Add(-time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	n, err := client.EntityEventsOutbox.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "seeding must not emit events")
	return id
}

// tenantDeleteEvents returns outbox topics for delete events of this tenant.
func tenantDeleteEvents(t *testing.T, client *ent.Client, id uuid.UUID) (matching []string, all []string) {
	t.Helper()
	rows, err := client.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(context.Background())
	require.NoError(t, err)
	for _, r := range rows {
		all = append(all, r.Topic)
		if strings.Contains(r.Topic, id.String()) && strings.HasSuffix(r.Topic, ".delete") {
			matching = append(matching, r.Topic)
		}
	}
	return matching, all
}

func softDeleted(t *testing.T, client *ent.Client, id uuid.UUID) bool {
	t.Helper()
	ctx := feature.Context(authn.Context(context.Background(), authn.SystemUser()), feature.FEATURE_SHOW_DELETED)
	row, err := client.Tenant.Query().Where(enttenant.IDEQ(id)).Only(ctx)
	require.NoError(t, err)
	return !row.DeletedAt.IsZero()
}

// TestSoftDeleteExpiredTenantEmitsDeleteEvent drives the REAL activity. The sweep
// is documented (activities.go) to make the MutationEventHook write a tenant
// delete outbox row, which drives DisableTenantWorkflow.
func TestSoftDeleteExpiredTenantEmitsDeleteEvent(t *testing.T) {
	t.Parallel()

	client, probe := setupWithEvents(t)
	id := seedExpiredTenant(t, client)

	activities := tenantexpirycheck.NewActivities(client)
	ctx := authn.Context(context.Background(), authn.SystemUser())
	require.NoError(t, activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id}))

	require.True(t, softDeleted(t, client, id), "the row itself must be soft-deleted")

	for _, r := range probe.records {
		t.Logf("probe (extractEntityID equivalent): %s", r)
	}

	matching, all := tenantDeleteEvents(t, client, id)
	t.Logf("hook stage counters: %s", probe.summary())
	t.Logf("outbox topics after sweep: %v", all)
	assert.Len(t, matching, 1,
		"sweep soft-deleted the tenant but wrote no tenant delete outbox event (all outbox topics: %v)", all)
}

// The hook must not depend on the caller's context carrying a tenant ID for
// Tenant: with only authn.Context (no tenant.Context) it still emits, using the
// tenant's own ID (SelfTenantSchemas), for a guarded and an unguarded
// UpdateOneID.
func TestSoftDeleteExpiredTenant_NoTenantContext_EmitsViaSelfTenant(t *testing.T) {
	t.Parallel()

	for name, sweep := range map[string]func(context.Context, *ent.Tx, uuid.UUID, time.Time) error{
		"UpdateOneID with expiry guards": func(ctx context.Context, tx *ent.Tx, id uuid.UUID, now time.Time) error {
			return tx.Tenant.UpdateOneID(id).
				Where(enttenant.ExpiresAtNotNil(), enttenant.ExpiresAtLTE(now)).
				SetDeletedAt(now).
				Exec(ctx)
		},
		"UpdateOneID": func(ctx context.Context, tx *ent.Tx, id uuid.UUID, now time.Time) error {
			return tx.Tenant.UpdateOneID(id).SetDeletedAt(now).Exec(ctx)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, _ := setupWithEvents(t)
			id := seedExpiredTenant(t, client)

			ctx := txid.With(authn.Context(context.Background(), authn.SystemUser()), txid.New())
			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			ctx = ent.NewTxContext(ctx, tx)
			require.NoError(t, sweep(ctx, tx, id, time.Now().UTC()))
			require.NoError(t, tx.Commit())

			matching, all := tenantDeleteEvents(t, client, id)
			assert.Len(t, matching, 1, "outbox topics: %v", all)
			// tenantID == entityID: the topic starts with pyck.<tenantID>.
			assert.Contains(t, matching[0], "pyck."+id.String()+".crud.management.tenant."+id.String())
		})
	}
}

// Without SelfTenantSchemas and without a tenant in the context, the mutation
// must fail loudly and roll back instead of committing with no event.
func TestSoftDeleteExpiredTenant_NoTenantResolvable_FailsAndRollsBack(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "management",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))
	id := seedExpiredTenant(t, client)

	ctx := txid.With(authn.Context(context.Background(), authn.SystemUser()), txid.New())
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	ctx = ent.NewTxContext(ctx, tx)

	err = tx.Tenant.UpdateOneID(id).SetDeletedAt(time.Now().UTC()).Exec(ctx)
	require.ErrorIs(t, err, events.ErrNoTenantForEvent)
	require.NoError(t, tx.Rollback())

	assert.False(t, softDeleted(t, client, id), "the soft-delete must be rolled back")
	_, all := tenantDeleteEvents(t, client, id)
	assert.Empty(t, all)
}

// The activity itself sets the tenant on its context: a client whose hook does
// NOT know Tenant is self-tenant (as a future service wiring might) still emits.
func TestSoftDeleteExpiredTenant_ActivityProvidesTenantContext(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "management",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))
	id := seedExpiredTenant(t, client)

	require.NoError(t, tenantexpirycheck.NewActivities(client).SoftDeleteExpiredTenantActivity(
		authn.Context(context.Background(), authn.SystemUser()),
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id}))

	matching, all := tenantDeleteEvents(t, client, id)
	assert.Len(t, matching, 1, "outbox topics: %v", all)
}
