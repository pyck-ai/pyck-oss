package resolvers_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

const routingStream = "pyck"

// userC has a role in tenantC, which has no routing entries in these tests.
var userC = &authn.User{
	ID:       uuid.MustParse("1020ed57-8fca-40e0-958b-10f428774102"),
	TenantID: tenantC,
	Roles:    map[uuid.UUID]authn.Role{tenantC: authn.ROLE_ADMIN},
}

var transactionRouting = resolver.ParseTemplate(`query {
	transactionRouting(transactionID: "{{.TransactionID}}") {
		transactionID
		entries {
			tenantID
			eventID
			outcome
			sequence
			recordedAt
			targets { kind workflow workflowID runID signal reason error }
		}
	}
}`)

type transactionRoutingData struct {
	TransactionRouting struct {
		TransactionID string
		Entries       []struct {
			TenantID   string
			EventID    string
			Outcome    string
			Sequence   uint64
			RecordedAt time.Time
			Targets    []struct {
				Kind       string
				Workflow   *string
				WorkflowID *string
				RunID      *string
				Signal     *string
				Reason     *string
				Error      *string
			}
		}
	}
}

// routingEnv is a resolver environment whose router reads routing statuses
// from an embedded JetStream.
type routingEnv struct {
	*testEnv

	js jetstream.JetStream
	kv jetstream.KeyValue
}

func setupRouting(t *testing.T) *routingEnv {
	t.Helper()

	srv, err := server.NewServer(&server.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)

	go srv.Start()

	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded NATS did not start")
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{Name: routingStream, Subjects: []string{routingStream + ".>"}})
	require.NoError(t, err)

	te := &testEnv{TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t), t: t}

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t), enttest.WithOptions(ent.Log(t.Log))).Debug()

	router := services.NewSignalRouter(client, services.SignalRouterConfig{
		ClientFactory:   newMockClientFactory(),
		JetstreamClient: js,
		StreamName:      routingStream,
	})
	te.SignalRouter = router

	v := validator.NewValidator(te.DataTypeProvider)
	te.Init(client, resolvers.NewSchema(resolvers.NewResolver("workflow", client, v, router, nil, resolvers.RemoteUIDefaults{})),
		func(s *handler.Server) { s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "workflow-test", 0)) })

	kv, err := services.EnsureRoutingStatusBucket(context.Background(), js, routingStream)
	require.NoError(t, err)

	return &routingEnv{testEnv: te, js: js, kv: kv}
}

// seed stores a routing status the way the router does.
func (e *routingEnv) seed(tenant, tx uuid.UUID, sequence uint64, outcome services.RoutingOutcome, targets ...services.RoutingTarget) uuid.UUID {
	e.t.Helper()

	eventID := uuid.Must(uuid.NewV7())

	data, err := json.Marshal(services.RoutingStatus{
		TenantID: tenant, TransactionID: tx, EventID: eventID, Outcome: outcome, Sequence: sequence,
		Targets: targets, RecordedAt: time.Now().UTC(),
	})
	require.NoError(e.t, err)

	_, err = e.kv.Put(context.Background(), services.RoutingStatusKey(tenant, tx, eventID), data)
	require.NoError(e.t, err)

	return eventID
}

func (e *routingEnv) query(ctx context.Context, tx uuid.UUID) transactionRoutingData {
	e.t.Helper()

	return execOK[transactionRoutingData](e.testEnv, ctx, transactionRouting, map[string]any{"TransactionID": tx.String()})
}

func TestTransactionRouting_ReturnsTheEntriesOfTheTransaction(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)
	tx := uuid.Must(uuid.NewV7())

	first := e.seed(tenantA, tx, 10, services.RoutingOutcomeDone,
		services.RoutingTarget{Kind: services.RoutingTargetStarted, Workflow: "order", WorkflowID: "order_1", RunID: "run-1"})
	second := e.seed(tenantA, tx, 11, services.RoutingOutcomeGaveUp,
		services.RoutingTarget{Kind: services.RoutingTargetSignalled, Workflow: "order", WorkflowID: "order_1", RunID: "run-1", Signal: "Approved"},
		services.RoutingTarget{Kind: services.RoutingTargetFailed, Workflow: "order", Signal: "Shipped", Error: "temporal down"},
		services.RoutingTarget{Kind: services.RoutingTargetDropped, Reason: "no running execution"})
	e.seed(tenantA, uuid.Must(uuid.NewV7()), 12, services.RoutingOutcomeDone) // another transaction

	got := e.query(e.ctx(userA), tx).TransactionRouting

	assert.Equal(t, tx.String(), got.TransactionID)
	require.Len(t, got.Entries, 2, "only this transaction's entries")

	assert.Equal(t, first.String(), got.Entries[0].EventID)
	assert.Equal(t, tenantA.String(), got.Entries[0].TenantID)
	assert.Equal(t, "DONE", got.Entries[0].Outcome)
	assert.EqualValues(t, 10, got.Entries[0].Sequence)
	require.Len(t, got.Entries[0].Targets, 1)
	assert.Equal(t, "STARTED", got.Entries[0].Targets[0].Kind)
	assert.Equal(t, "run-1", *got.Entries[0].Targets[0].RunID)

	assert.Equal(t, second.String(), got.Entries[1].EventID, "oldest first, by stream sequence")
	assert.Equal(t, "GAVE_UP", got.Entries[1].Outcome)
	require.Len(t, got.Entries[1].Targets, 3)
	assert.Equal(t, "SIGNALLED", got.Entries[1].Targets[0].Kind)
	assert.Equal(t, "Approved", *got.Entries[1].Targets[0].Signal)
	assert.Equal(t, "FAILED", got.Entries[1].Targets[1].Kind)
	assert.Equal(t, "temporal down", *got.Entries[1].Targets[1].Error)
	assert.Equal(t, "DROPPED", got.Entries[1].Targets[2].Kind)
	assert.Equal(t, "no running execution", *got.Entries[1].Targets[2].Reason)
}

func TestTransactionRouting_EmptyTargetsIsFinishedNotMissing(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)
	tx := uuid.Must(uuid.NewV7())
	e.seed(tenantA, tx, 1, services.RoutingOutcomeDone)

	got := e.query(e.ctx(userA), tx).TransactionRouting

	require.Len(t, got.Entries, 1)
	assert.Empty(t, got.Entries[0].Targets)
}

func TestTransactionRouting_NothingRoutedYetIsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)

	got := e.query(e.ctx(userA), uuid.Must(uuid.NewV7())).TransactionRouting

	assert.Empty(t, got.Entries)
}

func TestTransactionRouting_NoBucketYetIsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)
	require.NoError(t, e.js.DeleteKeyValue(context.Background(), services.RoutingStatusBucket(routingStream)))

	got := e.query(e.ctx(userA), uuid.Must(uuid.NewV7())).TransactionRouting

	assert.Empty(t, got.Entries, "the bucket is created by the first event the router settles")
}

func TestTransactionRouting_TenantIsolation(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)
	tx := uuid.Must(uuid.NewV7()) // the same transaction ID under both tenants

	mine := e.seed(tenantA, tx, 1, services.RoutingOutcomeDone,
		services.RoutingTarget{Kind: services.RoutingTargetStarted, WorkflowID: "mine"})
	theirs := e.seed(tenantB, tx, 2, services.RoutingOutcomeDone,
		services.RoutingTarget{Kind: services.RoutingTargetStarted, WorkflowID: "theirs"})

	t.Run("a tenant sees only its own entries", func(t *testing.T) {
		t.Parallel()

		got := e.query(e.ctx(userA), tx).TransactionRouting

		require.Len(t, got.Entries, 1)
		assert.Equal(t, mine.String(), got.Entries[0].EventID)
		assert.Equal(t, tenantA.String(), got.Entries[0].TenantID)
	})

	t.Run("the other tenant sees only its own", func(t *testing.T) {
		t.Parallel()

		got := e.query(e.ctx(userB), tx).TransactionRouting

		require.Len(t, got.Entries, 1)
		assert.Equal(t, theirs.String(), got.Entries[0].EventID)
	})

	t.Run("a tenant with no entries for the transaction sees none", func(t *testing.T) {
		t.Parallel()

		ctx := request.Context(e.t.Context(), userC, tenantC)

		got := e.query(ctx, tx).TransactionRouting

		assert.Empty(t, got.Entries)
	})

	t.Run("a user without a role in the tenant is turned away before the resolver", func(t *testing.T) {
		t.Parallel()

		closeResp, resp, err := e.SendQuery(t, request.Context(e.t.Context(), userNoRole, tenantA), transactionRouting,
			map[string]any{"TransactionID": tx.String()})
		defer closeResp()
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, "the tenant middleware refuses it; its entries are never read")
	})

	t.Run("a request scoped to both tenants sees both", func(t *testing.T) {
		t.Parallel()

		ctx := request.Context(e.t.Context(), userAB, tenantA, tenantB)

		got := e.query(ctx, tx).TransactionRouting

		require.Len(t, got.Entries, 2)
		assert.Equal(t, []string{mine.String(), theirs.String()}, []string{got.Entries[0].EventID, got.Entries[1].EventID})
	})

	t.Run("a user cannot widen the scope to a tenant it has no role in", func(t *testing.T) {
		t.Parallel()

		closeResp, resp, err := e.SendQuery(t, request.Context(e.t.Context(), userA, tenantA, tenantB), transactionRouting,
			map[string]any{"TransactionID": tx.String()})
		defer closeResp()
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, "the tenant middleware refuses a tenant the user has no role in")
	})
}

func TestTransactionRouting_IgnoresAnEntryWhoseValueDisagreesWithItsKey(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)
	tx := uuid.Must(uuid.NewV7())

	// A value that claims another tenant under this tenant's key must not be
	// returned: the key is what the lookup trusts, so check the value too.
	data, err := json.Marshal(services.RoutingStatus{
		TenantID: tenantB, TransactionID: tx, EventID: uuid.Must(uuid.NewV7()), Outcome: services.RoutingOutcomeDone,
	})
	require.NoError(t, err)

	_, err = e.kv.Put(context.Background(), services.RoutingStatusKey(tenantA, tx, uuid.Must(uuid.NewV7())), data)
	require.NoError(t, err)

	got := e.query(e.ctx(userA), tx).TransactionRouting

	assert.Empty(t, got.Entries)
}

func TestTransactionRouting_RejectsANonUUIDTransactionID(t *testing.T) {
	t.Parallel()

	e := setupRouting(t)

	execErr(e.testEnv, e.ctx(userA), transactionRouting, map[string]any{"TransactionID": "*"}, "")
	execErr(e.testEnv, e.ctx(userA), transactionRouting, map[string]any{"TransactionID": tenantA.String() + ".>"}, "")
}
