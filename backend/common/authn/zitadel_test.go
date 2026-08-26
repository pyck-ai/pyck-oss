package authn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/mocks"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	// testOrganizationCacheTTL is a deliberately tiny org-active verdict TTL used by
	// tests that need the cached verdict to expire between requests;
	// testOrganizationCacheWait is a comfortably-longer sleep to guarantee expiry
	// without flaking.
	testOrganizationCacheTTL  = 20 * time.Millisecond
	testOrganizationCacheWait = 60 * time.Millisecond

	// Fixed identity used by the activeIntrospection verdict-cache fixture.
	verdictIssuer = "https://auth.example.com"
	verdictSub    = "user123"
	verdictOrgID  = "org456"
)

func TestNewZitadelAuthProvider(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	config := config.ZitadelConfig{
		ZitadelAudience:           "https://test.example.com",
		ZitadelOrganizationId:     "123456789",
		ZitadelProjectId:          "987654321",
		ZitadelAppKeyPath:         "/path/to/key",
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })

	assert.NotNil(t, provider, "Provider should not be nil")
	// Verify it implements the Authenticator interface
	var _ authn.Authenticator = provider
}

func TestZitadelAuthProvider_Authenticate(t *testing.T) {
	t.Parallel()

	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	testCases := []struct {
		name           string
		token          string
		setupMock      func(*mocks.MockZitadelClient)
		expectedError  error
		validateResult func(*testing.T, authn.User)
	}{
		{
			name:  "successful authentication with roles",
			token: "valid-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "valid-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             issuer,
					Sub:             userSub,
					Username:        "testuser@example.com",
					ResourceOwnerID: orgID,
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles: map[string]map[string]string{
						"admin": {
							orgID: orgID,
						},
						"writer": {
							"org789": "org789",
						},
					},
				}, nil)
			},
			expectedError: nil,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				assert.Equal(t, authn.ComputeUUID(issuer, userSub), user.ID)
				assert.Equal(t, authn.ComputeUUID(issuer, orgID), user.TenantID)
				assert.Equal(t, "testuser@example.com", user.Username)
				assert.Len(t, user.Roles, 2)

				// Check role for main org
				orgUUID := authn.ComputeUUID(issuer, orgID)
				assert.Equal(t, authn.ROLE_ADMIN, user.Roles[orgUUID])

				// Check role for secondary org
				org789UUID := authn.ComputeUUID(issuer, "org789")
				assert.Equal(t, authn.ROLE_WRITER, user.Roles[org789UUID])
			},
		},
		{
			name:  "successful authentication without roles",
			token: "valid-token-no-roles",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "valid-token-no-roles").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             issuer,
					Sub:             userSub,
					Username:        "testuser@example.com",
					ResourceOwnerID: orgID,
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles:    nil,
				}, nil)
			},
			expectedError: nil,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				assert.Equal(t, authn.ComputeUUID(issuer, userSub), user.ID)
				assert.Equal(t, authn.ComputeUUID(issuer, orgID), user.TenantID)
				assert.Equal(t, "testuser@example.com", user.Username)
				assert.Nil(t, user.Roles)
			},
		},
		{
			name:  "empty token returns unauthorized",
			token: "",
			setupMock: func(m *mocks.MockZitadelClient) {
				// No mock setup needed - should return early
			},
			expectedError: authn.ErrUnauthorized,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				assert.Equal(t, uuid.Nil, user.ID)
				assert.Equal(t, uuid.Nil, user.TenantID)
			},
		},
		{
			name:  "introspection error returns unauthorized",
			token: "error-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "error-token").Return(
					nil, assert.AnError,
				)
			},
			expectedError: authn.ErrUnauthorized,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				assert.Equal(t, uuid.Nil, user.ID)
				assert.Equal(t, uuid.Nil, user.TenantID)
			},
		},
		{
			name:  "inactive token returns unauthorized",
			token: "inactive-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "inactive-token").Return(&zitadel.IntrospectionResult{
					Active: false,
				}, nil)
			},
			expectedError: authn.ErrUnauthorized,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				assert.Equal(t, uuid.Nil, user.ID)
				assert.Equal(t, uuid.Nil, user.TenantID)
			},
		},
		{
			name:  "multiple roles for same org keeps highest privilege",
			token: "multi-role-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "multi-role-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             issuer,
					Sub:             userSub,
					Username:        "testuser@example.com",
					ResourceOwnerID: orgID,
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles: map[string]map[string]string{
						"reader": {
							orgID: orgID,
						},
						"admin": {
							orgID: orgID,
						},
						"writer": {
							orgID: orgID,
						},
					},
				}, nil)
			},
			expectedError: nil,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				orgUUID := authn.ComputeUUID(issuer, orgID)
				assert.Equal(t, authn.ROLE_ADMIN, user.Roles[orgUUID])
				assert.Len(t, user.Roles, 1)
			},
		},
		{
			name:  "unknown roles are skipped",
			token: "unknown-role-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "unknown-role-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             issuer,
					Sub:             userSub,
					Username:        "testuser@example.com",
					ResourceOwnerID: orgID,
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles: map[string]map[string]string{
						"unknown_role": {
							orgID: orgID,
						},
						"writer": {
							orgID: orgID,
						},
					},
				}, nil)
			},
			expectedError: nil,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				orgUUID := authn.ComputeUUID(issuer, orgID)
				assert.Equal(t, authn.ROLE_WRITER, user.Roles[orgUUID])
				assert.Len(t, user.Roles, 1)
			},
		},
		{
			name:  "service roles captured separately from ladder roles",
			token: "service-role-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "service-role-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             issuer,
					Sub:             userSub,
					Username:        "testuser@example.com",
					ResourceOwnerID: orgID,
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles: map[string]map[string]string{
						"writer": {
							orgID: orgID,
						},
						"inventory_service": {
							orgID: orgID,
						},
						"picking_service": {
							orgID: orgID,
						},
					},
				}, nil)
			},
			expectedError: nil,
			validateResult: func(t *testing.T, user authn.User) {
				t.Helper()
				orgUUID := authn.ComputeUUID(issuer, orgID)
				// Ladder role stays in Roles; service roles never pollute it.
				assert.Equal(t, authn.ROLE_WRITER, user.Roles[orgUUID])
				assert.Len(t, user.Roles, 1)
				// Service roles are captured separately and queryable.
				assert.True(t, user.HasServiceRole("inventory_service", orgUUID))
				assert.True(t, user.HasServiceRole("picking_service", orgUUID))
				assert.False(t, user.HasServiceRole("receiving_service", orgUUID))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockClient := mocks.NewMockZitadelClient()
			if tc.setupMock != nil {
				tc.setupMock(mockClient)
			}

			config := config.ZitadelConfig{
				ZitadelPATCacheTTL:        1 * time.Hour,
				ZitadelPATCacheTTLOverlap: 1 * time.Minute,
			}

			provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
			ctx := t.Context()

			user, err := provider.Authenticate(ctx, tc.token)

			if tc.expectedError != nil {
				assert.ErrorIs(t, err, tc.expectedError)
			} else {
				assert.NoError(t, err)
			}

			if tc.validateResult != nil {
				tc.validateResult(t, user)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestZitadelAuthProvider_Authenticate_Caching(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// Setup mock to be called only once
	mockClient.On("IntrospectToken", mock.Anything, "cached-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "cached@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"admin": {
				orgID: orgID,
			},
		},
	}, nil).Once() // Important: should be called only once due to caching

	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := t.Context()

	// First call - should hit the API
	user1, err1 := provider.Authenticate(ctx, "cached-token")
	require.NoError(t, err1)
	assert.Equal(t, "cached@example.com", user1.Username)

	// Second call - should use cache
	user2, err2 := provider.Authenticate(ctx, "cached-token")
	require.NoError(t, err2)
	assert.Equal(t, "cached@example.com", user2.Username)

	// Verify users are identical
	assert.Equal(t, user1.ID, user2.ID)
	assert.Equal(t, user1.TenantID, user2.TenantID)
	assert.Equal(t, user1.Username, user2.Username)

	// Verify mock was only called once
	mockClient.AssertExpectations(t)
}

func TestZitadelAuthProvider_Authenticate_CacheTTL(t *testing.T) {
	t.Parallel()

	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	testCases := []struct {
		name        string
		tokenExpiry time.Duration
		cacheTTL    time.Duration
		overlap     time.Duration
		expectedTTL time.Duration
	}{
		{
			name:        "token expires before cache TTL",
			tokenExpiry: 30 * time.Minute,
			cacheTTL:    1 * time.Hour,
			overlap:     1 * time.Minute,
			expectedTTL: 29 * time.Minute, // tokenExpiry - overlap
		},
		{
			name:        "cache TTL expires before token",
			tokenExpiry: 2 * time.Hour,
			cacheTTL:    1 * time.Hour,
			overlap:     1 * time.Minute,
			expectedTTL: 59 * time.Minute, // cacheTTL - overlap
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Setup mock
			mockClient := mocks.NewMockZitadelClient()
			mockClient.On("IntrospectToken", mock.Anything, "ttl-test-token").Return(&zitadel.IntrospectionResult{
				Active:          true,
				Iss:             issuer,
				Sub:             userSub,
				Username:        "ttltest@example.com",
				ResourceOwnerID: orgID,
				Exp:             time.Now().Add(tc.tokenExpiry).Unix(),
				ProjectRoles:    nil,
			}, nil)

			config := config.ZitadelConfig{
				ZitadelPATCacheTTL:        tc.cacheTTL,
				ZitadelPATCacheTTLOverlap: tc.overlap,
			}

			provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
			ctx := t.Context()

			user, err := provider.Authenticate(ctx, "ttl-test-token")
			require.NoError(t, err)
			assert.Equal(t, "ttltest@example.com", user.Username)

			mockClient.AssertExpectations(t)
		})
	}
}

func TestZitadelAuthProvider_HTTPMiddleware(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name               string
		authHeader         string
		setupMock          func(*mocks.MockZitadelClient)
		expectedStatusCode int
		expectUserInCtx    bool
	}{
		{
			name:       "successful authentication with Bearer token",
			authHeader: "Bearer valid-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "valid-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             "https://auth.example.com",
					Sub:             "user123",
					Username:        "middleware@example.com",
					ResourceOwnerID: "org456",
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles: map[string]map[string]string{
						"admin": {
							"org456": "org456",
						},
					},
				}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectUserInCtx:    true,
		},
		{
			name:       "successful authentication with raw token",
			authHeader: "raw-valid-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "raw-valid-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             "https://auth.example.com",
					Sub:             "user123",
					Username:        "middleware@example.com",
					ResourceOwnerID: "org456",
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles:    nil,
				}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectUserInCtx:    true,
		},
		{
			name:               "no auth header - continues without auth",
			authHeader:         "",
			setupMock:          nil,
			expectedStatusCode: http.StatusOK,
			expectUserInCtx:    false,
		},
		{
			name:       "invalid token returns 401",
			authHeader: "Bearer invalid-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "invalid-token").Return(
					nil, assert.AnError,
				)
			},
			expectedStatusCode: http.StatusUnauthorized,
			expectUserInCtx:    false,
		},
		{
			name:       "inactive token returns 401",
			authHeader: "Bearer inactive-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "inactive-token").Return(&zitadel.IntrospectionResult{
					Active: false,
				}, nil)
			},
			expectedStatusCode: http.StatusUnauthorized,
			expectUserInCtx:    false,
		},
		{
			name:       "whitespace handling in header",
			authHeader: "  Bearer   spaced-token  ",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "spaced-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             "https://auth.example.com",
					Sub:             "user123",
					Username:        "spaced@example.com",
					ResourceOwnerID: "org456",
					Exp:             time.Now().Add(1 * time.Hour).Unix(),
					ProjectRoles:    nil,
				}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectUserInCtx:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockClient := mocks.NewMockZitadelClient()
			if tc.setupMock != nil {
				tc.setupMock(mockClient)
			}

			config := config.ZitadelConfig{
				ZitadelPATCacheTTL:        1 * time.Hour,
				ZitadelPATCacheTTLOverlap: 1 * time.Minute,
			}

			provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
			middleware := provider.HTTPMiddleware()

			// Create a test handler that checks for user in context
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user := authn.ForContext(r.Context())
				if tc.expectUserInCtx {
					assert.True(t, user.IsAuthenticated(), "User should be authenticated")
				} else {
					assert.False(t, user.IsAuthenticated(), "User should not be authenticated")
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("OK"))
			})

			// Create request with auth header
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}

			// Create response recorder
			rr := httptest.NewRecorder()

			// Execute middleware
			middleware(testHandler).ServeHTTP(rr, req)

			// Check status code
			assert.Equal(t, tc.expectedStatusCode, rr.Code)

			// If authentication failed, verify error message
			if tc.expectedStatusCode == http.StatusUnauthorized {
				assert.JSONEq(t, `{"error":"Unauthorized"}`, rr.Body.String())
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestZitadelAuthProvider_HTTPMiddleware_NextHandlerCalled(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	middleware := provider.HTTPMiddleware()

	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	middleware(testHandler).ServeHTTP(rr, req)

	assert.True(t, handlerCalled, "Next handler should be called when no auth header")
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestZitadelAuthProvider_Authenticate_ExpiredToken(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		token         string
		setupMock     func(*mocks.MockZitadelClient)
		expectedError error
	}{
		{
			name:  "expired token is still introspected but marked inactive",
			token: "expired-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "expired-token").Return(&zitadel.IntrospectionResult{
					Active:          false,
					Iss:             "https://auth.example.com",
					Sub:             "user123",
					Username:        "expired@example.com",
					ResourceOwnerID: "org456",
					Exp:             time.Now().Add(-1 * time.Hour).Unix(), // Expired 1 hour ago
				}, nil)
			},
			expectedError: authn.ErrUnauthorized,
		},
		{
			name:  "token about to expire gets minimal cache time",
			token: "almost-expired-token",
			setupMock: func(m *mocks.MockZitadelClient) {
				m.On("IntrospectToken", mock.Anything, "almost-expired-token").Return(&zitadel.IntrospectionResult{
					Active:          true,
					Iss:             "https://auth.example.com",
					Sub:             "user123",
					Username:        "almostexpired@example.com",
					ResourceOwnerID: "org456",
					Exp:             time.Now().Add(30 * time.Second).Unix(), // Expires in 30 seconds
					ProjectRoles:    nil,
				}, nil)
			},
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockClient := mocks.NewMockZitadelClient()
			if tc.setupMock != nil {
				tc.setupMock(mockClient)
			}

			config := config.ZitadelConfig{
				ZitadelPATCacheTTL:        1 * time.Hour,
				ZitadelPATCacheTTLOverlap: 5 * time.Minute,
			}

			provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
			ctx := t.Context()

			user, err := provider.Authenticate(ctx, tc.token)

			if tc.expectedError != nil {
				assert.ErrorIs(t, err, tc.expectedError)
				assert.Equal(t, uuid.Nil, user.ID)
			} else {
				assert.NoError(t, err)
				assert.NotEqual(t, uuid.Nil, user.ID)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestZitadelAuthProvider_Authenticate_CacheInvalidation(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// First call returns valid token
	mockClient.On("IntrospectToken", mock.Anything, "cache-test-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "cache@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(2 * time.Second).Unix(), // Very short expiry
		ProjectRoles:    nil,
	}, nil).Once()

	// Second call after expiry
	mockClient.On("IntrospectToken", mock.Anything, "cache-test-token").Return(&zitadel.IntrospectionResult{
		Active:          false,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "cache@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(-1 * time.Second).Unix(), // Already expired
	}, nil).Once()

	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        10 * time.Second, // Short TTL for test
		ZitadelPATCacheTTLOverlap: 1 * time.Second,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := t.Context()

	// First call - should hit the API and cache
	user1, err1 := provider.Authenticate(ctx, "cache-test-token")
	require.NoError(t, err1)
	assert.Equal(t, "cache@example.com", user1.Username)

	// Wait for cache to expire
	time.Sleep(3 * time.Second)

	// Second call - should hit the API again due to cache expiry
	user2, err2 := provider.Authenticate(ctx, "cache-test-token")
	assert.ErrorIs(t, err2, authn.ErrUnauthorized)
	assert.Equal(t, uuid.Nil, user2.ID)

	mockClient.AssertExpectations(t)
}

// TestZitadelAuthProvider_Authenticate_NonPositiveTTLNotCached is the
// deterministic regression test for #1169. A token whose own expiry sits
// inside the overlap window yields a non-positive effective TTL even with a
// valid (overlap < TTL) config. Before the fix, memkv stored such an entry as
// never-expiring and the second Authenticate was served from cache, so a
// revoked token stayed accepted for the process lifetime. The fix skips the
// cache on ttl <= 0, so the second call must re-introspect. No real clock /
// time.Sleep is involved: determinism comes from the token's Exp, not wall
// time, and a stalled runner only makes the TTL more negative.
func TestZitadelAuthProvider_Authenticate_NonPositiveTTLNotCached(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// First call: active token expiring in 5s. With a 1m overlap the
	// effective TTL is ~5s - 1m < 0, so the entry must not be cached.
	mockClient.On("IntrospectToken", mock.Anything, "short-lived-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "shortlived@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(5 * time.Second).Unix(),
		ProjectRoles:    nil,
	}, nil).Once()

	// Second call: the token has since been revoked. Reaching this mock at
	// all proves the first call did not poison the cache.
	mockClient.On("IntrospectToken", mock.Anything, "short-lived-token").Return(&zitadel.IntrospectionResult{
		Active:          false,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "shortlived@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(-1 * time.Second).Unix(),
	}, nil).Once()

	// Valid config (overlap < TTL): the non-positive TTL comes from the
	// token's own Exp clamp, not a misconfigured overlap, so the
	// constructor's overlap < TTL guard does not fire.
	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := t.Context()

	// First call succeeds but must not cache (ttl <= 0).
	user1, err1 := provider.Authenticate(ctx, "short-lived-token")
	require.NoError(t, err1)
	assert.Equal(t, "shortlived@example.com", user1.Username)

	// Second call must re-introspect rather than serve a poisoned entry,
	// observe the revocation, and reject.
	user2, err2 := provider.Authenticate(ctx, "short-lived-token")
	require.ErrorIs(t, err2, authn.ErrUnauthorized)
	assert.Equal(t, uuid.Nil, user2.ID)

	// Both introspections must have happened: the cache was never poisoned.
	mockClient.AssertExpectations(t)
}

func TestZitadelAuthProvider_Authenticate_Concurrent(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// Allow multiple calls - while the cache is thread-safe, there's a race between
	// checking the cache and populating it, so concurrent initial requests may all
	// introspect the token before the first one populates the cache
	mockClient.On("IntrospectToken", mock.Anything, "concurrent-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "concurrent@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"admin": {
				orgID: orgID,
			},
		},
	}, nil)

	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := t.Context()

	// Run multiple concurrent authentication requests
	const numGoroutines = 10
	results := make(chan authn.User, numGoroutines)
	errors := make(chan error, numGoroutines)

	for range numGoroutines {
		go func() {
			user, err := provider.Authenticate(ctx, "concurrent-token")
			if err != nil {
				errors <- err
			} else {
				results <- user
			}
		}()
	}

	// Collect results
	var users []authn.User
	for range numGoroutines {
		select {
		case user := <-results:
			users = append(users, user)
		case err := <-errors:
			t.Errorf("Unexpected error in concurrent authentication: %v", err)
		}
	}

	// Verify all users are the same
	assert.Len(t, users, numGoroutines)
	for i := 1; i < len(users); i++ {
		assert.Equal(t, users[0].ID, users[i].ID, "All users should have the same ID")
		assert.Equal(t, users[0].Username, users[i].Username, "All users should have the same Username")
		assert.Equal(t, users[0].TenantID, users[i].TenantID, "All users should have the same TenantID")
	}
}

func TestZitadelAuthProvider_Authenticate_ConcurrentCaching(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// First warm up the cache with a single call
	mockClient.On("IntrospectToken", mock.Anything, "concurrent-cached-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "concurrent@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"admin": {
				orgID: orgID,
			},
		},
	}, nil).Once() // Should only be called once

	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := t.Context()

	// Warm up the cache
	_, err := provider.Authenticate(ctx, "concurrent-cached-token")
	require.NoError(t, err)

	// Run multiple concurrent authentication requests
	const numGoroutines = 10
	results := make(chan authn.User, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			user, err := provider.Authenticate(ctx, "concurrent-cached-token")
			if err != nil {
				errors <- err
			} else {
				results <- user
			}
		}()
	}

	// Collect results
	var users []authn.User
	for i := 0; i < numGoroutines; i++ {
		select {
		case user := <-results:
			users = append(users, user)
		case err := <-errors:
			t.Errorf("Unexpected error in concurrent authentication: %v", err)
		}
	}

	// Verify all users are the same
	assert.Len(t, users, numGoroutines)
	for i := 1; i < len(users); i++ {
		assert.Equal(t, users[0].ID, users[i].ID, "All users should have the same ID")
		assert.Equal(t, users[0].Username, users[i].Username, "All users should have the same Username")
		assert.Equal(t, users[0].TenantID, users[i].TenantID, "All users should have the same TenantID")
	}

	// Verify the mock was called only once (during warmup)
	mockClient.AssertExpectations(t)
}

func BenchmarkZitadelAuthProvider_Authenticate(b *testing.B) {
	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// Set up mock to be called multiple times
	mockClient.On("IntrospectToken", mock.Anything, mock.Anything).Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "benchmark@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"admin": {
				orgID: orgID,
			},
			"writer": {
				"org789": "org789",
			},
		},
	}, nil)

	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := b.Context()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Use a unique token for each iteration to avoid cache
			token := "bench-token-" + time.Now().Format(time.RFC3339Nano)
			_, _ = provider.Authenticate(ctx, token)
		}
	})
}

// Regression test for M2 (PR #1172 round-2 review).
//
// Pre-fix behaviour: the cache-hit branch evicted the entry and 401'd
// whenever the org validator returned ANY error — infra fault and
// definite revoke were indistinguishable. A 30s blip in the validator
// (Zitadel/management/gateway) turned into fleet-wide 401s plus a load
// spike against the already-degraded dependency (every evicted token
// re-introspects on the next request and fails the validator again).
//
// Post-fix behaviour: only (false, nil) "definite no" evicts and 401s.
// (false, err) "infra fault" stays cached for the remaining TTL — the
// staleness the cache already accepts is the right risk budget when
// the validator can't tell us anything.
func TestZitadelAuthProvider_Authenticate_ValidatorErrorDoesNotEvictCache(t *testing.T) {
	t.Parallel()

	issuer := "https://auth.example.com"
	userSub := "user-m2"
	orgID := "org-m2"

	mockClient := mocks.NewMockZitadelClient()
	// Mock should only be called ONCE — the first request introspects,
	// the second request hits the cache. After the fix, the validator
	// failure between the two requests must NOT cause re-introspection.
	mockClient.On("IntrospectToken", mock.Anything, "blip-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "blip@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"writer": {orgID: orgID},
		},
	}, nil).Once()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		// Short verdict TTL so the validator is actually re-consulted on the
		// second request (otherwise the cached positive verdict short-circuits
		// the validator entirely and the error branch is never reached).
		ZitadelOrganizationCacheTTL: testOrganizationCacheTTL,
	}

	// Toggle for the validator: first call returns true (warming the
	// cache); subsequent calls return the configured failure.
	var validatorFailure error
	validatorActive := true
	validator := func(ctx context.Context, sub string) (bool, error) {
		return validatorActive, validatorFailure
	}

	provider := authn.NewZitadelAuthProvider(mockClient, cfg, validator)
	ctx := t.Context()

	// First call: introspect + cache + validator passes.
	user1, err := provider.Authenticate(ctx, "blip-token")
	require.NoError(t, err)
	assert.Equal(t, "blip@example.com", user1.Username)

	// Validator now fails with an infrastructure error. This is what
	// happens during a transient blip — management is down, gateway
	// 5xx, Zitadel timeout, etc.
	validatorFailure = assert.AnError
	validatorActive = false // also flip the bool to make sure the error
	// path is exercised before the bool path is even consulted.

	// Let the cached positive verdict expire so the validator's error path is
	// genuinely exercised on the next request.
	time.Sleep(testOrganizationCacheWait)

	// Second call: cache hit + validator errors. After M2 fix, the
	// cached user must be returned and the cache MUST NOT be evicted.
	user2, err := provider.Authenticate(ctx, "blip-token")
	require.NoError(t, err, "validator infra fault must not 401 — cache TTL is the existing staleness budget")
	assert.Equal(t, user1.ID, user2.ID, "must return the same cached user, not re-introspect")

	// Third call confirms the cache is intact (had eviction happened
	// on call 2, this would trigger a second IntrospectToken and the
	// .Once() mock would fail AssertExpectations).
	validatorFailure = nil
	validatorActive = true
	user3, err := provider.Authenticate(ctx, "blip-token")
	require.NoError(t, err)
	assert.Equal(t, user1.ID, user3.ID, "validator recovery: same cached entry served")

	// .Once() assertion — exactly one introspection across 3 requests.
	mockClient.AssertExpectations(t)
}

// Companion test confirming M2's fix did NOT relax the routine
// revocation path. A (false, nil) definite-no MUST still evict the
// cache and 401, otherwise the OnTenantDisabled NATS-eviction fast
// path would be the ONLY revocation mechanism (M8's collapsed-consumer
// bug would then be load-bearing).
func TestZitadelAuthProvider_Authenticate_ValidatorRevokeStillEvicts(t *testing.T) {
	t.Parallel()

	issuer := "https://auth.example.com"
	userSub := "user-revoke"
	orgID := "org-revoke"

	mockClient := mocks.NewMockZitadelClient()
	// Two introspections expected: one to warm the cache, one after
	// the revoke evicts and the next request re-introspects.
	mockClient.On("IntrospectToken", mock.Anything, "revoke-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "revoke@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"writer": {orgID: orgID},
		},
	}, nil).Twice()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		// Short org-active TTL so the verdict expires between calls and the
		// validator (now definite-no) is consulted on the second request.
		// Without this the cached positive verdict would mask the revocation
		// until DefaultOrganizationCacheTTL elapses — that TTL-bounded staleness
		// is covered separately by the verdict-cache tests.
		ZitadelOrganizationCacheTTL: testOrganizationCacheTTL,
	}

	validatorActive := true
	validator := func(ctx context.Context, sub string) (bool, error) {
		return validatorActive, nil
	}

	provider := authn.NewZitadelAuthProvider(mockClient, cfg, validator)
	ctx := t.Context()

	// Warm cache.
	_, err := provider.Authenticate(ctx, "revoke-token")
	require.NoError(t, err)

	// Validator flips to definite-no (org went inactive).
	validatorActive = false

	// Let the cached positive verdict expire so the validator is re-consulted.
	time.Sleep(testOrganizationCacheWait)

	// Second call must reject — this is a real revocation, not a fault.
	_, err = provider.Authenticate(ctx, "revoke-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized, "definite-no must reject")

	// Third call must re-introspect (cache was evicted on call 2).
	// We flip the validator back to active to simulate recovery; if the
	// cache wasn't evicted, this call would short-circuit on the stale
	// cache hit and .Twice() expectation would fail.
	validatorActive = true
	_, err = provider.Authenticate(ctx, "revoke-token")
	require.NoError(t, err)

	mockClient.AssertExpectations(t)
}

// countingValidator returns an OrgValidator that records how many times it was
// invoked and yields the configured (active, err). The counter lets tests prove
// the org-active verdict cache short-circuits the validator.
func countingValidator(calls *atomic.Int64, active bool, err error) authn.OrgValidator {
	return func(_ context.Context, _ string) (bool, error) {
		calls.Add(1)
		return active, err
	}
}

func activeIntrospection() *zitadel.IntrospectionResult {
	return &zitadel.IntrospectionResult{
		Active:          true,
		Iss:             verdictIssuer,
		Sub:             verdictSub,
		Username:        "verdict@example.com",
		ResourceOwnerID: verdictOrgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles:    map[string]map[string]string{"writer": {verdictOrgID: verdictOrgID}},
	}
}

// The core promise of #1312: a positive org-active verdict is cached, so the
// validator (a synchronous Zitadel / management round-trip) is NOT called on
// every request — only once per tenant per TTL window.
func TestZitadelAuthProvider_OrganizationVerdictCached(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "verdict-token").
		Return(activeIntrospection(), nil).Once()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:          1 * time.Hour,
		ZitadelPATCacheTTLOverlap:   1 * time.Minute,
		ZitadelOrganizationCacheTTL: 1 * time.Hour, // long enough to never expire mid-test
	}

	var calls atomic.Int64
	provider := authn.NewZitadelAuthProvider(mockClient, cfg, countingValidator(&calls, true, nil))
	ctx := t.Context()

	const requests = 5
	for range requests {
		_, err := provider.Authenticate(ctx, "verdict-token")
		require.NoError(t, err)
	}

	assert.Equal(t, int64(1), calls.Load(),
		"validator must be consulted once across %d requests; the rest hit the verdict cache", requests)
	mockClient.AssertExpectations(t) // introspected exactly once too
}

// A cached positive verdict must be dropped the moment OnTenantDisabled fires
// (the NATS revocation fast-path), so the next request re-checks and catches
// the now-inactive tenant rather than serving the stale verdict until TTL.
func TestZitadelAuthProvider_OrganizationVerdictEvictedOnTenantDisabled(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "evict-token").
		Return(activeIntrospection(), nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:          1 * time.Hour,
		ZitadelPATCacheTTLOverlap:   1 * time.Minute,
		ZitadelOrganizationCacheTTL: 1 * time.Hour, // only eviction, not TTL, should clear it
	}

	var calls atomic.Int64
	provider := authn.NewZitadelAuthProvider(mockClient, cfg, countingValidator(&calls, true, nil))
	ctx := t.Context()

	user, err := provider.Authenticate(ctx, "evict-token")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())

	// Second request is served from the verdict cache.
	_, err = provider.Authenticate(ctx, "evict-token")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load(), "verdict cache should short-circuit the validator")

	// Tenant disabled via the NATS fast-path. This clears both the token cache
	// and the verdict cache for the tenant.
	provider.OnTenantDisabled(user.TenantID)

	_, err = provider.Authenticate(ctx, "evict-token")
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load(),
		"after OnTenantDisabled the verdict must be re-checked, not served from cache")
}

// When the verdict TTL elapses the validator is consulted again — the bounded
// staleness backstop for the case where a disable's NATS event was missed.
func TestZitadelAuthProvider_OrganizationVerdictExpiresAfterTTL(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	// Token cache TTL is long, so introspection happens exactly once even
	// though the verdict is re-evaluated after its own (short) TTL.
	mockClient.On("IntrospectToken", mock.Anything, "ttl-token").
		Return(activeIntrospection(), nil).Once()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:          1 * time.Hour,
		ZitadelPATCacheTTLOverlap:   1 * time.Minute,
		ZitadelOrganizationCacheTTL: testOrganizationCacheTTL,
	}

	var calls atomic.Int64
	provider := authn.NewZitadelAuthProvider(mockClient, cfg, countingValidator(&calls, true, nil))
	ctx := t.Context()

	_, err := provider.Authenticate(ctx, "ttl-token")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())

	// Immediately again: still cached.
	_, err = provider.Authenticate(ctx, "ttl-token")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())

	// After the verdict TTL elapses the validator runs again.
	time.Sleep(testOrganizationCacheWait)
	_, err = provider.Authenticate(ctx, "ttl-token")
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load(), "verdict must be re-evaluated once its TTL expires")

	mockClient.AssertExpectations(t) // introspected once despite the re-validation
}

// An infrastructure fault (false, err) from the validator must NOT be cached,
// otherwise a transient blip would be remembered as a verdict.
func TestZitadelAuthProvider_OrganizationVerdictErrorNotCached(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "err-token").
		Return(activeIntrospection(), nil).Once()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:          1 * time.Hour,
		ZitadelPATCacheTTLOverlap:   1 * time.Minute,
		ZitadelOrganizationCacheTTL: 1 * time.Hour,
	}

	var calls atomic.Int64
	// Validator always faults. A fault is not a revocation, so Authenticate
	// keeps serving the cached user — but the verdict itself must not stick.
	provider := authn.NewZitadelAuthProvider(mockClient, cfg, countingValidator(&calls, false, assert.AnError))
	ctx := t.Context()

	_, err := provider.Authenticate(ctx, "err-token")
	require.NoError(t, err, "validator fault is not a revocation")
	require.Equal(t, int64(1), calls.Load())

	// Because the fault was not cached, the next request consults the validator
	// again rather than serving a stale (and wrong) cached verdict.
	_, err = provider.Authenticate(ctx, "err-token")
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load(), "faulted verdict must not be cached")
}

// A zero/unset ZitadelOrganizationCacheTTL must fall back to the default constant
// and still enable caching — it must NOT disable caching, nor pass 0 to memkv
// (which would make the entry permanent, the very bug #1311 tracks).
func TestZitadelAuthProvider_OrganizationVerdictDefaultTTLApplied(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "default-token").
		Return(activeIntrospection(), nil).Once()

	// ZitadelOrganizationCacheTTL intentionally left unset (zero).
	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}
	require.Positive(t, authn.DefaultOrganizationCacheTTL, "default TTL must be a positive duration")

	var calls atomic.Int64
	provider := authn.NewZitadelAuthProvider(mockClient, cfg, countingValidator(&calls, true, nil))
	ctx := t.Context()

	_, err := provider.Authenticate(ctx, "default-token")
	require.NoError(t, err)
	_, err = provider.Authenticate(ctx, "default-token")
	require.NoError(t, err)

	assert.Equal(t, int64(1), calls.Load(),
		"zero config must fall back to DefaultOrganizationCacheTTL and keep caching enabled")
	mockClient.AssertExpectations(t)
}

// NewZitadelAuthProvider's doc says the validator MUST NOT be nil.
// Pre-fix the constructor accepted nil and the panic surfaced on the
// FIRST authenticated request — a misconfigured service would boot
// successfully and then crash an auth-middleware goroutine on the
// first cache miss with a nil-func panic. Post-fix the constructor
// panics at startup so a wiring mistake fails loud at boot.
func TestNewZitadelAuthProvider_NilOrgValidatorPanics(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	assert.Panics(t, func() {
		authn.NewZitadelAuthProvider(mockClient, cfg, nil)
	}, "construction with a nil OrgValidator must panic — the doc says MUST NOT be nil")
}

// Boundary of the overlap-vs-TTL guard. overlap > TTL is nonsensical (every
// token computes a negative effective TTL, the #1169 eternal-cache trigger)
// and must fail loud at boot. overlap == TTL yields a zero effective TTL,
// which is a legitimate "disable caching, re-introspect every request" config
// and must boot fine — the Authenticate path skips the cache on ttl <= 0.
func TestNewZitadelAuthProvider_OverlapValidation(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	validator := func(ctx context.Context, sub string) (bool, error) { return true, nil }

	testCases := []struct {
		name        string
		ttl         time.Duration
		overlap     time.Duration
		expectPanic bool
	}{
		{name: "overlap below TTL boots", ttl: 1 * time.Hour, overlap: 1 * time.Minute, expectPanic: false},
		{name: "overlap equals TTL boots (zero effective TTL is intentional)", ttl: 1 * time.Minute, overlap: 1 * time.Minute, expectPanic: false},
		{name: "overlap exceeds TTL panics", ttl: 5 * time.Second, overlap: 1 * time.Minute, expectPanic: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.ZitadelConfig{
				ZitadelPATCacheTTL:        tc.ttl,
				ZitadelPATCacheTTLOverlap: tc.overlap,
			}

			construct := func() { authn.NewZitadelAuthProvider(mockClient, cfg, validator).Close() }

			if tc.expectPanic {
				assert.Panics(t, construct, "overlap > TTL must panic — it produces an eternal cache (#1169)")
			} else {
				assert.NotPanics(t, construct, "overlap <= TTL must boot — zero TTL disables caching, it is not an error")
			}
		})
	}
}

// Authenticate must reject tokens whose introspection result has an
// empty `sub` claim before the User is cached. Pre-fix an empty Sub
// reached the cache; every subsequent cache hit called the validator
// with sub="", which returns ErrResolveOrganizationEmptySub. The
// auth provider treats validator errors as "infra fault, stay cached"
// — so the request was admitted for the full TTL despite the
// caller-bug error.
func TestZitadelAuthProvider_Authenticate_EmptySubRejects(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "no-sub-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             "https://auth.example.com",
		Sub:             "", // empty
		Username:        "anon@example.com",
		ResourceOwnerID: "org-x",
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, cfg, func(ctx context.Context, sub string) (bool, error) {
		// If empty Sub ever reaches the validator, the test should
		// flag it loudly — but the auth path must reject before this.
		t.Errorf("validator must not be called for empty Sub; got sub=%q", sub)
		return true, nil
	})

	_, err := provider.Authenticate(t.Context(), "no-sub-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized,
		"empty Sub claim is a caller bug — reject up front, do NOT cache and re-error every request")
}

// System-role upgrade must key on the home-org-derived tenant ID
// (ComputeUUID(resp.Iss, resp.ResourceOwnerID)), NOT on the
// claim-derived tenant ID. The role map is keyed by ComputeUUID of
// each project grant — the home-org's ROLE_SYSTEM entry sits at the
// resource_owner-derived key. Pre-fix the check used the claim-derived
// tenantID; a webhook misconfig stamping the wrong pyck_tenant_id would
// silently demote a system PAT to a normal user with the broken
// tenant's scoping.
func TestZitadelAuthProvider_Authenticate_SystemRoleUsesHomeOrgKey(t *testing.T) {
	t.Parallel()

	issuer := "https://auth.example.com"
	const homeOrgID = "home-org"

	// Claim points at a DIFFERENT tenant than the home org. With a
	// well-formed UUID, the introspection-path takes the "claim wins"
	// branch and sets user.TenantID = claim. The system-role lookup
	// must still resolve against ComputeUUID(iss, home-org).
	claimTenant := uuid.New()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "sys-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             "sys-user",
		Username:        "system@example.com",
		ResourceOwnerID: homeOrgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		PyckTenantID:    claimTenant.String(),
		ProjectRoles: map[string]map[string]string{
			"system": {
				homeOrgID: homeOrgID,
			},
		},
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, cfg, func(ctx context.Context, sub string) (bool, error) {
		return true, nil
	})

	user, err := provider.Authenticate(t.Context(), "sys-token")
	require.NoError(t, err)
	assert.True(t, user.IsSystemUser(),
		"ROLE_SYSTEM grant on the home org must upgrade to SystemUser regardless of claim/computed divergence")
}

func BenchmarkZitadelAuthProvider_AuthenticateWithCache(b *testing.B) {
	mockClient := mocks.NewMockZitadelClient()
	issuer := "https://auth.example.com"
	userSub := "user123"
	orgID := "org456"

	// Mock will be called only once due to caching
	mockClient.On("IntrospectToken", mock.Anything, "cached-bench-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             userSub,
		Username:        "benchmark@example.com",
		ResourceOwnerID: orgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"admin": {
				orgID: orgID,
			},
		},
	}, nil)

	config := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider := authn.NewZitadelAuthProvider(mockClient, config, func(ctx context.Context, sub string) (bool, error) { return true, nil })
	ctx := b.Context()

	// Warm up cache
	_, _ = provider.Authenticate(ctx, "cached-bench-token")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = provider.Authenticate(ctx, "cached-bench-token")
		}
	})
}
