//go:build integration

// Package customeventforgery_test checks that a tenant cannot use
// sendCustomEvent to forge system events for another tenant's data.
//
// sendCustomEvent writes an outbox row whose schema, entity id and operation
// come from the caller. After the signal router replies, the outbox handler
// re-publishes it on the ordinary pyck.<tenant>.crud.management.<type>.<id>.<op>
// subject. Every service's datatype cache consumes
// pyck.*.crud.management.datatype.* for all tenants and keys its slots by the
// event's id alone, so a forged "datatype" event with another tenant's id
// rewrites that tenant's cache slot in every service.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestCustomEventForgery ./tests/custom-event-forgery/
package customeventforgery_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// CustomEventSuite embeds tests.Base for ctx, config, the Zitadel connection
// and fresh-tenant cleanup.
type CustomEventSuite struct {
	tests.Base
}

// TestCustomEventForgery is the runner.
func TestCustomEventForgery(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(CustomEventSuite))
}
