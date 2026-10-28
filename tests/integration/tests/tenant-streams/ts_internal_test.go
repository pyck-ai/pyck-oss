//go:build integration

package tenantstreams_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/pyck-ai/pyck/backend/common/events/topic"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// quietWindow is the shortest time an answer that must not be stored is
	// watched for. The server stores one within milliseconds; the window
	// grows with the control's latency (see
	// TestReplySubjectCannotWriteIntoForeignStreams).
	quietWindow = 5 * time.Second

	// refusalWait bounds how long a refusal by the server is waited for. It
	// answers a forbidden publish or subscribe at once; silence means it was
	// allowed.
	refusalWait = 5 * time.Second
)

// prefix returns the subjects of a stream without their trailing wildcard.
func prefix(subjects string) string {
	return strings.TrimSuffix(subjects, ">")
}

// each returns every call on every stream; a call is a format taking the
// stream name.
func each(streams []string, calls ...string) []string {
	subjects := make([]string, 0, len(streams)*len(calls))
	for _, stream := range streams {
		for _, call := range calls {
			subjects = append(subjects, fmt.Sprintf(call, stream))
		}
	}

	return subjects
}

// drain discards the violations of earlier calls; nil is never sent.
func drain(errs chan error) {
	for len(errs) > 0 {
		if err := <-errs; err == nil {
			return
		}
	}
}

// refused reports whether the server answers act with a permissions
// violation naming subject. Violations of earlier calls are skipped.
func refused(nc *nats.Conn, errs chan error, subject string, act func() error) error {
	drain(errs)

	if err := act(); err != nil {
		return fmt.Errorf("%s: %w", subject, err)
	}
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flush after %s: %w", subject, err)
	}

	timeout := time.After(refusalWait)
	for {
		select {
		case err := <-errs:
			if errors.Is(err, nats.ErrPermissionViolation) && strings.Contains(err.Error(), `"`+subject+`"`) {
				return nil
			}
		case <-timeout:
			return fmt.Errorf("the server accepted %s from a tenant client", subject)
		}
	}
}

// The server answers a JetStream request on its reply subject from inside the
// account, without checking that subject against the client's permissions, and
// a stream capturing the subject stores the answer. A tenant client must not
// be able to write into another tenant's or the system's stream that way: it
// would forge events there and, through the stream's message cap, push the
// stored ones out. The tenant account keeps the answer from any stream.
func (s *StreamsSuite) TestReplySubjectCannotWriteIntoForeignStreams() {
	nc, _ := s.tenantClient(make(chan error, 16))

	// Requests the server answers on their reply subject: the account info,
	// the info of the client's own stream, and the ack of a publish to it.
	requests := []string{
		"$JS.API.INFO",
		"$JS.API.STREAM.INFO." + s.streamName(s.own.ID),
		prefix(s.subjects(s.own.ID)) + "events.reply-probe",
	}

	targets := []struct{ name, stream, prefix string }{
		{"another tenant", s.streamName(s.other.ID), prefix(s.subjects(s.other.ID))},
		{"the system", topic.SystemStreamName(s.Cfg.NatsStream), prefix(topic.SystemStreamSubjects(s.Cfg.NatsStream))},
	}

	for _, target := range targets {
		s.Run(target.name, func() {
			stored := func(subject string) func() error {
				return func() error {
					n, err := s.countOn(target.stream, subject)
					if err != nil {
						return err
					}
					if n == 0 {
						return fmt.Errorf("nothing stored on %s", subject)
					}
					return nil
				}
			}

			// The service client, which may do so, shows that the answer is
			// stored and how fast; otherwise a refused leak and a slow one
			// look alike.
			control := target.prefix + "events.reply-control-" + uuid.NewString()
			s.Require().NoError(s.service.PublishRequest("$JS.API.INFO", control, nil))
			elapsed, err := tests.PollUntilElapsed(s.Ctx, waitFor, 50*time.Millisecond, stored(control))
			s.Require().NoError(err, "the server never stored a JetStream answer in %s", target.stream)

			probe := target.prefix + "events.reply-probe-" + uuid.NewString()
			for _, subject := range requests {
				// A refusal of the publish itself, reported asynchronously, is
				// as good as a dropped answer.
				s.Require().NoError(nc.PublishRequest(subject, probe, []byte("{}")))
			}
			s.Require().NoError(nc.Flush())

			window := max(quietWindow, 10*elapsed)
			err = tests.PollStable(s.Ctx, window, 200*time.Millisecond, func() error {
				n, err := s.countOn(target.stream, probe)
				if err != nil {
					return err
				}
				if n > 0 {
					return fmt.Errorf("%d of %d answers to a tenant client's requests stored in %s on %s", n, len(requests), target.stream, probe)
				}
				return nil
			})
			s.Require().NoError(err)
		})
	}
}

// A tenant client gets the JetStream API for its own stream and its own
// consumers only (tenantPermissions). Every other API call and every
// server-internal subject must be refused, both to publish and to subscribe.
func (s *StreamsSuite) TestTenantClientCannotUseInternalSubjects() {
	errs := make(chan error, 64)
	nc, _ := s.tenantClient(errs)

	own := s.streamName(s.own.ID)
	other := s.streamName(s.other.ID)
	aggregate := s.Cfg.NatsStream
	system := topic.SystemStreamName(aggregate)
	consumer := s.own.ID + "--one"
	ownSubjects := s.subjects(s.own.ID)
	otherSubjects := s.subjects(s.other.ID)

	publish := slices.Concat(
		// Accepted, each of these would change or remove a stream, so they
		// name the suite's two throwaway tenants only, never the shared
		// streams.
		each([]string{own, other},
			"$JS.API.STREAM.CREATE.%s", "$JS.API.STREAM.UPDATE.%s",
			"$JS.API.STREAM.DELETE.%s", "$JS.API.STREAM.PURGE.%s",
			"$JS.API.STREAM.MSG.DELETE.%s", "$JS.API.STREAM.SNAPSHOT.%s",
			"$JS.API.STREAM.RESTORE.%s", "$JS.API.STREAM.PEER.REMOVE.%s",
			"$JS.API.STREAM.LEADER.STEPDOWN.%s", "$JS.API.STREAM.CANCEL_MOVE.%s",
		),
		each([]string{own, other, aggregate, system},
			"$JS.API.STREAM.MSG.GET.%s", "$JS.API.DIRECT.GET.%s",
			"$JS.API.CONSUMER.NAMES.%s", "$JS.API.CONSUMER.LIST.%s",
			"$JS.API.CONSUMER.CREATE.%s", "$JS.API.CONSUMER.DURABLE.CREATE.%s.probe",
		),
		// The client's own consumer name, but on a stream that is not its own.
		each([]string{other, aggregate, system},
			"$JS.API.CONSUMER.CREATE.%s."+consumer+"."+ownSubjects,
			"$JS.API.CONSUMER.INFO.%s."+consumer,
			"$JS.API.CONSUMER.MSG.NEXT.%s."+consumer,
			"$JS.API.CONSUMER.DELETE.%s."+consumer,
			"$JS.ACK.%s."+consumer+".1.1.1.1.0",
		),
		// The own stream, but a filter, a consumer name or a call outside
		// the grant.
		each([]string{own},
			"$JS.API.CONSUMER.CREATE.%s."+consumer+"."+otherSubjects,
			"$JS.API.CONSUMER.CREATE.%s."+s.other.ID+"--one."+ownSubjects,
			"$JS.API.CONSUMER.CREATE.%s.probe."+ownSubjects,
			"$JS.API.CONSUMER.PAUSE.%s."+consumer,
			"$JS.API.CONSUMER.RESET.%s."+consumer,
			"$JS.API.CONSUMER.UNPIN.%s."+consumer,
			"$JS.API.CONSUMER.LEADER.STEPDOWN.%s."+consumer,
			"$JS.API.CONSUMER.PEER.REMOVE.%s."+consumer,
			"$JS.API.ACCOUNT.STREAM.MOVE.%s.probe",
		),
		[]string{
			"$JS.API.CONSUMER.DELETE." + other + "." + s.other.ID + "--one",

			// Account and server calls.
			"$JS.API.STREAM.NAMES",
			"$JS.API.STREAM.LIST",
			"$JS.API.META.LEADER.STEPDOWN",
			"$JS.API.SERVER.REMOVE",
			"$JS.API.ACCOUNT.PURGE.PROBE",

			// Server-internal subjects: system requests (the auth
			// callout's included), advisories, flow control, key-value and
			// object stores, service discovery, and other connections'
			// inboxes, where a publish would forge the answer to their
			// request.
			"$SYS.REQ.USER.AUTH",
			"$SYS.REQ.USER.INFO",
			"$SYS.REQ.SERVER.PING",
			"$JS.EVENT.ADVISORY.probe",
			"$JS.FC.probe",
			"$KV.probe.key",
			"$O.probe.chunk",
			"$SRV.PING",
			"_INBOX.probe",

			// Event subjects outside the client's own, and its own events
			// no tenant may forge.
			prefix(otherSubjects) + "events.probe",
			system + ".probe",
			aggregate + ".probe",
			prefix(ownSubjects) + "crud.management.datatype.created",
			prefix(ownSubjects) + "temporal.probe",
		},
	)

	subscribe := []string{
		">",
		"$JS.API.>",
		"$JS.API.STREAM.INFO." + other,
		"$JS.EVENT.>",
		"$JS.ACK." + aggregate + ".>",
		"$JS.ACK." + other + ".>",
		"$SYS.>",
		"$KV.>",
		"$O.>",
		"$SRV.>",
		aggregate + ".>",
		otherSubjects,
		system + ".>",
		prefix(ownSubjects) + "crud.>",
	}

	for _, subject := range publish {
		s.Run("publish "+subject, func() {
			s.Require().NoError(refused(nc, errs, subject, func() error {
				return nc.Publish(subject, nil)
			}))
		})
	}

	// The subscriptions end with the connection.
	for _, subject := range subscribe {
		s.Run("subscribe "+subject, func() {
			s.Require().NoError(refused(nc, errs, subject, func() error {
				_, err := nc.SubscribeSync(subject)
				return err
			}))
		})
	}
}

// Every tenant client shares the tenant account, and with it the reply
// subjects: a client subscribed to every inbox would receive the answers to
// other tenants' requests, their JetStream fetches among them.
func (s *StreamsSuite) TestTenantClientCannotSubscribeToEveryInbox() {
	errs := make(chan error, 16)
	nc, _ := s.tenantClient(errs)

	s.Require().NoError(refused(nc, errs, "_INBOX.>", func() error {
		_, err := nc.SubscribeSync("_INBOX.>")
		return err
	}))
}
