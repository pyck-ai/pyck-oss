package authn_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/mocks"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

const (
	// testLivenessCacheTTL is a deliberately tiny liveness-verdict TTL used by
	// tests that need the cached verdict to expire between requests;
	// testLivenessCacheWait is a comfortably-longer sleep to guarantee expiry
	// without flaking.
	testLivenessCacheTTL  = 20 * time.Millisecond
	testLivenessCacheWait = 60 * time.Millisecond

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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))

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

			provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

			provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

			provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

			provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	provider := newProvider(t, mockClient, config, livenessmocks.NoDisabledTenants, newLivenessCache(t, config))
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

	cache := liveness.NewCache(config.LivenessCacheTTL)
	b.Cleanup(cache.Close)
	provider := newProvider(b, mockClient, config, livenessmocks.NoDisabledTenants, cache)
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

// A source infrastructure fault denies the request (an unverifiable
// tenant is not served) but must NOT evict the cached token: the fault is
// transient, and evicting would re-introspect every request of the outage
// at Zitadel only to be refused again, and would delay recovery once the
// source answers. Only a definite "tenant is not served"
// verdict evicts. The mock's .Once() is the teeth: any eviction shows up
// as a second IntrospectToken call.
func TestZitadelAuthProvider_Authenticate_SourceErrorDoesNotEvictCache(t *testing.T) {
	t.Parallel()

	issuer := "https://auth.example.com"
	userSub := "user-m2"
	orgID := "org-m2"

	mockClient := mocks.NewMockZitadelClient()
	// Mock should only be called ONCE: the first request introspects,
	// the second request hits the cache. The source failure between the
	// two requests must NOT cause re-introspection.
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
		// Short verdict TTL so the source is actually re-consulted on the
		// second request (otherwise the cached live verdict short-circuits
		// the source entirely and the error branch is never reached).
		LivenessCacheTTL: testLivenessCacheTTL,
	}

	// Toggle for the source: the first call reports the tenant live (warming
	// the cache); subsequent calls return the configured failure.
	var sourceFailure error
	sourceLive := true
	source := func(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
		if sourceFailure != nil {
			return nil, sourceFailure
		}
		if sourceLive {
			return nil, nil
		}
		return ids, nil
	}

	provider := newProvider(t, mockClient, cfg, source, newLivenessCache(t, cfg))
	ctx := t.Context()

	// First call: introspect + cache + the source reports the tenant live.
	user1, err := provider.Authenticate(ctx, "blip-token")
	require.NoError(t, err)
	assert.Equal(t, "blip@example.com", user1.Username)

	// The source now fails with an infrastructure error. This is what
	// happens during a transient blip: management is down, the gateway
	// answers 5xx, a request times out.
	sourceFailure = assert.AnError
	sourceLive = false // also flip the bool to make sure the error
	// path is exercised before the bool path is even consulted.

	// Let the cached live verdict expire so the source's error path is
	// genuinely exercised on the next request.
	time.Sleep(testLivenessCacheWait)

	// Second call: token-cache hit + source errors. The request is denied
	// (an unverifiable tenant is not served) but the cache MUST NOT be
	// evicted: the fault is transient.
	_, err = provider.Authenticate(ctx, "blip-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized,
		"a tenant the source cannot verify must not be served")

	// Third call confirms the cache is intact (had eviction happened
	// on call 2, this would trigger a second IntrospectToken and the
	// .Once() mock would fail AssertExpectations) and that service
	// resumes immediately once the source answers.
	sourceFailure = nil
	sourceLive = true
	user3, err := provider.Authenticate(ctx, "blip-token")
	require.NoError(t, err)
	assert.Equal(t, user1.ID, user3.ID, "source recovery: same cached entry served")

	// .Once() assertion — exactly one introspection across 3 requests.
	mockClient.AssertExpectations(t)
}

// The fresh-introspection leg of the revocation rule: a token whose tenant
// is already denied when it is FIRST seen must never enter the token cache
// as a usable identity. SourceRevokeStillEvicts covers the cached-token
// leg (warm cache, then revoke); without this test both legs hang on that
// single assertion, and a regression that skipped the post-introspection
// check while keeping the cached-path one would go unseen.
func TestZitadelAuthProvider_Authenticate_DeniedTenantRejectsOnFirstSight(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "dead-on-arrival").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             "https://auth.example.com",
		Sub:             "user-doa",
		Username:        "doa@example.com",
		ResourceOwnerID: "org-doa",
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"writer": {"org-doa": "org-doa"},
		},
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}
	// The source reports every asked-about tenant as denied: the row was
	// deleted, or never existed, before the token ever authenticated.
	source := func(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
		return ids, nil
	}
	provider := newProvider(t, mockClient, cfg, source, newLivenessCache(t, cfg))

	_, err := provider.Authenticate(t.Context(), "dead-on-arrival")
	require.ErrorIs(t, err, authn.ErrUnauthorized,
		"a tenant denied at first introspection must reject immediately")

	// A second call must not be served from a cached User: the rejection
	// path may cache the token, but the liveness check must fire on the
	// cached leg too and keep rejecting.
	_, err = provider.Authenticate(t.Context(), "dead-on-arrival")
	require.ErrorIs(t, err, authn.ErrUnauthorized,
		"the denial must hold on the cached-token path as well")
}

// Companion test confirming M2's fix did NOT relax the routine
// revocation path. A (false, nil) definite-no MUST still evict the
// cache and 401, otherwise the OnTenantDisabled NATS-eviction fast
// path would be the ONLY revocation mechanism (M8's collapsed-consumer
// bug would then be load-bearing).
func TestZitadelAuthProvider_Authenticate_SourceRevokeStillEvicts(t *testing.T) {
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
		// Short verdict TTL so it expires between calls and the source (now
		// reporting the tenant disabled) is consulted on the second request.
		// Without this the cached live verdict would mask the revocation
		// until liveness.DefaultTTL elapses; that TTL-bounded staleness
		// is covered separately by the verdict-cache tests.
		LivenessCacheTTL: testLivenessCacheTTL,
	}

	sourceLive := true
	source := func(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
		if sourceLive {
			return nil, nil
		}
		return ids, nil
	}

	provider := newProvider(t, mockClient, cfg, source, newLivenessCache(t, cfg))
	ctx := t.Context()

	// Warm cache.
	_, err := provider.Authenticate(ctx, "revoke-token")
	require.NoError(t, err)

	// The source flips to definite-no (the tenant was disabled).
	sourceLive = false

	// Let the cached live verdict expire so the source is re-consulted.
	time.Sleep(testLivenessCacheWait)

	// Second call must reject — this is a real revocation, not a fault.
	_, err = provider.Authenticate(ctx, "revoke-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized, "definite-no must reject")

	// Third call must re-introspect (cache was evicted on call 2).
	// We flip the source back to live to simulate recovery; if the token
	// cache wasn't evicted, this call would short-circuit on the stale
	// cache hit and the .Twice() expectation would fail. The verdict TTL has
	// to lapse again first: unlike the organization verdict it replaced,
	// a disabled verdict IS cached: the source is the tenant row, which
	// flips synchronously, so there is no window in which a cached negative
	// can outlive the truth it came from.
	sourceLive = true
	time.Sleep(testLivenessCacheWait)
	_, err = provider.Authenticate(ctx, "revoke-token")
	require.NoError(t, err)

	mockClient.AssertExpectations(t)
}

// newProvider builds a ZitadelAuthProvider for a test and closes it when
// the test ends; a construction error fails the test.
func newProvider(tb testing.TB, client zitadel.Client, cfg config.ZitadelConfig, source liveness.TenantSource, cache *liveness.Cache) *authn.ZitadelAuthProvider {
	tb.Helper()
	provider, err := authn.NewZitadelAuthProvider(client, cfg, source, cache)
	require.NoError(tb, err)
	tb.Cleanup(provider.Close)
	return provider
}

// newLivenessCache returns a liveness cache sized from cfg's verdict TTL
// (what main does) and closed with the test.
func newLivenessCache(t *testing.T, cfg config.ZitadelConfig) *liveness.Cache {
	t.Helper()
	// These tests assert verdicts, not the lookup bound. A cold lookup that
	// misses the one-second default on a starved -race runner fails the
	// request closed, and the test reads it as an unexplained unauthorized.
	c := liveness.NewCache(cfg.LivenessCacheTTL, liveness.WithLookupTimeout(time.Minute))
	t.Cleanup(c.Close)
	return c
}

// countingSource returns a liveness.TenantSource that records how many times
// it was invoked and reports every requested tenant as live or disabled. The
// counter lets tests prove the verdict cache short-circuits the source.
func countingSource(calls *atomic.Int64, live bool, err error) liveness.TenantSource {
	return func(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
		calls.Add(1)
		if err != nil {
			return nil, err
		}
		if live {
			return nil, nil
		}
		return ids, nil
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

// The tenant's liveness verdict is cached, so the source (a synchronous
// management round-trip) is NOT called on every request, only once per
// tenant per TTL window.
func TestZitadelAuthProvider_TenantVerdictCached(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "verdict-token").
		Return(activeIntrospection(), nil).Once()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour, // long enough to never expire mid-test
	}

	var calls atomic.Int64
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, true, nil), newLivenessCache(t, cfg))
	ctx := t.Context()

	const requests = 5
	for range requests {
		_, err := provider.Authenticate(ctx, "verdict-token")
		require.NoError(t, err)
	}

	assert.Equal(t, int64(1), calls.Load(),
		"source must be consulted once across %d requests; the rest hit the verdict cache", requests)
	mockClient.AssertExpectations(t) // introspected exactly once too
}

// A cached live verdict must be dropped the moment OnTenantDisabled fires
// (the NATS revocation fast-path), so the next request re-checks and catches
// the now-inactive tenant rather than serving the stale verdict until TTL.
func TestZitadelAuthProvider_TenantVerdictEvictedOnTenantDisabled(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "evict-token").
		Return(activeIntrospection(), nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour, // only eviction, not TTL, should clear it
	}

	var calls atomic.Int64
	cache := newLivenessCache(t, cfg)
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, true, nil), cache)
	ctx := t.Context()

	user, err := provider.Authenticate(ctx, "evict-token")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())

	// Second request is served from the verdict cache.
	_, err = provider.Authenticate(ctx, "evict-token")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load(), "verdict cache should short-circuit the source")

	// Tenant disabled via the NATS fast-path: the lifecycle handler evicts
	// the tenant verdict from the liveness cache and the token from the
	// provider's token cache.
	authn.NewTenantEventHandler(cache, provider)(authn.TenantEvent{
		TenantID: user.TenantID, Disabling: true,
	})

	_, err = provider.Authenticate(ctx, "evict-token")
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load(),
		"after OnTenantDisabled the verdict must be re-checked, not served from cache")
}

// When the verdict TTL elapses the source is consulted again: the bounded
// staleness backstop for the case where a disable's NATS event was missed.
//
// The body runs in a synctest bubble. The "still cached" check needs the
// second Authenticate to land within testLivenessCacheTTL of the first, and
// on the real clock a stalled CI runner can exceed that between two
// statements. The bubble's fake clock only advances while every goroutine
// in it is blocked, so no time passes between the two calls, and the
// time.Sleep below advances it by exactly testLivenessCacheWait. The
// provider and its caches are created inside the bubble so their cleanup
// tickers run on the same clock, and provider.Close is registered so the
// token cache's cleanup goroutine has exited when the bubble ends.
func TestZitadelAuthProvider_TenantVerdictExpiresAfterTTL(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		mockClient := mocks.NewMockZitadelClient()
		// Token cache TTL is long, so introspection happens exactly once even
		// though the verdict is re-evaluated after its own (short) TTL.
		mockClient.On("IntrospectToken", mock.Anything, "ttl-token").
			Return(activeIntrospection(), nil).Once()

		cfg := config.ZitadelConfig{
			ZitadelPATCacheTTL:        1 * time.Hour,
			ZitadelPATCacheTTLOverlap: 1 * time.Minute,
			LivenessCacheTTL:          testLivenessCacheTTL,
		}

		var calls atomic.Int64
		provider := newProvider(t, mockClient, cfg, countingSource(&calls, true, nil), newLivenessCache(t, cfg))
		ctx := t.Context()

		_, err := provider.Authenticate(ctx, "ttl-token")
		require.NoError(t, err)
		require.Equal(t, int64(1), calls.Load())

		// Immediately again: still cached.
		_, err = provider.Authenticate(ctx, "ttl-token")
		require.NoError(t, err)
		require.Equal(t, int64(1), calls.Load())

		// After the verdict TTL elapses the source runs again.
		time.Sleep(testLivenessCacheWait)
		_, err = provider.Authenticate(ctx, "ttl-token")
		require.NoError(t, err)
		assert.Equal(t, int64(2), calls.Load(), "verdict must be re-evaluated once its TTL expires")

		mockClient.AssertExpectations(t) // introspected once despite the re-validation
	})
}

// An infrastructure fault from the source must NOT be cached as a verdict;
// otherwise a transient blip would be remembered and service would not
// resume with the source: each faulted request is denied, and each
// subsequent request consults the source again. The TOKEN, however,
// IS cached on a fault rejection: without the entry every retry of the
// outage would re-introspect at Zitadel only to be refused again; the
// .Once() mock is the teeth for that.
func TestZitadelAuthProvider_TenantVerdictErrorNotCached(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "err-token").
		Return(activeIntrospection(), nil).Once()

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}

	var calls atomic.Int64
	// The source always faults: every request is denied (an unverifiable
	// tenant is not served), but the verdict itself must not stick.
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, false, assert.AnError), newLivenessCache(t, cfg))
	ctx := t.Context()

	_, err := provider.Authenticate(ctx, "err-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized, "an unverifiable tenant is not served")
	require.Equal(t, int64(1), calls.Load())

	// Because the fault was not cached, the next request consults the source
	// again rather than serving a stale (and wrong) cached verdict.
	_, err = provider.Authenticate(ctx, "err-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized)
	assert.Equal(t, int64(2), calls.Load(), "faulted verdict must not be cached")
}

// A provider wired without its tenant source or liveness cache would fail
// on the first authenticated request; the constructor refuses it at boot.
func TestNewZitadelAuthProvider_MissingLivenessDepsIsAnError(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	provider, err := authn.NewZitadelAuthProvider(mockClient, cfg, nil, newLivenessCache(t, cfg))
	require.ErrorIs(t, err, liveness.ErrMissingDeps)
	assert.Nil(t, provider)

	provider, err = authn.NewZitadelAuthProvider(mockClient, cfg, livenessmocks.NoDisabledTenants, nil)
	require.ErrorIs(t, err, liveness.ErrMissingDeps)
	assert.Nil(t, provider)
}

// An environment that still sets the former name of PYCK_LIVENESS_CACHE_TTL
// wants a non-default staleness bound the provider would silently ignore;
// the constructor refuses it at boot instead of panicking. Set but empty is
// not a setting and boots. Not parallel: t.Setenv changes the process
// environment.
func TestNewZitadelAuthProvider_RenamedLivenessCacheTTLIsAnError(t *testing.T) {
	mockClient := mocks.NewMockZitadelClient()
	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
	}

	t.Setenv("PYCK_ZITADEL_ORGANIZATION_CACHE_TTL", "30s")
	provider, err := authn.NewZitadelAuthProvider(mockClient, cfg, livenessmocks.NoDisabledTenants, newLivenessCache(t, cfg))
	require.ErrorIs(t, err, authn.ErrRenamedLivenessCacheTTL)
	require.ErrorContains(t, err, "PYCK_LIVENESS_CACHE_TTL")
	assert.Nil(t, provider)

	t.Setenv("PYCK_ZITADEL_ORGANIZATION_CACHE_TTL", "")
	provider, err = authn.NewZitadelAuthProvider(mockClient, cfg, livenessmocks.NoDisabledTenants, newLivenessCache(t, cfg))
	require.NoError(t, err)
	provider.Close()
}

// Boundary of the overlap-vs-TTL guard. overlap > TTL is nonsensical (every
// token computes a negative effective TTL, an eternal cache) and is refused
// at boot. overlap == TTL yields a zero effective TTL, which is a legitimate
// "disable caching, re-introspect every request" config and must boot fine:
// the Authenticate path skips the cache on ttl <= 0.
func TestNewZitadelAuthProvider_OverlapValidation(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	source := livenessmocks.NoDisabledTenants

	testCases := []struct {
		name      string
		ttl       time.Duration
		overlap   time.Duration
		expectErr bool
	}{
		{name: "overlap below TTL boots", ttl: 1 * time.Hour, overlap: 1 * time.Minute, expectErr: false},
		{name: "overlap equals TTL boots (zero effective TTL is intentional)", ttl: 1 * time.Minute, overlap: 1 * time.Minute, expectErr: false},
		{name: "overlap exceeds TTL is refused", ttl: 5 * time.Second, overlap: 1 * time.Minute, expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.ZitadelConfig{
				ZitadelPATCacheTTL:        tc.ttl,
				ZitadelPATCacheTTLOverlap: tc.overlap,
			}

			provider, err := authn.NewZitadelAuthProvider(mockClient, cfg, source, newLivenessCache(t, cfg))
			if tc.expectErr {
				require.ErrorIs(t, err, authn.ErrPATCacheOverlap, "overlap > TTL produces an eternal cache")
				assert.Nil(t, provider)
				return
			}
			require.NoError(t, err, "overlap <= TTL must boot: zero TTL disables caching, it is not an error")
			provider.Close()
		})
	}
}

// Authenticate must reject tokens whose introspection result has an
// empty `sub` claim before the User is cached. An empty sub hashes to one
// degenerate user ID shared by every such token, and the liveness check
// cannot catch it: that check asks about the token's tenant, which is live.
// So the rejection has to happen here or not at all.
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

	provider := newProvider(t, mockClient, cfg, func(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
		// The auth path must reject before the liveness gate runs.
		t.Errorf("the tenant source must not be called for an empty sub; got ids=%v", ids)
		return nil, nil
	}, newLivenessCache(t, cfg))

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

	provider := newProvider(t, mockClient, cfg, livenessmocks.NoDisabledTenants, newLivenessCache(t, cfg))

	user, err := provider.Authenticate(t.Context(), "sys-token")
	require.NoError(t, err)
	assert.True(t, user.IsSystemUser(),
		"ROLE_SYSTEM grant on the home org must upgrade to SystemUser regardless of claim/computed divergence")
}

// A ROLE_SYSTEM machine account must never reach the liveness source: it
// has no tenant scope, and the source itself is reached through a system
// token, so gating system traffic on a tenant verdict would make every
// inter-service call depend on the very lookup it serves. A denying source
// (definitive "tenant disabled") must therefore leave a system token
// authenticated.
func TestZitadelAuthProvider_Authenticate_SystemUserSkipsTenantLookup(t *testing.T) {
	t.Parallel()

	const homeOrgID = "home-org"

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "sys-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             "https://auth.example.com",
		Sub:             "sys-user",
		Username:        "system@example.com",
		ResourceOwnerID: homeOrgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"system": {
				homeOrgID: homeOrgID,
			},
		},
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}

	var calls atomic.Int64
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, false, nil), newLivenessCache(t, cfg))

	user, err := provider.Authenticate(t.Context(), "sys-token")
	require.NoError(t, err, "a definitive tenant revocation must not reach a system token")
	assert.True(t, user.IsSystemUser())
	assert.Zero(t, calls.Load(), "a system user must never reach the liveness source")
}

// Organizations Zitadel minted (numeric) and organizations pyck now mints
// (uuid v7) reach this code the same way: as the token's resource owner and
// as the keys of the project-roles claim. Both must resolve to the tenant
// UUID their rows already carry, so the expectations are literal: deriving
// them here with ComputeUUID would assert the code against itself and pass
// through any change to the derivation.
func TestZitadelAuthProvider_Authenticate_MintedAndChosenOrgIDs(t *testing.T) {
	t.Parallel()

	const (
		issuer = "http://localhost:8080"
		// The user's home org, created before pyck chose organization IDs.
		legacyOrgID    = "375900000000000001"
		legacyTenantID = "98fb7af5-2cbf-5f13-aa8c-6b98e9aae0a0"
		// A tenant registered by the DB-first flow, which the same user was
		// granted into.
		chosenOrgID    = "0199d7d0-1111-7000-8000-000000000001"
		chosenTenantID = "00a157f4-f68c-5081-b51a-a787b7a4068a"
	)

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "mixed-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             issuer,
		Sub:             "user123",
		Username:        "admin@example.com",
		ResourceOwnerID: legacyOrgID,
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles: map[string]map[string]string{
			"admin":  {legacyOrgID: legacyOrgID},
			"writer": {chosenOrgID: chosenOrgID},
		},
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}
	provider := newProvider(t, mockClient, cfg, livenessmocks.NoDisabledTenants, newLivenessCache(t, cfg))

	user, err := provider.Authenticate(t.Context(), "mixed-token")
	require.NoError(t, err)

	assert.Equal(t, legacyTenantID, user.TenantID.String(), "an existing tenant keeps the id its row carries")
	assert.Equal(t, authn.ROLE_ADMIN, user.Roles[uuid.MustParse(legacyTenantID)])
	assert.Equal(t, authn.ROLE_WRITER, user.Roles[uuid.MustParse(chosenTenantID)],
		"a uuid organization id keys the roles map the same way a numeric one does")
	assert.Len(t, user.Roles, 2)
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

	cache := liveness.NewCache(config.LivenessCacheTTL)
	b.Cleanup(cache.Close)
	provider := newProvider(b, mockClient, config, livenessmocks.NoDisabledTenants, cache)
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

// The liveness verdict belongs to the tenant, not to the user who happened
// to trigger the lookup: every user homed in a tenant shares one entry. The
// source's signature is what makes the alternative unrepresentable (it is
// handed tenant IDs and never sees the caller) so what is left to pin is
// the sharing itself: a second user of the same tenant, authenticating once
// the entry exists, must not reach the source at all.
func TestZitadelAuthProvider_Authenticate_TenantVerdictIsSharedByItsUsers(t *testing.T) {
	t.Parallel()

	const (
		issuer = "https://auth.example.com"
		orgID  = "shared-org"
		subA   = "user-a"
		subB   = "user-b"
	)

	introspection := func(sub string) *zitadel.IntrospectionResult {
		return &zitadel.IntrospectionResult{
			Active:          true,
			Iss:             issuer,
			Sub:             sub,
			Username:        sub + "@example.com",
			ResourceOwnerID: orgID,
			Exp:             time.Now().Add(1 * time.Hour).Unix(),
			ProjectRoles:    map[string]map[string]string{"writer": {orgID: orgID}},
		}
	}

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "token-a").Return(introspection(subA), nil)
	mockClient.On("IntrospectToken", mock.Anything, "token-b").Return(introspection(subB), nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}

	var calls atomic.Int64
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, true, nil), newLivenessCache(t, cfg))

	_, err := provider.Authenticate(t.Context(), "token-a")
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load(), "the first user of the tenant fills the entry")

	_, err = provider.Authenticate(t.Context(), "token-b")
	require.NoError(t, err)
	assert.Equal(t, int64(1), calls.Load(),
		"a second user of the same tenant must be answered from the entry the first one filled")
}

// A token that introspects without a resource owner still names a tenant
// through the webhook-injected claim, and that tenant is what the liveness
// check asks about. Before the check read the tenant row it asked Zitadel
// about the organization, had none to ask about, and admitted the request,
// leaving such a token outside the axis until the tenant middleware ran, and
// outside it entirely on the paths that have no middleware (the NATS auth
// callout, the Temporal claim mapper).
func TestZitadelAuthProvider_Authenticate_NoResourceOwnerIsCheckedByItsTenantClaim(t *testing.T) {
	t.Parallel()

	claimTenant := uuid.New()
	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "no-org-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             "https://auth.example.com",
		Sub:             "orphan-user",
		Username:        "orphan@example.com",
		ResourceOwnerID: "",
		PyckTenantID:    claimTenant.String(),
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles:    map[string]map[string]string{"writer": {"some-org": "some-org"}},
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}

	var asked [][]uuid.UUID
	provider := newProvider(t, mockClient, cfg,
		func(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
			asked = append(asked, ids)
			return nil, nil
		}, newLivenessCache(t, cfg))

	user, err := provider.Authenticate(t.Context(), "no-org-token")
	require.NoError(t, err, "the claimed tenant is live, so the token is admitted")
	assert.Equal(t, "orphan-user", user.Sub)
	assert.Equal(t, claimTenant, user.TenantID)
	assert.Equal(t, [][]uuid.UUID{{claimTenant}}, asked,
		"the tenant claim is the key the liveness check asks about")
}

// A token carrying neither a resource owner nor a tenant claim is outside
// both liveness axes at once: the org gate has no organization to ask about,
// and every such token would share the synthetic ComputeUUID(iss, "") tenant
// that matches no tenant row, so a disabled home tenant would never deny it.
// It is refused at introspection rather than admitted unscoped.
func TestZitadelAuthProvider_Authenticate_NoResourceOwnerNoTenantClaimRejected(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "unscopable-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             "https://auth.example.com",
		Sub:             "orphan-user",
		Username:        "orphan@example.com",
		ResourceOwnerID: "",
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
		ProjectRoles:    map[string]map[string]string{"writer": {"some-org": "some-org"}},
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}

	var calls atomic.Int64
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, true, nil), newLivenessCache(t, cfg))

	_, err := provider.Authenticate(t.Context(), "unscopable-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized,
		"a token no liveness axis can bite must not be admitted")
	assert.Equal(t, int64(0), calls.Load(),
		"the rejection happens before the org gate; there is no organization to ask about")
}

// A malformed tenant claim is not a usable tenant, so a token that also
// lacks a resource owner is refused for the same reason as one carrying no
// claim at all — the fallback would land on the synthetic
// ComputeUUID(iss, "") tenant.
func TestZitadelAuthProvider_Authenticate_MalformedTenantClaimNoResourceOwnerRejected(t *testing.T) {
	t.Parallel()

	mockClient := mocks.NewMockZitadelClient()
	mockClient.On("IntrospectToken", mock.Anything, "bad-claim-token").Return(&zitadel.IntrospectionResult{
		Active:          true,
		Iss:             "https://auth.example.com",
		Sub:             "orphan-user",
		ResourceOwnerID: "",
		PyckTenantID:    "not-a-uuid",
		Exp:             time.Now().Add(1 * time.Hour).Unix(),
	}, nil)

	cfg := config.ZitadelConfig{
		ZitadelPATCacheTTL:        1 * time.Hour,
		ZitadelPATCacheTTLOverlap: 1 * time.Minute,
		LivenessCacheTTL:          1 * time.Hour,
	}

	var calls atomic.Int64
	provider := newProvider(t, mockClient, cfg, countingSource(&calls, true, nil), newLivenessCache(t, cfg))

	_, err := provider.Authenticate(t.Context(), "bad-claim-token")
	require.ErrorIs(t, err, authn.ErrUnauthorized,
		"an unparseable claim leaves the token as unscopable as a missing one")
}

// The system-identity upgrade anchors on the token's home org, not on the
// configured platform organization, and a home org other than the root one
// only trips a log line: enforcement waits until every system PAT's home
// org is confirmed (see the tripwire comment at the upgrade site). These
// tests pin both halves: silence for the expected shape, an Error line
// (with the upgrade still granted) for the unexpected one.
func TestZitadelAuthProvider_Authenticate_SystemUpgradeTripwire(t *testing.T) {
	t.Parallel()

	const (
		issuer    = "https://auth.example.com"
		rootOrgID = "root-org"

		tripwireMsg = "system-role token homed outside the configured platform organization"
	)

	cfg := config.ZitadelConfig{
		// Audience == issuer so the configured system tenant
		// (ComputeUUID(audience, org)) matches the home-org-derived key
		// (ComputeUUID(iss, resource owner)) for a root-org-homed PAT.
		ZitadelAudience:           issuer,
		ZitadelOrganizationId:     rootOrgID,
		ZitadelPATCacheTTL:        time.Hour,
		ZitadelPATCacheTTLOverlap: time.Minute,
	}

	testCases := []struct {
		name         string
		homeOrgID    string
		wantTripwire bool
	}{
		{name: "HomedInRootOrg_NoTripwire", homeOrgID: rootOrgID, wantTripwire: false},
		{name: "HomedElsewhere_TripwireStillUpgrades", homeOrgID: "another-org", wantTripwire: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockClient := mocks.NewMockZitadelClient()
			mockClient.On("IntrospectToken", mock.Anything, "sys-token").Return(&zitadel.IntrospectionResult{
				Active:          true,
				Iss:             issuer,
				Sub:             "sys-user",
				Username:        "system@example.com",
				ResourceOwnerID: tc.homeOrgID,
				Exp:             time.Now().Add(time.Hour).Unix(),
				ProjectRoles: map[string]map[string]string{
					"system": {tc.homeOrgID: tc.homeOrgID},
				},
			}, nil)

			provider := newProvider(t, mockClient, cfg, livenessmocks.NoDisabledTenants, newLivenessCache(t, cfg))

			var buf bytes.Buffer
			ctx := log.Context(t.Context(), zerolog.New(&buf))

			user, err := provider.Authenticate(ctx, "sys-token")
			require.NoError(t, err)
			assert.True(t, user.IsSystemUser(), "the upgrade must be granted either way: the tripwire only logs")

			logged := buf.String()
			if !tc.wantTripwire {
				assert.NotContains(t, logged, tripwireMsg)
				return
			}
			assert.Contains(t, logged, tripwireMsg)
			assert.Contains(t, logged, `"sub":"sys-user"`)
			assert.Contains(t, logged, authn.ComputeUUID(issuer, tc.homeOrgID).String(), "the line must name the token's home tenant")
			assert.Contains(t, logged, authn.ComputeUUID(cfg.ZitadelAudience, rootOrgID).String(), "the line must name the configured system tenant")
		})
	}
}
