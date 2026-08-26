package gate_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/gate"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

func TestHTTPMiddleware(t *testing.T) {
	t.Parallel()

	tenantA := uuid.New()
	tenantB := uuid.New()

	withRole := func(tenantID uuid.UUID, keys ...string) authn.User {
		set := make(map[string]struct{}, len(keys))
		for _, k := range keys {
			set[k] = struct{}{}
		}
		return authn.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			ServiceRoles: map[uuid.UUID]map[string]struct{}{tenantID: set},
		}
	}

	testCases := []struct {
		name        string
		user        *authn.User
		tenantIDs   []uuid.UUID
		wantStatus  int
		wantallowed bool
	}{
		{
			name:        "holds the service role -> allowed",
			user:        ptr(withRole(tenantA, "inventory_service")),
			tenantIDs:   []uuid.UUID{tenantA},
			wantStatus:  http.StatusOK,
			wantallowed: true,
		},
		{
			name:        "missing the service role -> 403",
			user:        ptr(withRole(tenantA, "picking_service")),
			tenantIDs:   []uuid.UUID{tenantA},
			wantStatus:  http.StatusForbidden,
			wantallowed: false,
		},
		{
			name:        "system user bypasses",
			user:        authn.SystemUser(),
			tenantIDs:   []uuid.UUID{tenantA},
			wantStatus:  http.StatusOK,
			wantallowed: true,
		},
		{
			name:        "unauthenticated falls through",
			user:        nil,
			tenantIDs:   nil,
			wantStatus:  http.StatusOK,
			wantallowed: true,
		},
		{
			name:        "multi-tenant: role in all -> allowed",
			user:        ptr(multiTenantUser(tenantA, tenantB, "inventory_service")),
			tenantIDs:   []uuid.UUID{tenantA, tenantB},
			wantStatus:  http.StatusOK,
			wantallowed: true,
		},
		{
			name:        "multi-tenant: missing in one -> 403",
			user:        ptr(withRole(tenantA, "inventory_service")),
			tenantIDs:   []uuid.UUID{tenantA, tenantB},
			wantStatus:  http.StatusForbidden,
			wantallowed: false,
		},
		{
			name:        "authenticated with empty tenant set -> 403",
			user:        ptr(withRole(tenantA, "inventory_service")),
			tenantIDs:   nil,
			wantStatus:  http.StatusForbidden,
			wantallowed: false,
		},
		{
			name:        "system user with empty tenant set -> allowed",
			user:        authn.SystemUser(),
			tenantIDs:   nil,
			wantStatus:  http.StatusOK,
			wantallowed: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			allowed := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				allowed = true
				w.WriteHeader(http.StatusOK)
			})

			handler := gate.HTTPMiddleware(serviceroles.Inventory)(next)

			ctx := context.Background()
			if tc.user != nil {
				ctx = authn.Context(ctx, tc.user)
			}
			ctx = tenant.Context(ctx, tc.tenantIDs...)
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/query", nil)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantallowed, allowed)
		})
	}
}

func multiTenantUser(a, b uuid.UUID, key string) authn.User {
	return authn.User{
		ID:       uuid.New(),
		TenantID: a,
		ServiceRoles: map[uuid.UUID]map[string]struct{}{
			a: {key: struct{}{}},
			b: {key: struct{}{}},
		},
	}
}

func ptr(u authn.User) *authn.User { return &u }
