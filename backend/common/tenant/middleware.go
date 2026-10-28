package tenant

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	TenantIDHeader    = "X-Pyck-Tenant-Id"
	AllTenantIDsValue = "all"
)

type (
	// Middleware resolves the X-Pyck-Tenant-Id header (explicit IDs, the "all"
	// keyword, or nothing — which means "all") to the tenants the request acts
	// on and authorizes each one on two axes before exposing the set through
	// [Context]:
	//
	//   - role: the caller must hold at least ROLE_READER on EVERY tenant in
	//     the acting set — explicitly named and "all"-expanded alike — else
	//     the whole request is denied with 400 (the pre-existing
	//     all-or-nothing rule: the expansion is not filtered down to the
	//     accessible subset);
	//   - liveness: a tenant no live row backs (in management: soft-deleted,
	//     a registration that did not finish, or no row at all; verdict under
	//     liveness.TenantKey) is never acted on. Explicitly named
	//     disabled tenant → the whole request is denied with 403 (fail-closed:
	//     the caller asked for it by name and silently narrowing the scope
	//     would hide the denial). Disabled tenants reached only through the
	//     "all" expansion → filtered out of the acting set and the request
	//     proceeds on the rest ("all" means "everything I may see", and it is
	//     the default when the header is absent, so denying would lock a
	//     multi-tenant user out of every tenant the moment one is disabled).
	//     If the filter would leave nothing of a non-empty expansion — every
	//     tenant the caller may see is disabled — the request is denied with
	//     403 as well: proceeding with an empty acting set would surface
	//     downstream as a misleading "missing role" or a mutation without a
	//     target, hiding the real cause. The caller's own home tenant is
	//     always part of the check, even when the request does not act on
	//     it — a disabled tenant's users lose access everywhere at the row
	//     flip. Filtering settles the mutation target too: the acting set
	//     it produces is the one request.MutationTenantID counts, so a
	//     caller left with a single live tenant mutates it implicitly where
	//     the unfiltered set had no unambiguous target. Intended — a
	//     request may only act on tenants in its acting set — and recorded
	//     in docs/tenant-access-control.md.
	//
	// The liveness check runs AFTER the role check, so a caller without a
	// grant on a tenant learns nothing about that tenant's state, and is
	// skipped for system users — they are how tenants are disabled and
	// restored, and their identity is validated by the system-role check at
	// introspection.
	//
	// If the source fails (management unreachable, decode fault) cached
	// verdicts still hold (a tenant this replica already knows is live
	// stays served and one it knows is disabled stays denied) but tenants
	// without a cached verdict are DENIED: a tenant nobody can verify is
	// not served, because admitting it would re-open a deleted tenant for
	// the length of the outage. The failure is reported once per throttle
	// window; nothing is cached from it, so service resumes with the first
	// successful lookup. The auth path's home-tenant check applies the same
	// rule.
	Middleware struct {
		next http.Handler
		// livenessCache holds the tenant verdicts under liveness.TenantKey;
		// shared with the auth provider and the tenant-events subscriber.
		livenessCache *liveness.Cache
		// source answers cache misses (management's tenant table, directly
		// or through the gateway).
		source liveness.TenantSource
		// faultLog keeps a failing source from writing one Error line per
		// request. Entries expire on a clock, so once the source is down
		// every request misses the cache and reports the same fault.
		faultLog *log.FaultThrottle
	}

	// selection is the parsed X-Pyck-Tenant-Id header, before role and
	// liveness checks. Explicit and "all"-expanded tenants are kept apart
	// because [Middleware] treats them differently.
	selection struct {
		// Explicit holds the tenant IDs named in the header, deduplicated, in
		// first-seen order.
		Explicit []uuid.UUID
		// All is set when the header carries the "all" keyword or names no
		// tenant at all (the default): the acting set additionally includes
		// every tenant the caller holds a role in.
		All bool
	}
)

var _ http.Handler = (*Middleware)(nil)

// HTTPMiddleware resolves X-Pyck-Tenant-Id to the request's acting tenant
// set and authorizes it — see [Middleware] for the rules. cache and source
// back the liveness axis; a nil one is returned as an error so the wiring
// mistake fails at boot, not at the first request.
func HTTPMiddleware(cache *liveness.Cache, source liveness.TenantSource) (func(next http.Handler) http.Handler, error) {
	if cache == nil || source == nil {
		return nil, fmt.Errorf("tenant middleware: %w", liveness.ErrMissingDeps)
	}
	// One throttle for every handler this middleware wraps: the fault it
	// reports is the shared source being down, not a property of the route.
	faultLog := log.NewFaultThrottle(log.FaultLogWindow)
	return func(next http.Handler) http.Handler {
		return &Middleware{next: next, livenessCache: cache, source: source, faultLog: faultLog}
	}, nil
}

func (mw *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := authn.ForContext(ctx)

	sel, err := parseSelection(ctx, r.Header)
	if err != nil {
		httputil.JSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	acting := sel.actingTenants(user)
	for _, tenantID := range acting {
		if !user.HasRole(authn.ROLE_READER, tenantID) {
			err = fmt.Errorf("%w %q", ErrNoAccessToTenantID, tenantID)
			httputil.JSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	tenantIDs, err := mw.authorizeLiveTenants(ctx, user, sel, acting)
	if err != nil {
		httputil.JSONError(w, err.Error(), http.StatusForbidden)
		return
	}

	log.ForContext(ctx).Debug().
		Any("tenant-ids", tenantIDs).
		Msg("resolved acting tenant IDs from request")

	ctx = Context(ctx, tenantIDs...)

	mw.next.ServeHTTP(w, r.WithContext(ctx))
}

// authorizeLiveTenants applies the liveness rule of [Middleware] to the selection's
// acting set: an error means the request must be denied (the caller's home
// tenant or an explicitly named tenant is disabled, or every tenant of the
// "all" expansion is), otherwise the returned set is the acting set minus
// disabled tenants reached through "all".
func (mw *Middleware) authorizeLiveTenants(ctx context.Context, user authn.User, sel selection, acting []uuid.UUID) ([]uuid.UUID, error) {
	if user.IsSystemUser() || !user.IsAuthenticated() {
		return acting, nil
	}

	// The caller's home tenant is checked even when the request does not
	// act on it: a disabled tenant's users lose access everywhere at the
	// row flip, not only once Zitadel deactivates their org.
	toCheck := acting
	if !slices.Contains(acting, user.TenantID) {
		toCheck = append(slices.Clone(acting), user.TenantID)
	}
	live, err := mw.livenessCache.LookupTenants(ctx, toCheck, mw.source)
	if err != nil {
		// Cached verdicts were applied; tenants with no verdict are denied
		// below: a tenant that cannot be verified is not served.
		mw.faultLog.Err(log.ForContext(ctx), err).
			Msg("disabled-tenant lookup failed; denying uncached tenants")
	}
	if !live[user.TenantID] {
		return nil, fmt.Errorf("%w: caller's home tenant %q", ErrTenantDisabled, user.TenantID)
	}
	for _, id := range sel.Explicit {
		if !live[id] {
			return nil, fmt.Errorf("%w %q", ErrTenantDisabled, id)
		}
	}

	kept := make([]uuid.UUID, 0, len(acting))
	var dropped []uuid.UUID
	for _, id := range acting {
		if live[id] {
			kept = append(kept, id)
		} else {
			dropped = append(dropped, id)
		}
	}
	if len(dropped) == 0 {
		return kept, nil
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("%w: every tenant the caller may see is disabled", ErrTenantDisabled)
	}
	log.ForContext(ctx).Debug().
		Any("disabled-tenant-ids", dropped).
		Msg("dropped disabled tenants from the 'all' expansion")
	return kept, nil
}

// actingTenants returns the acting set for user: Explicit plus, when All,
// every tenant the user holds a role in (nothing for an unauthenticated
// user). The expansion is deliberately unfiltered — the role axis in
// [Middleware.ServeHTTP] checks the whole acting set with all-or-nothing
// semantics, exactly as it did before the liveness axis existed.
// Deduplicated; explicit IDs first, in header order.
func (s selection) actingTenants(user authn.User) []uuid.UUID {
	tenantIDs := append([]uuid.UUID(nil), s.Explicit...)
	if !s.All || !user.IsAuthenticated() {
		return tenantIDs
	}
	seen := make(map[uuid.UUID]struct{}, len(s.Explicit))
	for _, id := range s.Explicit {
		seen[id] = struct{}{}
	}
	for tenantID := range user.Roles {
		if _, dup := seen[tenantID]; !dup {
			tenantIDs = append(tenantIDs, tenantID)
		}
	}
	return tenantIDs
}

// ParseHeaders resolves the X-Pyck-Tenant-Id header to the acting tenant
// set of the user in ctx: parseSelection followed by selection.actingTenants.
// It applies neither the role nor the liveness rule — that is
// [Middleware]'s job; ParseHeaders is for callers that only need the
// resolved set (the NATS auth handler).
func ParseHeaders(ctx context.Context, header http.Header) ([]uuid.UUID, error) {
	sel, err := parseSelection(ctx, header)
	if err != nil {
		return nil, err
	}
	tenantIDs := sel.actingTenants(authn.ForContext(ctx))

	log.ForContext(ctx).Debug().
		Any("tenant-ids", tenantIDs).
		Msg("parsed tenant IDs from request")

	return tenantIDs, nil
}

// parseSelection parses the X-Pyck-Tenant-Id header values (repeated
// headers and comma-separated lists are both accepted) into a selection.
// The "all" keyword requires an authenticated user in ctx (ErrNoUser
// otherwise); a value that is neither "all" nor a UUID is
// ErrInvalidTenantID. No tenant named at all means "all".
func parseSelection(ctx context.Context, header http.Header) (selection, error) {
	user := authn.ForContext(ctx)

	var sel selection
	seen := make(map[uuid.UUID]struct{})

	for _, headerValue := range header.Values(TenantIDHeader) {
		for _, tenantIDStr := range strings.Split(headerValue, ",") {
			tenantIDStr = strings.TrimSpace(tenantIDStr)

			if tenantIDStr == "" {
				continue
			}

			// "all" may be combined with specific IDs: "all" grants access
			// to every tenant the user may see, the specific IDs are
			// additionally validated as explicitly requested.
			if tenantIDStr == AllTenantIDsValue {
				if !user.IsAuthenticated() {
					return selection{}, fmt.Errorf("%w %q", ErrNoUser, tenantIDStr)
				}
				sel.All = true
				continue
			}

			tenantID, err := uuid.Parse(tenantIDStr)
			if err != nil {
				return selection{}, fmt.Errorf("%w %q", ErrInvalidTenantID, tenantIDStr)
			}
			if _, dup := seen[tenantID]; dup {
				continue
			}
			seen[tenantID] = struct{}{}
			sel.Explicit = append(sel.Explicit, tenantID)
		}
	}

	if len(sel.Explicit) == 0 && !sel.All {
		log.ForContext(ctx).Debug().
			Msg("no tenant IDs specified in request, defaulting to 'all'")
		sel.All = true
	}

	return sel, nil
}
