//go:build integration

package natsinboxisolation_test

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/events/topic"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	deliveryTimeout = 30 * time.Second
	// quietWindow is how long A must stay silent after the control saw
	// B's data.
	quietWindow    = 3 * time.Second
	refusalTimeout = 5 * time.Second
	pollInterval   = 100 * time.Millisecond
)

// TestTenantTokenCannotReadOtherTenantsInboxes: A's _INBOX.> subscription is
// refused and sees none of B's data, which reaches inboxes two ways: the
// services fetch B's tenant-row event into their account's inboxes, which a
// control on the services' account user must see, and B fetches its own
// marker into its inbox in the tenant account, which B's fetch receiving it
// shows. Without both, A's silence would mean nothing.
func (s *NatsInboxSuite) TestTenantTokenCannotReadOtherTenantsInboxes() {
	r := s.Require()
	a, b := s.provision("A"), s.provision("B")
	bID := b.RT.ID
	marker := "nats-inbox-isolation-marker-" + uuid.NewString()
	s.T().Logf("tenant A=%s, tenant B=%s, marker=%s", a.RT.ID, bID, marker)

	control := s.subscribeCapture(s.dialAccount(), "_INBOX.>", bID)

	aConn, aErrs := s.dialTenant(a)
	leaked := s.subscribeCapture(aConn, "_INBOX.>", bID, marker)

	// Platform path: B's tenant row changes; the services' tenant-events
	// consumers fetch the event into their inboxes.
	expiry := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, bID, &expiry), "setTenantExpiry on B")

	// Tenant path: B fetches its own marker message into its own inbox, in
	// the account A shares.
	s.fetchOwnMarker(b, marker)

	tenantRowEvent := ".crud.management.tenant." + bID + "."
	r.NoError(tests.PollUntil(s.Ctx, deliveryTimeout, pollInterval, func() error {
		if len(control.matching(tenantRowEvent)) == 0 {
			return errors.New("no platform consumer fetched B's tenant row event into an inbox")
		}
		return nil
	}), "control: B's data must travel through inboxes, else A's silence proves nothing; control saw:%s",
		describe(control.all()))
	s.T().Logf("control saw %d inbox messages carrying B's data:%s", len(control.all()), describe(control.all()))

	s.NoError(tests.PollStable(s.Ctx, quietWindow, pollInterval, func() error {
		if got := leaked.all(); len(got) > 0 {
			return fmt.Errorf("tenant A's _INBOX.> subscription received %d messages carrying tenant B's data:%s",
				len(got), describe(got))
		}
		return nil
	}), "tenant A read tenant B's data from other connections' inboxes")

	refusal := `Subscription to "_INBOX.>"`
	s.NoError(tests.PollUntil(s.Ctx, refusalTimeout, pollInterval, func() error {
		err := aErrs.find(refusal)
		if err == nil {
			return fmt.Errorf("no refusal yet; async errors so far: %s", aErrs)
		}
		if !errors.Is(err, nats.ErrPermissionViolation) {
			return fmt.Errorf("refusal is not a permissions violation: %w", err)
		}
		return nil
	}), "the server must refuse tenant A's subscription to _INBOX.> with a permissions violation")
}

// fetchOwnMarker publishes marker on the tenant's subject and fetches it
// through one of its consumer slots, into the tenant's inbox.
func (s *NatsInboxSuite) fetchOwnMarker(t *tenantCtx, marker string) {
	s.T().Helper()
	r := s.Require()

	nc, _ := s.dialTenant(t)
	js, err := jetstream.New(nc)
	r.NoError(err, "jetstream for tenant %s", t.RT.ID)

	stream := topic.TenantStreamName(s.Cfg.NatsStream, t.ID(s))
	subject := fmt.Sprintf("%s.%s.events.%s", s.Cfg.NatsStream, t.RT.ID, marker)
	_, err = js.Publish(s.Ctx, subject, []byte(marker+" tenant="+t.RT.ID))
	r.NoError(err, "tenant %s: publish the marker on its own subject %s", t.RT.ID, subject)

	consumerName := t.RT.ID + "--one"
	consumer, err := js.CreateOrUpdateConsumer(s.Ctx, stream, jetstream.ConsumerConfig{
		Name:              consumerName,
		Durable:           consumerName,
		FilterSubject:     subject,
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		InactiveThreshold: time.Minute,
	})
	r.NoError(err, "tenant %s: create its consumer slot %s", t.RT.ID, consumerName)
	test := s.T()
	test.Cleanup(func() {
		if err := js.DeleteConsumer(s.Ctx, stream, consumerName); err != nil {
			test.Logf("cleanup: delete consumer %s: %v", consumerName, err)
		}
	})

	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(10*time.Second))
	r.NoError(err, "tenant %s: fetch the marker", t.RT.ID)
	var fetched int
	for msg := range batch.Messages() {
		fetched++
		r.NoError(msg.Ack(), "tenant %s: ack the marker", t.RT.ID)
	}
	r.NoError(batch.Error(), "tenant %s: fetch batch", t.RT.ID)
	r.Equal(1, fetched, "tenant %s: the fetch must deliver the marker", t.RT.ID)
}

// TestTenantTokenCannotSubscribeToAnotherTenantsPrefix: tenant ids are not
// secret, so A aims at B's inbox prefix and the wildcards covering it. Each
// subscription is refused and sees none of B's fetched marker, which B's
// fetch receives on that prefix.
func (s *NatsInboxSuite) TestTenantTokenCannotSubscribeToAnotherTenantsPrefix() {
	a, b := s.provision("A"), s.provision("B")
	bID := b.RT.ID
	marker := "nats-inbox-isolation-prefix-marker-" + uuid.NewString()
	s.T().Logf("tenant A=%s, tenant B=%s, marker=%s", a.RT.ID, bID, marker)

	bPrefix := "_INBOX." + bID + ".>"

	aConn, aErrs := s.dialTenant(a)
	attacks := []string{
		bPrefix,
		"_INBOX." + strings.ToUpper(bID) + ".>",
		"_INBOX.*.>",
		"_INBOX.*.*",
		"_INBOX.*.*.*",
		">",
	}
	leaks := make(map[string]*capture, len(attacks))
	for _, subject := range attacks {
		leaks[subject] = s.subscribeCapture(aConn, subject, marker)
	}

	s.fetchOwnMarker(b, marker)

	s.Require().NoError(tests.PollStable(s.Ctx, quietWindow, pollInterval, func() error {
		for subject, leaked := range leaks {
			if got := leaked.all(); len(got) > 0 {
				return fmt.Errorf("tenant A's subscription to %s received tenant B's marker:%s", subject, describe(got))
			}
		}
		return nil
	}), "tenant A read tenant B's fetched data on B's inbox prefix")

	for _, subject := range attacks {
		refusal := fmt.Sprintf("Subscription to %q", subject)
		s.NoError(tests.PollUntil(s.Ctx, refusalTimeout, pollInterval, func() error {
			err := aErrs.find(refusal)
			if err == nil {
				return fmt.Errorf("no refusal of %s yet; async errors so far: %s", subject, aErrs)
			}
			if !errors.Is(err, nats.ErrPermissionViolation) {
				return fmt.Errorf("refusal of %s is not a permissions violation: %w", subject, err)
			}
			return nil
		}), "the server must refuse tenant A's subscription to %s", subject)
	}
}

// TestTenantTokenCannotSnoopSharedInfoRequests: requests on the shared INFO
// subjects carry the sender's inbox, which names its tenant. B's requests
// are answered; A's subscriptions there are refused and see nothing.
func (s *NatsInboxSuite) TestTenantTokenCannotSnoopSharedInfoRequests() {
	r := s.Require()
	a, b := s.provision("A"), s.provision("B")
	s.T().Logf("tenant A=%s, tenant B=%s", a.RT.ID, b.RT.ID)

	subjects := []string{"$JS.API.INFO", "$JS.API.STREAM.INFO." + topic.TenantStreamName(s.Cfg.NatsStream, b.ID(s))}

	// B's id is only in the reply subject, which capture ignores: record all.
	aConn, aErrs := s.dialTenant(a)
	leaks := make(map[string]*capture, len(subjects))
	for _, subject := range subjects {
		leaks[subject] = s.subscribeCapture(aConn, subject, "")
	}

	bConn, _ := s.dialTenant(b)
	for _, subject := range subjects {
		reply, err := bConn.Request(subject, nil, deliveryTimeout)
		r.NoError(err, "tenant B: request on %s must be answered", subject)
		r.NotEmpty(reply.Data, "tenant B: empty reply on %s", subject)
	}

	r.NoError(tests.PollStable(s.Ctx, quietWindow, pollInterval, func() error {
		for subject, leaked := range leaks {
			if got := leaked.all(); len(got) > 0 {
				return fmt.Errorf("tenant A's subscription to %s saw a request:%s", subject, describe(got))
			}
		}
		return nil
	}), "tenant A saw other tenants' requests on a shared info subject")

	for _, subject := range subjects {
		refusal := fmt.Sprintf("Subscription to %q", subject)
		s.NoError(tests.PollUntil(s.Ctx, refusalTimeout, pollInterval, func() error {
			err := aErrs.find(refusal)
			if err == nil {
				return fmt.Errorf("no refusal of %s yet; async errors so far: %s", subject, aErrs)
			}
			if !errors.Is(err, nats.ErrPermissionViolation) {
				return fmt.Errorf("refusal of %s is not a permissions violation: %w", subject, err)
			}
			return nil
		}), "the server must refuse tenant A's subscription to %s", subject)
	}
}
