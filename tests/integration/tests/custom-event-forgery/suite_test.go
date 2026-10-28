//go:build integration

// Package customeventforgery_test checks that a tenant cannot use
// sendCustomEvent to forge system events for another tenant's data.
//
// sendCustomEvent writes an outbox row whose type, entity id and operation come
// from the caller. Every service's datatype cache consumes
// pyck.*.crud.management.datatype.* for all tenants and keys its slots by the
// event's id alone, so a custom "datatype" event published there with another
// tenant's id would rewrite that tenant's cache slot in every service. Custom
// events are published on pyck.<tenant>.custom.<type>.<id>.<op> instead, so
// such an event is accepted and reaches no cache.
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
