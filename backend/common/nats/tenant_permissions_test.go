package nats_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	natsgo "github.com/pyck-ai/pyck/backend/common/nats"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"
)

const permStream = "pyck"

var testLimits = events.StreamLimits{MaxAge: time.Hour, TenantMaxMsgs: 1000, SystemMaxMsgs: 1000, AggregateMaxMsgs: 10_000}

// tenantClient is a connection with the permissions the auth callout grants a
// client of one tenant.
type tenantClient struct {
	nc   *nats.Conn
	js   jetstream.JetStream
	errs chan error
}

// drainErrors discards the violations of earlier calls, so the next
// assertion sees only what the call it checks raised.
func (c tenantClient) drainErrors(t *testing.T) {
	t.Helper()

	for len(c.errs) > 0 {
		if err := <-c.errs; err != nil {
			t.Logf("discarding an earlier async error: %v", err)
		}
	}
}

// requireDenied publishes to subject and asserts the server refuses it.
func (c tenantClient) requireDenied(t *testing.T, subject string) {
	t.Helper()

	c.drainErrors(t)

	require.NoError(t, c.nc.Publish(subject, nil))
	require.NoError(t, c.nc.Flush())

	select {
	case err := <-c.errs:
		require.ErrorContains(t, err, "Permissions Violation", subject)
	case <-time.After(2 * time.Second):
		t.Fatalf("publishing to %s was allowed", subject)
	}
}

// requireSubscribeDenied subscribes to subject and asserts the server refuses
// it.
func (c tenantClient) requireSubscribeDenied(t *testing.T, subject string) {
	t.Helper()

	c.drainErrors(t)

	_, err := c.nc.SubscribeSync(subject)
	require.NoError(t, err)
	require.NoError(t, c.nc.Flush())

	select {
	case err := <-c.errs:
		require.ErrorContains(t, err, "Permissions Violation for Subscription", subject)
	case <-time.After(2 * time.Second):
		t.Fatalf("subscribing to %s was allowed", subject)
	}
}

// TestTenantPermissions runs a tenant client with the callout's permissions
// against a real server: its JetStream access ends at its own stream.
//
//nolint:tparallel,paralleltest // the subtests share the client and its error channel
func TestTenantPermissions(t *testing.T) {
	t.Parallel()

	own, other := uuid.New(), uuid.New()

	srv := natstest.Start(t, map[string]jwt.Permissions{
		"own": natsgo.TenantPermissions(permStream, own),
	})

	ctx := t.Context()

	service := natstest.Connect(t, srv, natstest.ServiceUser, nil)
	serviceJS, err := events.SetupJetStream(ctx, service, permStream, 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(serviceJS, permStream, 1, natstest.SerialLock(), testLimits)
	require.NoError(t, topology.EnsureTenant(ctx, own))
	require.NoError(t, topology.EnsureTenant(ctx, other))

	errs := make(chan error, 16)
	nc := natstest.Connect(t, srv, "own", errs, nats.CustomInboxPrefix(natsgo.TenantInboxPrefix(own)))
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	client := tenantClient{nc: nc, js: js, errs: errs}

	ownStream := topic.TenantStreamName(permStream, own)
	otherStream := topic.TenantStreamName(permStream, other)
	ownEvent := events.WorkflowEventTopic{StreamName: permStream, TenantID: own, WorkflowID: uuid.New(), WorkflowName: "w"}.String()
	otherEvent := events.WorkflowEventTopic{StreamName: permStream, TenantID: other, WorkflowID: uuid.New(), WorkflowName: "w"}.String()

	t.Run("reads its own stream through its own consumer", func(t *testing.T) {
		_, err := serviceJS.Publish(ctx, ownEvent, []byte("{}"))
		require.NoError(t, err)

		stream, err := js.Stream(ctx, ownStream)
		require.NoError(t, err)
		assert.Equal(t, ownStream, stream.CachedInfo().Config.Name)

		consumer, err := js.CreateOrUpdateConsumer(ctx, ownStream, jetstream.ConsumerConfig{
			Name:          own.String() + "--one",
			Durable:       own.String() + "--one",
			AckPolicy:     jetstream.AckExplicitPolicy,
			FilterSubject: topic.TenantStreamSubjects(permStream, own),
		})
		require.NoError(t, err)

		batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
		require.NoError(t, err)

		var fetched int
		for msg := range batch.Messages() {
			assert.Equal(t, ownEvent, msg.Subject())
			require.NoError(t, msg.Ack())
			fetched++
		}
		require.NoError(t, batch.Error())
		assert.Equal(t, 1, fetched)
	})

	t.Run("publishes to its own stream", func(t *testing.T) {
		ack, err := js.Publish(ctx, ownEvent, []byte("{}"), jetstream.WithMsgID("tenant-chosen-id"))
		require.NoError(t, err)
		assert.Equal(t, ownStream, ack.Stream)

		// The same ID in another stream is no duplicate.
		ack, err = serviceJS.Publish(ctx, otherEvent, []byte("{}"), jetstream.WithMsgID("tenant-chosen-id"))
		require.NoError(t, err)
		assert.False(t, ack.Duplicate, "a tenant's message ID suppressed another tenant's event")
	})

	t.Run("cannot reach another stream", func(t *testing.T) {
		for _, stream := range []string{otherStream, permStream, topic.SystemStreamName(permStream)} {
			client.requireDenied(t, "$JS.API.STREAM.INFO."+stream)
			client.requireDenied(t, "$JS.API.CONSUMER.CREATE."+stream+"."+own.String()+"--one."+topic.TenantStreamSubjects(permStream, own))
			client.requireDenied(t, "$JS.API.CONSUMER.INFO."+stream+"."+own.String()+"--one")
		}
	})

	t.Run("cannot list streams, whose names carry tenant IDs, or consumers", func(t *testing.T) {
		client.requireDenied(t, "$JS.API.STREAM.NAMES")
		client.requireDenied(t, "$JS.API.STREAM.LIST")
		client.requireDenied(t, "$JS.API.CONSUMER.NAMES."+ownStream)
		client.requireDenied(t, "$JS.API.CONSUMER.LIST."+ownStream)
	})

	t.Run("cannot publish outside its own subjects", func(t *testing.T) {
		client.requireDenied(t, otherEvent)
		client.requireDenied(t, events.CustomEventTopic{StreamName: topic.SystemStreamName(permStream)}.String())
		client.requireDenied(t, events.TemporalWorkflowStateChangeTopic{
			StreamName: permStream, Namespace: own.String(),
			TaskQueue: "q", WorkflowTypeName: "t", WorkflowID: "w", RunID: "r", Status: "s",
		}.String())
		client.requireDenied(t, events.MutationEventTopic{
			StreamName: permStream, TenantID: own,
			ServiceName: "management", SchemaName: "tenant", EntityID: own, OperationName: "delete",
		}.String())
		// Custom events go through sendCustomEvent, which needs the writer role.
		client.requireDenied(t, events.TenantCustomEventTopic{
			StreamName: permStream, TenantID: own, EventType: "tour-creation", EntityID: uuid.New(), OperationName: "create",
		}.String())
	})

	t.Run("cannot use a consumer name outside its own", func(t *testing.T) {
		client.requireDenied(t, "$JS.API.CONSUMER.CREATE."+ownStream+".router."+topic.TenantStreamSubjects(permStream, own))
	})

	// The server answers a request on its reply subject without checking it
	// against the client's permissions.
	t.Run("cannot have its requests answered into another stream", func(t *testing.T) {
		targets := map[string]string{
			otherStream:                        topic.TenantStreamSubjects(permStream, other),
			topic.SystemStreamName(permStream): topic.SystemStreamSubjects(permStream),
		}

		for stream, subjects := range targets {
			prefix := strings.TrimSuffix(subjects, ">")

			// The service user shows the answer would be stored.
			control := prefix + "reply-control"
			require.NoError(t, service.PublishRequest("$JS.API.INFO", control, nil))
			require.Eventually(t, func() bool {
				n, err := natstest.StoredOn(ctx, serviceJS, stream, control)
				return err == nil && n == 1
			},
				5*time.Second, 10*time.Millisecond, "no answer stored in %s", stream)

			probe := prefix + "reply-probe"
			for _, subject := range []string{"$JS.API.INFO", "$JS.API.STREAM.INFO." + ownStream, ownEvent} {
				require.NoError(t, nc.PublishRequest(subject, probe, []byte("{}")))
			}

			assert.Never(t, func() bool {
				n, err := natstest.StoredOn(ctx, serviceJS, stream, probe)
				return err != nil || n > 0
			},
				time.Second, 10*time.Millisecond, "answers to a tenant client stored in %s", stream)
		}
	})

	t.Run("subscribes to its own inbox only", func(t *testing.T) {
		client.requireSubscribeDenied(t, "_INBOX.>")
		client.requireSubscribeDenied(t, natsgo.TenantInboxPrefix(other)+".>")
		client.requireSubscribeDenied(t, topic.TenantStreamSubjects(permStream, own))
		client.requireSubscribeDenied(t, "$JS.API.INFO")
	})

	// Each cross-account request leaves a response mapping in the stream
	// account until it is answered; a call that answers once must not leave
	// one behind, or a client calling in a loop fills the server's memory.
	t.Run("leaves no pending response behind its calls", func(t *testing.T) {
		account, err := srv.LookupAccount(natstest.StreamAccount)
		require.NoError(t, err)

		// Earlier pulls may still hold theirs until the response threshold.
		before := account.NumPendingAllResponses()

		for range 200 {
			_, err := js.AccountInfo(ctx)
			require.NoError(t, err)
		}
		_, err = js.Stream(ctx, ownStream)
		require.NoError(t, err)
		_, err = js.Consumer(ctx, ownStream, own.String()+"--one")
		require.NoError(t, err)

		assert.LessOrEqual(t, account.NumPendingAllResponses(), before)
	})
}
