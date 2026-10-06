//go:build integration

package authroles

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/pyck-ai/pyck/backend/common/events"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// customEventProbeType is the event type the sendCustomEvent tests publish.
// No workflow listens on it, so the only effect of a published event is the
// NATS request the outbox relay sends.
const customEventProbeType = "ar-custom-event-probe"

// TestSendCustomEventRefusesReader pins that a READER cannot publish a custom
// event. sendCustomEvent writes straight to the outbox, which no ent privacy
// policy guards, so the resolver's own role check is the only barrier. The test
// watches the relay's fire-and-forget mutation-event subject for the tenant and
// event type: the READER's call must fail and its event must never be
// published. WRITER control events bound the wait: the relay claims rows with
// FOR UPDATE SKIP LOCKED and publishes a batch in no fixed order, so a READER
// row committed before the first control could still be published just after
// it. The second control is written only once the first was seen, so a later
// relay pass publishes it, after the pass that would have carried the READER's
// row.
func (s *AuthRolesSuite) TestSendCustomEventRefusesReader() {
	readerToken := s.provisionUser("reader")
	_, err := tests.WaitTokenReady(s.Ctx, s.Cfg, readerToken, s.tenant.ID, 30*time.Second)
	s.Require().NoError(err, "reader PAT never became usable")
	writerToken := s.provisionReadyWriter()

	nc, err := nats.Connect(s.Cfg.NatsURL)
	s.Require().NoError(err, "nats connect")
	defer nc.Close()

	// The nil entity id and empty operation render as wildcards, so the
	// subscription sees both the READER's and the WRITER's event.
	published := make(chan *nats.Msg, 16)
	subject := events.MutationEventTopic{
		StreamName:  s.Cfg.NatsStream,
		TenantID:    s.tenantUUID(),
		ServiceName: "management",
		SchemaName:  customEventProbeType,
	}.String()
	sub, err := nc.ChanSubscribe(subject, published)
	s.Require().NoError(err, "subscribe %s", subject)
	defer func() { s.NoError(sub.Unsubscribe(), "unsubscribe %s", subject) }()
	s.Require().NoError(nc.Flush(), "flush subscription")

	readerEventID := uuid.New()
	_, readerErr := sendProbeEvent(s, gateway.NewClientForTenant(s.Cfg, readerToken, s.tenant.ID), readerEventID)

	writer := gateway.NewClientForTenant(s.Cfg, writerToken, s.tenant.ID)
	var readerPublished bool
	for range 2 {
		controlID := uuid.New()
		_, err = sendProbeEvent(s, writer, controlID)
		s.Require().NoError(err, "writer control call must succeed")
		if s.awaitProbeEvent(published, controlID, readerEventID) {
			readerPublished = true
		}
	}

	s.False(readerPublished, "a READER's event must never reach NATS")
	if s.Error(readerErr, "sendCustomEvent must refuse a READER") {
		s.Contains(readerErr.Error(), "writer role required for sendCustomEvent")
	}
}

// TestSendCustomEventAllowsServiceToken pins that the WRITER check does not
// lock out the system service token, which workflow activities use to call
// sendCustomEvent: its event is accepted and the relay publishes it on the
// tenant's fire-and-forget mutation-event subject. A guard that ignored
// ROLE_SYSTEM would fail the call.
func (s *AuthRolesSuite) TestSendCustomEventAllowsServiceToken() {
	nc, err := nats.Connect(s.Cfg.NatsURL)
	s.Require().NoError(err, "nats connect")
	defer nc.Close()

	eventID := uuid.New()
	subject := events.MutationEventTopic{
		StreamName:    s.Cfg.NatsStream,
		TenantID:      s.tenantUUID(),
		ServiceName:   "management",
		SchemaName:    customEventProbeType,
		EntityID:      eventID,
		OperationName: "created",
	}.String()
	sub, err := nc.SubscribeSync(subject)
	s.Require().NoError(err, "subscribe %s", subject)
	defer func() { s.NoError(sub.Unsubscribe(), "unsubscribe %s", subject) }()
	s.Require().NoError(nc.Flush(), "flush subscription")

	res, err := sendProbeEvent(s, s.adminClient(), eventID)
	s.Require().NoError(err, "service token must be allowed to send a custom event")
	s.True(res.GetSendCustomEvent().GetSuccess(), "sendCustomEvent success flag")

	_, err = sub.NextMsg(30 * time.Second)
	s.Require().NoError(err, "service token event was never published on %s", subject)
}

// awaitProbeEvent reads published until the event for controlID arrives and
// reports whether an event for forbiddenID arrived on the way. It fails the
// test if the control event is not published within 30 seconds.
func (s *AuthRolesSuite) awaitProbeEvent(published <-chan *nats.Msg, controlID, forbiddenID uuid.UUID) bool {
	s.T().Helper()

	var sawForbidden bool
	deadline := time.After(30 * time.Second)
	for {
		select {
		case msg := <-published:
			switch {
			case strings.Contains(msg.Subject, forbiddenID.String()):
				sawForbidden = true
			case strings.Contains(msg.Subject, controlID.String()):
				return sawForbidden
			}
		case <-deadline:
			s.FailNow("writer control event was never published", "control %s", controlID)
		}
	}
}

// sendProbeEvent publishes one probe custom event with the given entity id.
func sendProbeEvent(s *AuthRolesSuite, c managementapi.Client, id uuid.UUID) (*managementapi.SendCustomEvent, error) {
	return c.SendCustomEvent(s.Ctx, managementapi.SendCustomEventArgs{
		Input: managementmodel.SendCustomEventInput{
			Type:      customEventProbeType,
			Operation: "created",
			Payload:   &managementmodel.CustomEventPayloadInput{ID: id},
		},
	})
}
