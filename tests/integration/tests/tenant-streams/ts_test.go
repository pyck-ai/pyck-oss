//go:build integration

package tenantstreams_test

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const waitFor = 30 * time.Second

func (s *StreamsSuite) tenantID(id string) uuid.UUID {
	tenantID, err := uuid.Parse(id)
	s.Require().NoError(err)

	return tenantID
}

func (s *StreamsSuite) streamName(id string) string {
	return topic.TenantStreamName(s.Cfg.NatsStream, s.tenantID(id))
}

func (s *StreamsSuite) subjects(id string) string {
	return topic.TenantStreamSubjects(s.Cfg.NatsStream, s.tenantID(id))
}

// countOn returns how many messages stream holds on filter.
func (s *StreamsSuite) countOn(stream, filter string) (uint64, error) {
	return natstest.StoredOn(s.Ctx, s.js, stream, filter)
}

// aggregateMirrors waits until the aggregate holds as many messages on filter
// as stream, at least one. More is a duplicate and fails.
func (s *StreamsSuite) aggregateMirrors(stream, filter string) {
	var source, aggregate uint64

	// The aggregate is counted first: a message stored in between then only
	// makes it lag, never exceed its source.
	s.Eventually(func() bool {
		var err error
		if aggregate, err = s.countOn(s.Cfg.NatsStream, filter); err != nil {
			return false
		}
		if source, err = s.countOn(stream, filter); err != nil {
			return false
		}

		return source > 0 && aggregate >= source
	}, waitFor, 200*time.Millisecond, "the aggregate never caught up with %s on %s", stream, filter)

	s.Equal(source, aggregate, "messages on %s in the aggregate and in %s; more is a duplicate", filter, stream)
}

func (s *StreamsSuite) TestRegistrationCreatesTheTenantStream() {
	stream, err := s.js.Stream(s.Ctx, s.streamName(s.own.ID))
	s.Require().NoError(err, "registration must create the tenant stream")
	s.Equal([]string{s.subjects(s.own.ID)}, stream.CachedInfo().Config.Subjects)

	aggregate, err := s.js.Stream(s.Ctx, s.Cfg.NatsStream)
	s.Require().NoError(err)

	cfg := aggregate.CachedInfo().Config
	s.Empty(cfg.Subjects, "the aggregate captures no subjects")
	s.Zero(cfg.Duplicates, "the aggregate must not detect duplicates")

	sources := make([]string, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		sources = append(sources, source.Name)
	}
	s.Contains(sources, topic.SystemStreamName(s.Cfg.NatsStream))
	s.Contains(sources, s.streamName(s.own.ID))
	s.Contains(sources, s.streamName(s.other.ID))
}

// Registration creates the default data types, whose outbox events must land
// in the tenant's stream and reach the aggregate.
func (s *StreamsSuite) TestOutboxEventsReachTheAggregate() {
	filter := strings.TrimSuffix(s.subjects(s.own.ID), ">") + "crud.management.datatype.>"

	s.aggregateMirrors(s.streamName(s.own.ID), filter)
}

// tenantClient connects through the auth callout with the tenant's PAT.
// Refusals are reported on errs.
func (s *StreamsSuite) tenantClient(errs chan error) (*nats.Conn, jetstream.JetStream) {
	nc, err := tests.DialTenantNATS(s.Cfg, s.pat, s.tenantID(s.own.ID),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case errs <- err:
			default:
			}
		}),
	)
	s.Require().NoError(err)
	s.T().Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	s.Require().NoError(err)

	return nc, js
}

// requireDenied publishes to subject and requires the server to refuse it.
func (s *StreamsSuite) requireDenied(nc *nats.Conn, errs chan error, subject string) {
	s.Require().NoError(refused(nc, errs, subject, func() error {
		return nc.Publish(subject, nil)
	}))
}

func (s *StreamsSuite) TestTenantClientReachesOnlyItsStream() {
	errs := make(chan error, 16)
	nc, js := s.tenantClient(errs)

	ctx, cancel := context.WithTimeout(s.Ctx, waitFor)
	defer cancel()

	_, err := js.Stream(ctx, s.streamName(s.own.ID))
	s.Require().NoError(err, "a tenant client must reach its own stream")

	for _, stream := range []string{s.Cfg.NatsStream, topic.SystemStreamName(s.Cfg.NatsStream), s.streamName(s.other.ID)} {
		s.requireDenied(nc, errs, "$JS.API.STREAM.INFO."+stream)
	}

	s.requireDenied(nc, errs, "$JS.API.STREAM.NAMES")
	s.requireDenied(nc, errs, strings.TrimSuffix(s.subjects(s.other.ID), ">")+"events.probe")
	s.requireDenied(nc, errs, topic.SystemStreamName(s.Cfg.NatsStream)+".custom-events")
}

// The issue's attack: a tenant publishes first with the message ID another
// tenant's event will carry.
func (s *StreamsSuite) TestTenantMessageIDCannotSuppressAnotherTenant() {
	_, js := s.tenantClient(make(chan error, 16))

	msgID := "tenant-streams-" + uuid.NewString()
	ownSubject := strings.TrimSuffix(s.subjects(s.own.ID), ">") + "events.probe"
	otherSubject := strings.TrimSuffix(s.subjects(s.other.ID), ">") + "events.probe"

	ack, err := js.Publish(s.Ctx, ownSubject, []byte("{}"), jetstream.WithMsgID(msgID))
	s.Require().NoError(err)
	s.Equal(s.streamName(s.own.ID), ack.Stream)

	ack, err = s.js.Publish(s.Ctx, otherSubject, []byte("{}"), jetstream.WithMsgID(msgID))
	s.Require().NoError(err)
	s.False(ack.Duplicate, "a tenant's message ID suppressed another tenant's event")

	s.aggregateMirrors(s.streamName(s.own.ID), ownSubject)
	s.aggregateMirrors(s.streamName(s.other.ID), otherSubject)
}
