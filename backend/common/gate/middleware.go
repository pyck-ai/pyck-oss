// Package gate provides the per-service access gate: an HTTP middleware that
// requires the authenticated user to hold a service's gate role
// (<service>_service) in the tenant(s) the request operates on.
//
// It is applied only to gated services (inventory, picking, receiving, file,
// main-data) and must run AFTER authn.HTTPMiddleware (sets the user) and
// tenant.HTTPMiddleware (resolves the operative tenants). Management and
// workflow are intentionally not gated.
//
// This is a thin, self-contained layer on top of the unchanged
// reader/writer/admin ladder — the ladder still governs what a user may do
// once past the gate. Keep it isolated so it can be removed cleanly when full
// RBAC replaces it.
package gate

import (
	"net/http"

	"github.com/pyck-ai/pyck/backend/common/authn"
	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// HTTPMiddleware enforces the per-service gate for the given service.
//
// For an authenticated, non-system user it denies the request (403) unless the
// user holds the service's gate role in every tenant the request operates on.
// System users bypass (authn.User.HasServiceRole returns true for them).
// Unauthenticated requests fall through unchanged so downstream auth handles
// them as before.
func HTTPMiddleware(role serviceroles.ServiceRole) func(http.Handler) http.Handler {
	roleKey := role.String()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			user := authn.ForContext(ctx)

			// Leave unauthenticated requests to downstream auth handling.
			if !user.IsAuthenticated() {
				next.ServeHTTP(w, r)
				return
			}

			// Fail closed: an authenticated, non-system user with no operative
			// tenant has no tenant in which to hold the gate role, so there is
			// nothing to check. Denying here keeps the gate a no-op only for
			// system users, independent of how downstream scopes reads.
			tenantIDs := tenant.ForContext(ctx)
			if len(tenantIDs) == 0 && !user.IsSystemUser() {
				httputil.JSONError(w, "access denied: missing "+roleKey+" role", http.StatusForbidden)
				return
			}

			for _, tenantID := range tenantIDs {
				if !user.HasServiceRole(roleKey, tenantID) {
					httputil.JSONError(w, "access denied: missing "+roleKey+" role", http.StatusForbidden)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
