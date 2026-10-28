//go:build integration

package tenants

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// eventWait bounds the wait for an already-committed tenant mutation to
// reach JetStream: the outbox drainer publishes on its own cadence, well
// under this budget locally.
const eventWait = 30 * time.Second

// TnTenantEventsSuite reads the tenant CRUD events management publishes
// and asserts their payload, which no unit test can: only a live stack
// runs the production hook wiring, the outbox drainer and the real
// subject.
//
// Both events under test are written by a Temporal activity, not by a
// resolver, and both used to be committed with a bulk
// Update().Where(...): the affected-row count then reached the hook as
// the post-mutation value, so the registration flip published
// data_after=1, dropped by every service's tenant-event consumer
// before it evicts its cached liveness verdict, and the expiry sweep
// published nothing at all, because a count carries no tenant to build
// the subject from. Both regressions leave the row correct, so only the
// payload proves the fix.
type TnTenantEventsSuite struct {
	tests.Base

	nc *nats.Conn
	js jetstream.JetStream
}

func (s *TnTenantEventsSuite) SetupSuite() {
	s.Base.SetupSuite()

	nc, err := nats.Connect(s.Cfg.NatsURL)
	s.Require().NoError(err, "nats connect")
	s.nc = nc

	js, err := jetstream.New(nc)
	s.Require().NoError(err, "jetstream context")
	s.js = js
}

func (s *TnTenantEventsSuite) TearDownSuite() {
	if s.nc != nil {
		s.nc.Close()
	}
	s.Base.TearDownSuite()
}

// TestTenantEventsCarryTheRow registers a tenant, then expires it, and
// reads both events off the stream. The tenant is its own event tenant
// (the Tenant schema has no tenant_id column), so both the subject's
// tenant segment and the payload's tenant_id must be the tenant's ID.
func (s *TnTenantEventsSuite) TestTenantEventsCarryTheRow() {
	r := s.Require()

	tenant := fixtures.NewTenant()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	r.NotEmpty(rt.ID)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status, "the tenant must be fully registered before its flip event is read")

	// The flip from pending to done: the event every service's
	// tenant-event consumer needs in order to evict the "not live"
	// verdict it cached while the tenant was still pending.
	if !s.Run("registration flip publishes the row", func() {
		r := s.Require()

		payload := s.awaitEvent(rt.ID, events.OpUpdate, eventWait)
		r.Equal(events.OpUpdate, payload["operation"])
		r.Equal(rt.ID, payload["tenant_id"], "the tenant is its own event tenant")
		r.Equal(rt.ID, payload["id"])

		before := s.dataMap(payload, "data_before")
		after := s.dataMap(payload, "data_after")
		r.Equal("pending", before["status"], "the event must name the state it left")
		r.Equal("done", after["status"], "the event must name the state consumers act on")
		r.NotContains(string(s.encode(payload)), tenant.AdminPassword, "no admin secret may reach the stream")
	}) {
		return
	}

	// The expiry sweep's soft delete: the event DisableTenantWorkflow
	// and the per-service token eviction hang off. Without it the
	// tenant only gets disabled when the reconcile sweep notices the
	// tombstone, minutes later.
	s.Run("expiry sweep publishes the soft-deleted row", func() {
		r := s.Require()

		expiresAt := time.Now().UTC().Add(5 * time.Second)
		r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, &expiresAt))

		payload := s.awaitEvent(rt.ID, events.OpDelete, s.Cfg.TenantExpiryCheckInterval+eventWait)
		r.Equal(events.OpDelete, payload["operation"], "a soft delete must be classified as a delete")
		r.Equal(rt.ID, payload["tenant_id"])

		before := s.dataMap(payload, "data_before")
		after := s.dataMap(payload, "data_after")
		r.False(topic.IsDeletedAt(before["deleted_at"]), "data_before must be the live row the sweep found")
		r.True(topic.IsDeletedAt(after["deleted_at"]), "data_after.deleted_at must mark the row deleted, got %v", after["deleted_at"])
	})
}

// awaitEvent returns the payload of the first message published on the
// tenant's own <op> subject, waiting up to timeout for it. The stream
// keeps messages (LimitsPolicy), so an ordered consumer replaying from
// the start finds events published before this call.
func (s *TnTenantEventsSuite) awaitEvent(tenantID, op string, timeout time.Duration) map[string]any {
	s.T().Helper()
	r := s.Require()

	subject := fmt.Sprintf("%s.%s.crud.%s.%s.%s.%s",
		s.Cfg.NatsStream, tenantID, topic.ManagementService, topic.TenantSchema, tenantID, op)

	stream, err := s.js.Stream(s.Ctx, s.Cfg.NatsStream)
	r.NoError(err, "stream %s", s.Cfg.NatsStream)

	cons, err := stream.OrderedConsumer(s.Ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subject},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	r.NoError(err, "ordered consumer on %s", subject)

	msg, err := cons.Next(jetstream.FetchMaxWait(timeout))
	r.NoError(err, "no %s event on %s within %s", op, subject, timeout)
	s.T().Logf("event subject=%s", msg.Subject())

	var payload map[string]any
	r.NoError(json.Unmarshal(msg.Data(), &payload), "event payload: %s", msg.Data())
	return payload
}

// dataMap reads payload[key] as the row a consumer expects. A bulk
// update publishes the affected-row count here instead, which is the
// regression this suite exists for.
func (s *TnTenantEventsSuite) dataMap(payload map[string]any, key string) map[string]any {
	s.T().Helper()
	data, ok := payload[key].(map[string]any)
	s.Require().True(ok, "%s must decode to the tenant row, got %T (%v)", key, payload[key], payload[key])
	return data
}

func (s *TnTenantEventsSuite) encode(payload map[string]any) []byte {
	s.T().Helper()
	b, err := json.Marshal(payload)
	s.Require().NoError(err)
	return b
}
