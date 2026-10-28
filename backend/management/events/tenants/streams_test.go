package tenants_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3" // enttest SQLite driver

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/management/events/tenants"
)

var testLimits = events.StreamLimits{MaxAge: time.Hour, TenantMaxMsgs: 1000, SystemMaxMsgs: 1000, AggregateMaxMsgs: 10_000}

func TestReconcileStreams(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	// disabledWithStream was disabled after it got its stream; disabled never
	// had one, like a failed registration.
	active, disabled, disabledWithStream := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{active, disabled, disabledWithStream} {
		create := client.Tenant.Create().
			SetID(id).
			SetName("tenant-" + id.String()).
			SetIdpOrgRef("org-" + id.String())
		if id != active {
			create = create.SetDeletedAt(time.Now())
		}

		_, err := create.Save(authn.Context(ctx, authn.SystemUser()))
		require.NoError(t, err)
	}

	srv := natstest.Start(t, nil)
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := events.SetupJetStream(ctx, nc, "pyck", 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, "pyck", 1, natstest.SerialLock(), testLimits)
	require.NoError(t, topology.EnsureTenant(ctx, disabledWithStream))
	require.NoError(t, tenants.ReconcileStreams(ctx, client, topology))

	_, err = js.Stream(ctx, topic.TenantStreamName("pyck", active))
	require.NoError(t, err, "the active tenant has no stream")

	_, err = js.Stream(ctx, topic.TenantStreamName("pyck", disabled))
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound, "a disabled tenant must not get a stream")

	aggregate, err := js.Stream(ctx, "pyck")
	require.NoError(t, err)

	sources := make([]string, 0, len(aggregate.CachedInfo().Config.Sources))
	for _, source := range aggregate.CachedInfo().Config.Sources {
		sources = append(sources, source.Name)
	}
	assert.ElementsMatch(t, []string{
		"pyck-system",
		topic.TenantStreamName("pyck", active),
		topic.TenantStreamName("pyck", disabledWithStream),
	}, sources)
}

// newClientWith opens a tenant table holding one active tenant per id.
func newClientWith(t *testing.T, ids ...uuid.UUID) *ent.Client {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	for _, id := range ids {
		_, err := client.Tenant.Create().
			SetID(id).
			SetName("tenant-" + id.String()).
			SetIdpOrgRef("org-" + id.String()).
			Save(authn.Context(t.Context(), authn.SystemUser()))
		require.NoError(t, err)
	}

	return client
}

func capturedLog(ctx context.Context) (context.Context, *bytes.Buffer) {
	var buf bytes.Buffer

	return log.Context(ctx, zerolog.New(&buf)), &buf
}

// A tenant stream that cannot be put must not stop management, and operators
// must see which tenants have none.
func TestReconcileStreams_TenantFailureDoesNotFail(t *testing.T) {
	t.Parallel()

	ctx, logged := capturedLog(t.Context())

	bad := uuid.New()
	client := newClientWith(t, bad)

	srv := natstest.Start(t, nil)
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := events.SetupJetStream(ctx, nc, "pyck", 1, testLimits)
	require.NoError(t, err)

	// A stream over one of its subjects makes the tenant's stream overlap.
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{Name: "blocker", Subjects: []string{"pyck." + bad.String() + ".x"}})
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, "pyck", 1, natstest.SerialLock(), testLimits)
	require.NoError(t, tenants.ReconcileStreams(ctx, client, topology))

	assert.Contains(t, logged.String(), `"level":"error"`)
	assert.Contains(t, logged.String(), "some tenant event streams are missing or unsourced")
	assert.Contains(t, logged.String(), bad.String())
}

// Without the system stream nothing of no tenant can be published, and the
// aggregate cannot be written: management must not start.
func TestReconcileStreams_SystemStreamFailureFails(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client := newClientWith(t, uuid.New())

	srv := natstest.Start(t, nil)
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	// A stream over one of its subjects makes the system stream overlap.
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{Name: "blocker", Subjects: []string{"pyck-system.x"}})
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, "pyck", 1, natstest.SerialLock(), testLimits)
	err = tenants.ReconcileStreams(ctx, client, topology)
	require.Error(t, err)
	require.NotErrorIs(t, err, events.ErrTenantStreams)
	assert.Contains(t, err.Error(), "put the system stream")
}

// A tenant stream that went missing after startup comes back on the next
// repair.
func TestRepairStreams(t *testing.T) {
	t.Parallel()

	ctx, logged := capturedLog(t.Context())

	lost := uuid.New()
	client := newClientWith(t, uuid.New(), lost)

	srv := natstest.Start(t, nil)
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := events.SetupJetStream(ctx, nc, "pyck", 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, "pyck", 1, natstest.SerialLock(), testLimits)
	require.NoError(t, tenants.ReconcileStreams(ctx, client, topology))
	require.NoError(t, js.DeleteStream(ctx, topic.TenantStreamName("pyck", lost)))

	require.NoError(t, tenants.RepairStreams(ctx, client, topology))

	_, err = js.Stream(ctx, topic.TenantStreamName("pyck", lost))
	require.NoError(t, err, "the lost stream was not put again")
	assert.Contains(t, logged.String(), `"missing":1`)
}
