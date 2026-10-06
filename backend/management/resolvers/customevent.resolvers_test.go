package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var sendCustomEvent = resolver.ParseTemplate(`mutation {
	sendCustomEvent(input: {
		type: "TestEvent",
		operation: "logout",
		payload: {
			id: "{{.ID}}",
			data: { name: "custom" }
		}
	}) {
		success
		eventCount
	}
}`)

var sendTypedCustomEvent = resolver.ParseTemplate(`mutation {
	sendCustomEvent(input: {
		type: "{{.Type}}",
		operation: "{{.Operation}}",
		payload: { id: "{{.ID}}" }
	}) {
		success
	}
}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type sendCustomEventData struct {
	SendCustomEvent struct {
		Success    bool
		EventCount int
	}
}

// =============================================================================
// SEND CUSTOM EVENT TESTS
// =============================================================================

func TestSendCustomEvent(t *testing.T) {
	t.Parallel()

	t.Run("sends custom event successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		eventID := uuid.New()
		data := execOK[sendCustomEventData](te, ctx, sendCustomEvent, map[string]any{
			"ID": eventID.String(),
		})

		assert.True(t, data.SendCustomEvent.Success)
		// The resolver writes its outbox row directly (no ent hook), so it
		// tallies the row itself.
		assert.Equal(t, 1, data.SendCustomEvent.EventCount)

		// Custom event should be inserted into the outbox for the handler to publish
		te.assertEvents(ctx, Event{"testevent", eventID, "logout"})
	})

	t.Run("payload event_id is the outbox row ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		entityID := uuid.New()
		execOK[sendCustomEventData](te, ctx, sendCustomEvent, map[string]any{
			"ID": entityID.String(),
		})

		entries, err := te.Ent.EntityEventsOutbox.Query().All(ctx)
		require.NoError(t, err)
		require.Len(t, entries, 1)

		assert.Equal(t, entries[0].ID.String(), entries[0].Payload["event_id"],
			"event_id must equal the outbox row ID")
		assert.Equal(t, entityID.String(), entries[0].Payload["id"], "id stays the entity ID")
	})
}

// TestSendCustomEventRefusesEntityTypes pins that a custom event cannot use
// the type of a real management entity. The event is re-published on
// pyck.<tenant>.crud.management.<type>.<id>.<op>, the subject the entity's
// own CRUD events use; every service's datatype cache consumes those for all
// tenants and keys its slots by id, so a forged "datatype" event with another
// tenant's id removed that datatype from every service's cache.
func TestSendCustomEventRefusesEntityTypes(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })
	ctx := te.ctx(userA)

	// Every spelling that normalises to an entity's subject segment.
	for _, typ := range []string{"datatype", "DataType", " DATATYPE ", "tenant", "user", "DeviceUser", "devicelocation", "keyvalue", "location", "device"} {
		for _, op := range []string{"create", "update", "delete"} {
			te.clearEvents(ctx)
			execErr(te, ctx, sendTypedCustomEvent, map[string]any{"Type": typ, "Operation": op, "ID": uuid.New()}, "reserved")
			te.assertNoEvents(ctx)
		}
	}

	// Positive control: custom types of their own still work, including one
	// that only differs from an entity by a separator.
	for _, typ := range []string{"replenishment", "tour-creation", "data-type"} {
		te.clearEvents(ctx)
		data := execOK[sendCustomEventData](te, ctx, sendTypedCustomEvent, map[string]any{"Type": typ, "Operation": "create", "ID": uuid.New()})
		assert.True(t, data.SendCustomEvent.Success, typ)
	}
}

// TestSendCustomEventRefusesBlankTokens pins that a type or operation must
// leave a subject segment of its own. "" becomes the wildcard "*" and
// whitespace-only input an empty segment, a subject NATS refuses, so the
// outbox would retry the event until it is dead-lettered.
func TestSendCustomEventRefusesBlankTokens(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })
	ctx := te.ctx(userA)

	for _, blank := range []string{"", " ", "  \t"} {
		te.clearEvents(ctx)
		execErr(te, ctx, sendTypedCustomEvent, map[string]any{"Type": blank, "Operation": "create", "ID": uuid.New()}, "blank")
		te.assertNoEvents(ctx)

		te.clearEvents(ctx)
		execErr(te, ctx, sendTypedCustomEvent, map[string]any{"Type": "replenishment", "Operation": blank, "ID": uuid.New()}, "blank")
		te.assertNoEvents(ctx)
	}
}

// TestSendCustomEvent_RequiresWriter pins that the outbox insert, which no ent
// privacy policy guards, is refused below WRITER on the mutation tenant and
// leaves no outbox row behind.
func TestSendCustomEvent_RequiresWriter(t *testing.T) {
	t.Parallel()

	userAReader := &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000c002"),
		TenantID: resolver.TenantA,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantA: authn.ROLE_READER},
	}
	userReaderAWriterB := &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000c003"),
		TenantID: resolver.TenantA,
		Roles: map[uuid.UUID]authn.Role{
			resolver.TenantA: authn.ROLE_READER,
			resolver.TenantB: authn.ROLE_WRITER,
		},
	}

	// A caller with a tenant header holds READER on it: without it
	// tenant.HTTPMiddleware answers 400 before the resolver runs. The
	// header-less anonymous caller (uuid.Nil tenant) reaches the resolver and
	// must be refused as unauthenticated before the mutation tenant is read.
	refused := []struct {
		name    string
		user    *authn.User
		tenant  uuid.UUID
		wantErr string
	}{
		{"reader", userAReader, resolver.TenantA, "unauthorized: writer role required for sendCustomEvent"},
		{"reader of the other tenant", userBReader, resolver.TenantB, "unauthorized: writer role required for sendCustomEvent"},
		{"writer of another tenant only", userReaderAWriterB, resolver.TenantA, "unauthorized: writer role required for sendCustomEvent"},
		{"unauthenticated caller", &authn.User{}, uuid.Nil, "unauthorized: authentication required for sendCustomEvent"},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			execErr(te, te.ctxForTenant(tc.user, tc.tenant), sendCustomEvent, map[string]any{
				"ID": uuid.New().String(),
			}, tc.wantErr)

			te.assertNoEvents(te.ctx(systemUser))
		})
	}

	allowed := []struct {
		name   string
		user   *authn.User
		tenant uuid.UUID
	}{
		{"writer", userAWriter, resolver.TenantA},
		{"admin", userA, resolver.TenantA},
		{"system user", systemUser, resolver.TenantA},
	}
	for _, tc := range allowed {
		t.Run("allows "+tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			eventID := uuid.New()
			data := execOK[sendCustomEventData](te, te.ctxForTenant(tc.user, tc.tenant), sendCustomEvent, map[string]any{
				"ID": eventID.String(),
			})
			assert.True(t, data.SendCustomEvent.Success)

			te.assertEvents(te.ctx(systemUser), Event{"testevent", eventID, "logout"})
		})
	}
}

// TestSendCustomEvent_WriterLattice walks every role pair a caller can hold on
// the mutation tenant (A) and on one other tenant (B), with the caller's home
// tenant set to B. It pins two invariants the hand-picked table above cannot:
// accepted exactly when the role on A is at least WRITER, whatever the caller
// holds elsewhere; and an accepted event is written to A, the tenant the role
// was checked on, never to the caller's home tenant. A resolver that checked A
// but stamped the event with User().TenantID would publish into B, a tenant
// where the caller may be only a READER. The role on A starts at READER
// because tenant.HTTPMiddleware answers 400 before the resolver runs for a
// header tenant the caller has no role in.
func TestSendCustomEvent_WriterLattice(t *testing.T) {
	t.Parallel()

	onTarget := []authn.Role{authn.ROLE_READER, authn.ROLE_WRITER, authn.ROLE_ADMIN}
	onOther := []authn.Role{authn.ROLE_NONE, authn.ROLE_READER, authn.ROLE_WRITER, authn.ROLE_ADMIN}

	for _, target := range onTarget {
		for _, other := range onOther {
			t.Run(fmt.Sprintf("%s on mutation tenant, %s elsewhere", target, other), func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)

				roles := map[uuid.UUID]authn.Role{resolver.TenantA: target}
				if other != authn.ROLE_NONE {
					roles[resolver.TenantB] = other
				}
				user := &authn.User{ID: uuid.New(), TenantID: resolver.TenantB, Roles: roles}
				args := map[string]any{"ID": uuid.New().String()}
				ctx := te.ctxForTenant(user, resolver.TenantA)

				if target < authn.ROLE_WRITER {
					execErr(te, ctx, sendCustomEvent, args, "unauthorized: writer role required for sendCustomEvent")
					te.assertNoEvents(te.ctx(systemUser))
					return
				}

				data := execOK[sendCustomEventData](te, ctx, sendCustomEvent, args)
				assert.True(t, data.SendCustomEvent.Success)
				assertOnlyEventIn(te, resolver.TenantA, user.ID)
			})
		}
	}
}

// TestSendCustomEvent_NoTenantHeader pins the WRITER check for a request
// without a tenant header, which defaults to every tenant the caller holds a
// role in: a single-tenant writer resolves to its one tenant and is allowed,
// and a single-tenant reader is refused before anything is written.
func TestSendCustomEvent_NoTenantHeader(t *testing.T) {
	t.Parallel()

	readerA := &authn.User{
		ID:       uuid.New(),
		TenantID: resolver.TenantA,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantA: authn.ROLE_READER},
	}

	t.Run("refuses single-tenant reader naming none", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := request.Context(t.Context(), readerA)
		execErr(te, ctx, sendCustomEvent, map[string]any{"ID": uuid.New().String()},
			"unauthorized: writer role required for sendCustomEvent")
		te.assertNoEvents(te.ctx(systemUser))
	})

	t.Run("allows single-tenant writer naming none", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := request.Context(t.Context(), userAWriter)
		data := execOK[sendCustomEventData](te, ctx, sendCustomEvent, map[string]any{"ID": uuid.New().String()})
		assert.True(t, data.SendCustomEvent.Success)
		assertOnlyEventIn(te, resolver.TenantA, userAWriter.ID)
	})
}

// assertOnlyEventIn asserts the outbox holds exactly one event, stamped with
// tenantID on the row, in the NATS subject, in the payload's tenant_id and in
// its pyck_tenant_id search attribute, and attributed to userID. The payload
// fields matter on their own: the workflow signal router picks the tenant
// whose workflows it starts or signals from tenant_id alone, and copies
// pyck_tenant_id onto the started workflow, which is what the workflow
// listings filter on. A payload naming another tenant would start that
// tenant's workflows under a role checked on this one.
func assertOnlyEventIn(te *testEnv, tenantID, userID uuid.UUID) {
	te.t.Helper()

	entries, err := te.Ent.EntityEventsOutbox.Query().All(te.ctx(systemUser))
	require.NoError(te.t, err)
	require.Len(te.t, entries, 1, "exactly one outbox row")
	assert.Equal(te.t, tenantID, entries[0].TenantID, "outbox row tenant")
	assert.Contains(te.t, entries[0].Topic, "."+tenantID.String()+".crud.", "NATS subject tenant")
	assert.Equal(te.t, tenantID.String(), fmt.Sprint(entries[0].Payload["tenant_id"]),
		"payload tenant_id (the tenant the signal router starts workflows in)")
	attrs, ok := entries[0].Payload["wf_search_attributes"].(map[string]any)
	if assert.True(te.t, ok, "payload wf_search_attributes is an object, got %T", entries[0].Payload["wf_search_attributes"]) {
		assert.Equal(te.t, tenantID.String(), attrs["pyck_tenant_id"], "search attribute tenant")
	}
	if assert.NotNil(te.t, entries[0].UserID, "outbox row user") {
		assert.Equal(te.t, userID, *entries[0].UserID, "outbox row user")
	}
}
