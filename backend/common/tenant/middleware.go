package tenant

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	TenantIDHeader    = "X-Pyck-Tenant-Id"
	AllTenantIDsValue = "all"
)

func HTTPMiddleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return &Middleware{next: next}
	}
}

type Middleware struct {
	next http.Handler
}

var _ http.Handler = (*Middleware)(nil)

func (mw *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := authn.ForContext(ctx)

	tenantIDs, err := ParseHeaders(ctx, r.Header)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	for _, tenantID := range tenantIDs {
		if !user.HasRole(authn.ROLE_READER, tenantID) {
			err = fmt.Errorf("%w %q", ErrNoAccessToTenantID, tenantID)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	ctx = Context(r.Context(), tenantIDs...)

	mw.next.ServeHTTP(w, r.WithContext(ctx))
}

func ParseHeaders(ctx context.Context, header http.Header) ([]uuid.UUID, error) {
	user := authn.ForContext(ctx)

	seenTenantIDs := make(map[uuid.UUID]struct{})
	tenantIDHeaders := header.Values(TenantIDHeader)
	addAllTenantIDs := false

	for _, tenantIDHeaderValue := range tenantIDHeaders {
		for _, tenantIDStr := range strings.Split(tenantIDHeaderValue, ",") {
			tenantIDStr = strings.TrimSpace(tenantIDStr)

			if tenantIDStr == "" {
				continue
			}

			// Handle the special "all" tenant ID header value
			if tenantIDStr == AllTenantIDsValue {
				if addAllTenantIDs {
					// Skip duplicate "all" keywords
					continue
				}

				if !user.IsAuthenticated() {
					return nil, fmt.Errorf("%w %q", ErrNoUser, tenantIDStr)
				}

				addAllTenantIDs = true
				// Continue processing to allow mixed "all" + specific tenant
				// IDs. "all" grants access to all user's accessible tenants.
				// Specific IDs enforce mandatory access validation. Combined
				// usage ensures specific tenants are verified even when
				// expanding to all tenants.
				continue
			}

			// Parse the specific tenant ID.
			tenantID, err := uuid.Parse(tenantIDStr)
			if err != nil {
				return nil, fmt.Errorf("%w %q", ErrInvalidTenantID, tenantIDStr)
			}

			seenTenantIDs[tenantID] = struct{}{}
		}
	}

	// If no tenant IDs were specified, default to "all".
	if len(seenTenantIDs) == 0 {
		log.ForContext(ctx).Debug().
			Msg("no tenant IDs specified in request, defaulting to 'all'")
		addAllTenantIDs = true
	}

	// If "all" was specified, add all tenant IDs the user has access to.
	if addAllTenantIDs && user.IsAuthenticated() {
		for tenantID := range user.Roles {
			seenTenantIDs[tenantID] = struct{}{}
		}
	}

	// Convert the map keys to a slice.
	tenantIDs := make([]uuid.UUID, 0, len(seenTenantIDs))

	for tenantID := range seenTenantIDs {
		tenantIDs = append(tenantIDs, tenantID)
	}

	log.ForContext(ctx).Debug().
		Any("tenant-ids", tenantIDs).
		Msg("parsed tenant IDs from request")

	return tenantIDs, nil
}
