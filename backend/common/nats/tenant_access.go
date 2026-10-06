package nats

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// errTenantNotSingle is returned when the connect headers do not resolve to
// exactly one tenant. Permissions are scoped to a single tenant's subjects,
// so a connection cannot span several.
var errTenantNotSingle = errors.New("exactly one tenant required")

// resolveTenant decides which tenant a NATS client may be scoped to.
//
// It mirrors tenant.HTTPMiddleware: the tenant headers are parsed, and the
// user must hold at least ROLE_READER in every tenant they resolve to. On top
// of that the result must be exactly one tenant. The special "all" value (or
// no tenant header at all) expands to every tenant the user holds a role in,
// so it is accepted only when that expansion yields a single tenant; with
// several tenants it is rejected here instead of panicking later in
// request.MutationTenantID.
//
// Service clients do not reach this function: their credentials are listed
// in the server's auth_callout auth_users and bypass the callout.
func resolveTenant(ctx context.Context, user authn.User, header http.Header) (uuid.UUID, error) {
	userCtx := authn.Context(ctx, &user)

	tenantIDs, err := tenant.ParseHeaders(userCtx, header)
	if err != nil {
		return uuid.Nil, err
	}

	if len(tenantIDs) != 1 {
		return uuid.Nil, fmt.Errorf("%w, got %d", errTenantNotSingle, len(tenantIDs))
	}

	tenantID := tenantIDs[0]
	if !user.HasRole(authn.ROLE_READER, tenantID) {
		return uuid.Nil, fmt.Errorf("%w %q", tenant.ErrNoAccessToTenantID, tenantID)
	}

	return tenantID, nil
}

// stateChangeDenyPattern returns the subject pattern covering every Temporal
// workflow state-change subject of the stream, whatever namespace it names
// and however many tokens follow "temporal". The format is
// events.TemporalWorkflowStateChangeTopic.
func stateChangeDenyPattern(streamName string) string {
	return streamName + ".*.temporal.>"
}
