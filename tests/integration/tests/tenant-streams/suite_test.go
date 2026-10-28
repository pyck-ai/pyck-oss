//go:build integration

// Package tenantstreams_test checks the per-tenant JetStream layout
// against a live stack: registration gives a tenant its own stream, which the
// services' aggregate sources, and a tenant client reaches that stream only:
// not the rest of the JetStream API, not the server-internal subjects, and not
// another stream through the reply subject of its requests.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestTenantStreams ./tests/tenant-streams/
package tenantstreams_test

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// StreamsSuite registers two fresh tenants and a reader PAT in the first.
type StreamsSuite struct {
	tests.Base

	own, other *gateway.RegisteredTenant
	pat        string

	service *nats.Conn
	js      jetstream.JetStream
}

// TestTenantStreams is the runner.
func TestTenantStreams(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(StreamsSuite))
}

func (s *StreamsSuite) SetupSuite() {
	s.Base.SetupSuite()

	var err error

	s.own, err = gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	s.Require().NoError(err, "register own tenant")
	s.DeferSuiteTenantCleanup(s.own.ID)

	s.other, err = gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	s.Require().NoError(err, "register other tenant")
	s.DeferSuiteTenantCleanup(s.other.ID)

	provisioned, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, s.own, []string{"reader"})
	s.Require().NoError(err, "provision reader")
	s.pat = provisioned.PAT

	s.service, err = nats.Connect(s.Cfg.NatsURL)
	s.Require().NoError(err, "connect as service")
	s.T().Cleanup(s.service.Close)

	s.js, err = jetstream.New(s.service)
	s.Require().NoError(err)
}
