package tenant_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPMiddleware(t *testing.T) {
	// Create a test handler that captures the context
	var capturedTenantIDs []uuid.UUID
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTenantIDs = tenant.ForContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	middleware := tenant.HTTPMiddleware()(testHandler)

	t.Run("no tenant headers with unauthenticated user", func(t *testing.T) {
		capturedTenantIDs = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		assert.Empty(t, capturedTenantIDs)
	})

	t.Run("single tenant header with authorized user", func(t *testing.T) {
		capturedTenantIDs = nil
		tenantID := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_READER,
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tenant.TenantIDHeader, tenantID.String())
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedTenantIDs, 1)
		assert.Equal(t, tenantID, capturedTenantIDs[0])
	})

	t.Run("user with no roles at all in requested tenant", func(t *testing.T) {
		capturedTenantIDs = nil
		authorizedTenantID := uuid.New()
		unauthorizedTenantID := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: authorizedTenantID,
			Roles: map[uuid.UUID]authn.Role{
				authorizedTenantID: authn.ROLE_READER,
				// User has no entry for unauthorizedTenantID
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tenant.TenantIDHeader, unauthorizedTenantID.String())
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		// Now requires ROLE_READER, so access is denied for tenants where user has no role
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "no access to tenant ID")
		assert.Contains(t, rec.Body.String(), unauthorizedTenantID.String())
	})

	t.Run("user with ROLE_NONE in requested tenant", func(t *testing.T) {
		capturedTenantIDs = nil
		tenantID := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_NONE, // Explicitly set to ROLE_NONE
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tenant.TenantIDHeader, tenantID.String())
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		// ROLE_NONE is not sufficient for ROLE_READER requirement
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "no access to tenant ID")
		assert.Contains(t, rec.Body.String(), tenantID.String())
	})

	t.Run("multiple tenant headers", func(t *testing.T) {
		capturedTenantIDs = nil
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
				tenantID2: authn.ROLE_WRITER,
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(tenant.TenantIDHeader, tenantID1.String())
		req.Header.Add(tenant.TenantIDHeader, tenantID2.String())
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedTenantIDs, 2)
		assert.Contains(t, capturedTenantIDs, tenantID1)
		assert.Contains(t, capturedTenantIDs, tenantID2)
	})

	t.Run("all tenants header", func(t *testing.T) {
		capturedTenantIDs = nil
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		tenantID3 := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
				tenantID2: authn.ROLE_WRITER,
				tenantID3: authn.ROLE_ADMIN,
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tenant.TenantIDHeader, "all")
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedTenantIDs, 3)
		assert.Contains(t, capturedTenantIDs, tenantID1)
		assert.Contains(t, capturedTenantIDs, tenantID2)
		assert.Contains(t, capturedTenantIDs, tenantID3)
	})

	t.Run("invalid tenant ID", func(t *testing.T) {
		capturedTenantIDs = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tenant.TenantIDHeader, "invalid-uuid")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid tenant ID")
		assert.Contains(t, rec.Body.String(), "invalid-uuid")
	})
}

func TestParseHeaders(t *testing.T) {
	t.Run("no headers with unauthenticated user", func(t *testing.T) {
		ctx := context.Background()
		header := http.Header{}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		assert.Empty(t, tenantIDs)
	})

	t.Run("no headers with single tenant user", func(t *testing.T) {
		tenantID := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_READER,
			},
		}
		ctx := authn.Context(context.Background(), user)
		header := http.Header{}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 1)
		assert.Contains(t, tenantIDs, tenantID)
	})

	t.Run("headers with multi-tenant user", func(t *testing.T) {
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
				tenantID2: authn.ROLE_WRITER,
			},
		}
		ctx := authn.Context(context.Background(), user)
		header := http.Header{}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.NotEmpty(t, tenantIDs)
		require.Len(t, tenantIDs, 2)
		assert.Contains(t, tenantIDs, tenantID1)
		assert.Contains(t, tenantIDs, tenantID2)
	})

	t.Run("single tenant ID header", func(t *testing.T) {
		ctx := context.Background()
		tenantID := uuid.New()
		header := http.Header{
			tenant.TenantIDHeader: []string{tenantID.String()},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 1)
		assert.Contains(t, tenantIDs, tenantID)
	})

	t.Run("multiple tenant IDs in single header", func(t *testing.T) {
		ctx := context.Background()
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		header := http.Header{
			tenant.TenantIDHeader: []string{fmt.Sprintf("%s,%s", tenantID1.String(), tenantID2.String())},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 2)
		assert.Contains(t, tenantIDs, tenantID1)
		assert.Contains(t, tenantIDs, tenantID2)
	})

	t.Run("multiple headers with tenant IDs", func(t *testing.T) {
		ctx := context.Background()
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		header := http.Header{}
		header.Add(tenant.TenantIDHeader, tenantID1.String())
		header.Add(tenant.TenantIDHeader, tenantID2.String())

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 2)
		assert.Contains(t, tenantIDs, tenantID1)
		assert.Contains(t, tenantIDs, tenantID2)
	})

	t.Run("duplicate tenant IDs", func(t *testing.T) {
		ctx := context.Background()
		tenantID := uuid.New()
		header := http.Header{}
		header.Add(tenant.TenantIDHeader, tenantID.String())
		header.Add(tenant.TenantIDHeader, tenantID.String())

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID, tenantIDs[0])
	})

	t.Run("all keyword with authenticated user", func(t *testing.T) {
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
				tenantID2: authn.ROLE_WRITER,
			},
		}
		ctx := authn.Context(context.Background(), user)
		header := http.Header{
			tenant.TenantIDHeader: []string{"all"},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 2)
		assert.Contains(t, tenantIDs, tenantID1)
		assert.Contains(t, tenantIDs, tenantID2)
	})

	t.Run("all keyword with unauthenticated user", func(t *testing.T) {
		ctx := context.Background()
		header := http.Header{
			tenant.TenantIDHeader: []string{"all"},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, tenant.ErrNoUser))
		assert.Contains(t, err.Error(), "all")
		assert.Nil(t, tenantIDs)
	})

	t.Run("mix of all and specific tenant IDs", func(t *testing.T) {
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		tenantID3 := uuid.New()
		user := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
				tenantID2: authn.ROLE_WRITER,
			},
		}
		ctx := authn.Context(context.Background(), user)
		header := http.Header{
			tenant.TenantIDHeader: []string{fmt.Sprintf("all,%s", tenantID3.String())},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 3)
		assert.Contains(t, tenantIDs, tenantID1)
		assert.Contains(t, tenantIDs, tenantID2)
		assert.Contains(t, tenantIDs, tenantID3)
	})

	t.Run("invalid tenant ID format", func(t *testing.T) {
		ctx := context.Background()
		header := http.Header{
			tenant.TenantIDHeader: []string{"not-a-uuid"},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, tenant.ErrInvalidTenantID))
		assert.Contains(t, err.Error(), "not-a-uuid")
		assert.Nil(t, tenantIDs)
	})

	t.Run("whitespace handling", func(t *testing.T) {
		ctx := context.Background()
		tenantID := uuid.New()
		header := http.Header{
			tenant.TenantIDHeader: []string{fmt.Sprintf("  %s  ", tenantID.String())},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID, tenantIDs[0])
	})

	t.Run("empty strings in list", func(t *testing.T) {
		ctx := context.Background()
		tenantID := uuid.New()
		header := http.Header{
			tenant.TenantIDHeader: []string{fmt.Sprintf(",,%s,,", tenantID.String())},
		}

		tenantIDs, err := tenant.ParseHeaders(ctx, header)

		assert.NoError(t, err)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID, tenantIDs[0])
	})
}

func TestMiddleware_Integration(t *testing.T) {
	// Test the full middleware flow with a real server
	var capturedTenantIDs []uuid.UUID

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTenantIDs = tenant.ForContext(r.Context())

		// Return tenant count in response
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Tenants: %d", len(capturedTenantIDs))
	})

	middleware := tenant.HTTPMiddleware()(handler)
	server := httptest.NewServer(middleware)
	defer server.Close()

	t.Run("request with single tenant", func(t *testing.T) {
		capturedTenantIDs = nil
		tenantID := uuid.New()

		// Note: In a real scenario, the authn middleware would set the user context
		// For this test, we're testing the ParseHeaders logic primarily
		req, err := http.NewRequest(http.MethodGet, server.URL, nil)
		require.NoError(t, err)
		req.Header.Set(tenant.TenantIDHeader, tenantID.String())

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Without a user context with proper roles, this should fail
		// But it tests the parsing logic
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestTenantHeaderConstant(t *testing.T) {
	assert.Equal(t, "X-Pyck-Tenant-Id", tenant.TenantIDHeader)
}

func TestAllTenantIDsValue(t *testing.T) {
	assert.Equal(t, "all", tenant.AllTenantIDsValue)
}

func BenchmarkParseHeaders(b *testing.B) {
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID1,
		Roles: map[uuid.UUID]authn.Role{
			tenantID1: authn.ROLE_READER,
			tenantID2: authn.ROLE_WRITER,
		},
	}
	ctx := authn.Context(context.Background(), user)

	b.Run("single tenant", func(b *testing.B) {
		header := http.Header{
			tenant.TenantIDHeader: []string{tenantID1.String()},
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = tenant.ParseHeaders(ctx, header)
		}
	})

	b.Run("multiple tenants", func(b *testing.B) {
		header := http.Header{
			tenant.TenantIDHeader: []string{fmt.Sprintf("%s,%s", tenantID1.String(), tenantID2.String())},
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = tenant.ParseHeaders(ctx, header)
		}
	})

	b.Run("all keyword", func(b *testing.B) {
		header := http.Header{
			tenant.TenantIDHeader: []string{"all"},
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = tenant.ParseHeaders(ctx, header)
		}
	})

	b.Run("no headers single tenant", func(b *testing.B) {
		singleTenantUser := &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
			},
		}
		singleCtx := authn.Context(context.Background(), singleTenantUser)
		header := http.Header{}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = tenant.ParseHeaders(singleCtx, header)
		}
	})
}

func BenchmarkHTTPMiddleware(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := tenant.HTTPMiddleware()(handler)
	tenantID := uuid.New()
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Roles: map[uuid.UUID]authn.Role{
			tenantID: authn.ROLE_READER,
		},
	}

	b.Run("no tenants", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rec.Body.Reset()
			middleware.ServeHTTP(rec, req)
		}
	})

	b.Run("with tenants", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tenant.TenantIDHeader, tenantID.String())
		ctx := authn.Context(req.Context(), user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rec.Body.Reset()
			middleware.ServeHTTP(rec, req)
		}
	})
}
