//go:build integration

package natsinboxisolation_test

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// payloadPreview bounds how much of a captured payload a failure prints.
const payloadPreview = 200

type tenantCtx struct {
	RT  *gateway.RegisteredTenant
	PAT string
}

// ID returns the tenant's id.
func (t *tenantCtx) ID(s *NatsInboxSuite) uuid.UUID {
	id, err := uuid.Parse(t.RT.ID)
	s.Require().NoError(err, "tenant id %q", t.RT.ID)

	return id
}

// provision registers a fresh tenant and a reader that belongs to it alone,
// so the callout places the user's connections in that tenant.
func (s *NatsInboxSuite) provision(label string) *tenantCtx {
	s.T().Helper()
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant %s", label)
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, []string{"reader"})
	r.NoError(err, "provision user in %s", label)

	return &tenantCtx{RT: rt, PAT: p.PAT}
}

// asyncErrors collects a connection's async errors, such as refusals.
type asyncErrors struct {
	mu   sync.Mutex
	errs []error
}

func (a *asyncErrors) handler(_ *nats.Conn, _ *nats.Subscription, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.errs = append(a.errs, err)
}

func (a *asyncErrors) find(substr string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, err := range a.errs {
		if strings.Contains(err.Error(), substr) {
			return err
		}
	}
	return nil
}

func (a *asyncErrors) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fmt.Sprint(a.errs)
}

// dialTenant connects with the tenant's token through the auth callout.
func (s *NatsInboxSuite) dialTenant(t *tenantCtx) (*nats.Conn, *asyncErrors) {
	s.T().Helper()

	errs := &asyncErrors{}
	nc, err := tests.DialTenantNATS(s.Cfg, t.PAT, t.ID(s),
		nats.Name("nats-inbox-isolation "+t.RT.ID),
		nats.ErrorHandler(errs.handler),
	)
	s.Require().NoError(err)
	s.T().Cleanup(nc.Close)

	return nc, errs
}

// dialAccount connects as the services' account user, like the platform
// services.
func (s *NatsInboxSuite) dialAccount() *nats.Conn {
	s.T().Helper()

	nc, err := nats.Connect(s.Cfg.NatsURL, nats.Name("nats-inbox-isolation control"))
	s.Require().NoError(err, "connect as the account user to %s", s.Cfg.NatsURL)
	s.T().Cleanup(nc.Close)

	return nc
}

// capture records every message of one subscription whose subject or
// payload mentions any of the needles.
type capture struct {
	needles []string

	mu   sync.Mutex
	msgs []*nats.Msg
}

func (c *capture) handle(msg *nats.Msg) {
	for _, needle := range c.needles {
		if strings.Contains(msg.Subject, needle) || strings.Contains(string(msg.Data), needle) {
			c.mu.Lock()
			c.msgs = append(c.msgs, msg)
			c.mu.Unlock()
			return
		}
	}
}

// matching returns the captured messages whose subject or payload contains
// substr.
func (c *capture) matching(substr string) []*nats.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*nats.Msg
	for _, msg := range c.msgs {
		if strings.Contains(msg.Subject, substr) || strings.Contains(string(msg.Data), substr) {
			out = append(out, msg)
		}
	}
	return out
}

func (c *capture) all() []*nats.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*nats.Msg(nil), c.msgs...)
}

// describe renders each message as its subject and the start of its payload.
func describe(msgs []*nats.Msg) string {
	var b strings.Builder
	for _, msg := range msgs {
		data := msg.Data
		if len(data) > payloadPreview {
			data = data[:payloadPreview]
		}
		fmt.Fprintf(&b, "\n  subject=%s payload=%q", msg.Subject, data)
	}
	return b.String()
}

// subscribeCapture subscribes nc to subject and flushes, so the server has
// accepted or refused it on return.
func (s *NatsInboxSuite) subscribeCapture(nc *nats.Conn, subject string, needles ...string) *capture {
	s.T().Helper()

	c := &capture{needles: needles}
	sub, err := nc.Subscribe(subject, c.handle)
	s.Require().NoError(err, "subscribe %s", subject)
	t := s.T()
	t.Cleanup(func() {
		if err := sub.Unsubscribe(); err != nil {
			t.Logf("cleanup: unsubscribe %s: %v", subject, err)
		}
	})
	s.Require().NoError(nc.FlushTimeout(5*time.Second), "flush subscription to %s", subject)

	return c
}
