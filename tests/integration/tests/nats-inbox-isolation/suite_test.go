//go:build integration

// Package natsinboxisolation_test checks that a tenant's token connection to
// NATS cannot read the replies delivered to other connections' inboxes.
//
// Token clients connect over the websocket listener and are admitted by the
// auth callout (backend/common/nats/auth_handler.go), which places them in
// the tenant account and scopes them to one tenant. JetStream delivers what
// a pull consumer fetches, with the full payload, to the fetching
// connection's inbox. The platform services fetch into the inboxes of the
// services' account, which a tenant client cannot reach at all; every tenant
// client shares the tenant account, so one allowed to subscribe to _INBOX.>
// would receive every other tenant's fetches.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestNatsInboxIsolation ./tests/nats-inbox-isolation/
package natsinboxisolation_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// NatsInboxSuite embeds tests.Base for ctx, config, the Zitadel connection
// and fresh-tenant cleanup.
type NatsInboxSuite struct {
	tests.Base
}

// TestNatsInboxIsolation is the runner.
func TestNatsInboxIsolation(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(NatsInboxSuite))
}
