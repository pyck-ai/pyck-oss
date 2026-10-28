package handlers_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	entgo "entgo.io/ent"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/handlers"
)

// errBeginRefused is what splitDriver answers BeginTx with when told to.
var errBeginRefused = errors.New("begin refused")

// errWriterQueryRefused is what splitDriver answers a writer query with when
// failWriterQueries is set.
var errWriterQueryRefused = errors.New("writer query refused")

// splitDriver routes like the production Postgres multi-driver
// (backend/common/db pgMultiDriver): Ent queries go to the reader unless
// the context carries db.WithWriterReads, everything else and every
// non-read-only transaction to the writer. The reader here is a separate
// database that lags the writer, so a read that lands on it is visible in
// the answer.
type splitDriver struct {
	reader, writer *entsql.Driver
	// readerQueries counts queries answered by the reader.
	readerQueries atomic.Int32
	// writerQueries counts queries sent to the writer outside a transaction.
	writerQueries atomic.Int32
	// writerTxs counts transactions begun on the writer.
	writerTxs atomic.Int32
	beginErr  error
	// failWriterQueries makes every writer query outside a transaction
	// fail with errWriterQueryRefused.
	failWriterQueries bool
}

func (d *splitDriver) Query(ctx context.Context, query string, args, v any) error {
	if entgo.QueryFromContext(ctx) != nil && !db.ReadsFromWriter(ctx) {
		d.readerQueries.Add(1)
		return d.reader.Query(ctx, query, args, v)
	}
	d.writerQueries.Add(1)
	if d.failWriterQueries {
		return errWriterQueryRefused
	}
	return d.writer.Query(ctx, query, args, v)
}

func (d *splitDriver) Exec(ctx context.Context, query string, args, v any) error {
	return d.writer.Exec(ctx, query, args, v)
}

func (d *splitDriver) Tx(ctx context.Context) (dialect.Tx, error) { return d.BeginTx(ctx, nil) }

func (d *splitDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	if opts != nil && opts.ReadOnly {
		return d.reader.BeginTx(ctx, opts)
	}
	d.writerTxs.Add(1)
	return d.writer.BeginTx(ctx, opts)
}

func (d *splitDriver) Close() error    { return nil }
func (d *splitDriver) Dialect() string { return dialect.SQLite }

// openTenantDB opens a migrated SQLite database for the tenant schema and
// returns its seeding client and a raw driver over the same database.
func openTenantDB(t *testing.T) (*ent.Client, *entsql.Driver) {
	t.Helper()
	uri := resolver.DatabaseURI(t)
	client := enttest.Open(t, dialect.SQLite, uri)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	drv, err := entsql.Open(dialect.SQLite, uri)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, drv.Close()) })
	return client, drv
}

// seedTenant writes one tenant row with the given liveness-relevant state.
func seedTenant(t *testing.T, client *ent.Client, id uuid.UUID, status enttenant.Status, deleted bool) {
	t.Helper()
	create := client.Tenant.Create().
		SetID(id).
		SetName("t-" + id.String()).
		SetIdpOrgRef("org-" + id.String()).
		SetStatus(status)
	if deleted {
		create.SetDeletedAt(zeroSafeNow())
	}
	require.NoError(t, create.Exec(authn.Context(t.Context(), authn.SystemUser())))
}

// A lifecycle event evicts the cached verdict as soon as its write
// commits; the lookup that follows must read the writer, or a replica
// that has not replayed the write yet answers with the old verdict and
// the cache keeps it for the whole TTL. The reader here still holds the
// pre-event state of every tenant; only the writer's state may come back.
func TestDisabledTenants_ReadsTheWriterNotALaggingReplica(t *testing.T) {
	t.Parallel()

	readerSeed, readerDrv := openTenantDB(t)
	writerSeed, writerDrv := openTenantDB(t)

	restored := uuid.New()   // writer: live and done; reader: still soft-deleted
	retired := uuid.New()    // writer: soft-deleted; reader: still live
	registered := uuid.New() // writer: done; reader: still pending
	seedTenant(t, writerSeed, restored, enttenant.StatusDone, false)
	seedTenant(t, readerSeed, restored, enttenant.StatusDone, true)
	seedTenant(t, writerSeed, retired, enttenant.StatusDone, true)
	seedTenant(t, readerSeed, retired, enttenant.StatusDone, false)
	seedTenant(t, writerSeed, registered, enttenant.StatusDone, false)
	seedTenant(t, readerSeed, registered, enttenant.StatusPending, false)

	drv := &splitDriver{reader: readerDrv, writer: writerDrv}
	client := ent.NewClient(ent.Driver(drv))

	denied, err := handlers.DisabledTenants(t.Context(), client, []uuid.UUID{restored, retired, registered})
	require.NoError(t, err)

	assert.Equal(t, []uuid.UUID{retired}, denied,
		"only the writer's verdicts count: restored and registered are live, retired is not")
	assert.Zero(t, drv.readerQueries.Load(), "no liveness read may land on the reader pool")
	assert.Zero(t, drv.writerTxs.Load(), "the lookup opens no transaction")
	assert.Equal(t, int32(1), drv.writerQueries.Load(), "the lookup is one plain writer query")
}

// The liveness lookup fails closed: a writer that cannot answer the query
// is an error, not an empty deny list that would admit every requested
// tenant, and the lookup does not fall back to the reader.
func TestDisabledTenants_WriterQueryFailureIsAnError(t *testing.T) {
	t.Parallel()

	_, readerDrv := openTenantDB(t)
	_, writerDrv := openTenantDB(t)
	drv := &splitDriver{reader: readerDrv, writer: writerDrv, failWriterQueries: true}
	client := ent.NewClient(ent.Driver(drv))

	denied, err := handlers.DisabledTenants(t.Context(), client, []uuid.UUID{uuid.New()})
	require.ErrorIs(t, err, errWriterQueryRefused)
	assert.Nil(t, denied)
	assert.Zero(t, drv.readerQueries.Load(), "a failed writer query must not fall back to the reader")
}

// The lookup sits on every cache miss of the auth path and must not pay a
// BEGIN/COMMIT round trip: with a writer that refuses every transaction it
// still answers.
func TestDisabledTenants_OpensNoTransaction(t *testing.T) {
	t.Parallel()

	_, readerDrv := openTenantDB(t)
	writerSeed, writerDrv := openTenantDB(t)
	live, disabled := uuid.New(), uuid.New()
	seedTenant(t, writerSeed, live, enttenant.StatusDone, false)
	seedTenant(t, writerSeed, disabled, enttenant.StatusDone, true)

	drv := &splitDriver{reader: readerDrv, writer: writerDrv, beginErr: errBeginRefused}
	client := ent.NewClient(ent.Driver(drv))

	denied, err := handlers.DisabledTenants(t.Context(), client, []uuid.UUID{live, disabled})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{disabled}, denied)
	assert.Zero(t, drv.writerTxs.Load())
}

// A caller granted into more tenants than the LimitMixin cap is answered in
// chunks of at most mixin.Limit IDs: each chunk is one plain writer query,
// none of them a transaction or a reader read, and the verdicts of every
// chunk come back, the disabled tenant in the second chunk included.
func TestDisabledTenants_ChunksPastTheLimitIntoPlainWriterQueries(t *testing.T) {
	t.Parallel()

	_, readerDrv := openTenantDB(t)
	writerSeed, writerDrv := openTenantDB(t)

	ids := make([]uuid.UUID, 0, 2*mixin.Limit+1)
	for range 2*mixin.Limit + 1 {
		ids = append(ids, uuid.New())
	}
	disabled := ids[mixin.Limit+3]
	for _, id := range ids {
		seedTenant(t, writerSeed, id, enttenant.StatusDone, id == disabled)
	}

	drv := &splitDriver{reader: readerDrv, writer: writerDrv, beginErr: errBeginRefused}
	client := ent.NewClient(ent.Driver(drv))

	denied, err := handlers.DisabledTenants(t.Context(), client, ids)

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{disabled}, denied)
	assert.Equal(t, int32(3), drv.writerQueries.Load(), "one plain writer query per chunk of mixin.Limit IDs")
	assert.Zero(t, drv.readerQueries.Load())
	assert.Zero(t, drv.writerTxs.Load())
}
