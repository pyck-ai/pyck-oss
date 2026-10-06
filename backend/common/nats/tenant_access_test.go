package nats_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	natsgo "github.com/pyck-ai/pyck/backend/common/nats"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

func newUser(roles map[uuid.UUID]authn.Role) authn.User {
	return authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Roles:    roles,
	}
}

func tenantHeader(values ...string) http.Header {
	h := http.Header{}
	for _, v := range values {
		h.Add(tenant.TenantIDHeader, v)
	}

	return h
}

func TestResolveTenant(t *testing.T) {
	t.Parallel()

	own := uuid.New()
	other := uuid.New()
	third := uuid.New()

	t.Run("member of the named tenant is allowed", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER})

		got, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader(own.String()))

		require.NoError(t, err)
		assert.Equal(t, own, got)
	})

	t.Run("non-member of the named tenant is rejected", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_ADMIN})

		_, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader(other.String()))

		require.ErrorIs(t, err, tenant.ErrNoAccessToTenantID)
	})

	t.Run("role none in the named tenant is rejected", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_NONE})

		_, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader(own.String()))

		require.ErrorIs(t, err, tenant.ErrNoAccessToTenantID)
	})

	t.Run("no tenant header with a single membership resolves to it", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER})

		got, err := natsgo.ResolveTenant(context.Background(), user, http.Header{})

		require.NoError(t, err)
		assert.Equal(t, own, got)
	})

	t.Run("no tenant header without any membership is rejected", func(t *testing.T) {
		t.Parallel()

		user := newUser(nil)

		_, err := natsgo.ResolveTenant(context.Background(), user, http.Header{})

		require.ErrorIs(t, err, natsgo.ErrTenantNotSingle)
	})

	t.Run("all with several memberships is rejected without panic", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER, other: authn.ROLE_READER})

		assert.NotPanics(t, func() {
			_, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader(tenant.AllTenantIDsValue))
			require.ErrorIs(t, err, natsgo.ErrTenantNotSingle)
		})
	})

	t.Run("no header with several memberships is rejected without panic", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER, other: authn.ROLE_READER})

		assert.NotPanics(t, func() {
			_, err := natsgo.ResolveTenant(context.Background(), user, http.Header{})
			require.ErrorIs(t, err, natsgo.ErrTenantNotSingle)
		})
	})

	t.Run("two named tenants are rejected without panic", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER, other: authn.ROLE_READER})

		assert.NotPanics(t, func() {
			_, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader(own.String()+","+other.String()))
			require.ErrorIs(t, err, natsgo.ErrTenantNotSingle)
		})
	})

	t.Run("a named tenant plus all is rejected when it expands to several", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER, third: authn.ROLE_READER})

		_, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader(own.String(), tenant.AllTenantIDsValue))

		require.ErrorIs(t, err, natsgo.ErrTenantNotSingle)
	})

	t.Run("invalid tenant id is rejected", func(t *testing.T) {
		t.Parallel()

		user := newUser(map[uuid.UUID]authn.Role{own: authn.ROLE_READER})

		_, err := natsgo.ResolveTenant(context.Background(), user, tenantHeader("not-a-uuid"))

		require.ErrorIs(t, err, tenant.ErrInvalidTenantID)
	})

	t.Run("unauthenticated user is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := natsgo.ResolveTenant(context.Background(), authn.User{}, tenantHeader(own.String()))

		require.ErrorIs(t, err, tenant.ErrNoAccessToTenantID)
	})

	t.Run("system user may name any tenant", func(t *testing.T) {
		t.Parallel()

		got, err := natsgo.ResolveTenant(context.Background(), *authn.SystemUser(), tenantHeader(other.String()))

		require.NoError(t, err)
		assert.Equal(t, other, got)
	})

	t.Run("system user without a tenant header is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := natsgo.ResolveTenant(context.Background(), *authn.SystemUser(), http.Header{})

		require.ErrorIs(t, err, natsgo.ErrTenantNotSingle)
	})
}

// subjectMatches reports whether a NATS subject matches a pattern using the
// "*" (one token) and ">" (one or more trailing tokens) wildcards.
func subjectMatches(pattern, subject string) bool {
	p := strings.Split(pattern, ".")
	s := strings.Split(subject, ".")

	for i, tok := range p {
		if tok == ">" {
			return len(s) > i
		}

		if i >= len(s) || (tok != "*" && tok != s[i]) {
			return false
		}
	}

	return len(p) == len(s)
}

func TestStateChangeDenyPattern(t *testing.T) {
	t.Parallel()

	const stream = "pyck"

	pattern := natsgo.StateChangeDenyPattern(stream)
	tenantID := uuid.New()

	stateChange := events.TemporalWorkflowStateChangeTopic{
		StreamName:       stream,
		Namespace:        tenantID.String(),
		TaskQueue:        "queue",
		WorkflowTypeName: "type",
		WorkflowID:       "wf",
		RunID:            "run",
		Status:           "WORKFLOW_EXECUTION_STATUS_RUNNING",
	}

	t.Run("covers the state-change subject the temporal server publishes", func(t *testing.T) {
		t.Parallel()

		assert.True(t, subjectMatches(pattern, stateChange.String()), stateChange.String())
	})

	t.Run("covers the router's filter subject", func(t *testing.T) {
		t.Parallel()

		filter := events.TemporalWorkflowStateChangeTopic{StreamName: stream}.String()
		assert.True(t, subjectMatches(pattern, filter), filter)
	})

	t.Run("is inside what a tenant could otherwise publish", func(t *testing.T) {
		t.Parallel()

		allowed := stream + "." + tenantID.String() + ".>"
		assert.True(t, subjectMatches(allowed, stateChange.String()))
	})

	t.Run("does not cover tenant workflow events", func(t *testing.T) {
		t.Parallel()

		wf := events.WorkflowEventTopic{StreamName: stream, TenantID: tenantID, WorkflowID: uuid.New(), WorkflowName: "w"}
		assert.False(t, subjectMatches(pattern, wf.String()), wf.String())
	})
}
