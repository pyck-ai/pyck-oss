package idempotency

import (
	"context"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/request"
)

// DefaultAuthLookup is the [AuthLookup] every backend service uses in
// production: it pulls the authenticated user and (single) tenant out of
// the request context populated by the auth/tenant HTTP middlewares.
//
// The lookup is considered successful only when the request resolves to
// exactly one tenant and a non-nil user. Anonymous mutations and
// requests with multiple tenants are rejected by [PreCheck] as 400.
func DefaultAuthLookup(ctx context.Context) (tenantID, userID uuid.UUID, ok bool) {
	rc := request.ForContext(ctx)
	user := rc.User()
	if !user.IsAuthenticated() {
		return uuid.Nil, uuid.Nil, false
	}
	if !rc.HasMutationTenantID() {
		return uuid.Nil, uuid.Nil, false
	}
	return rc.MutationTenantID(), user.ID, true
}
