package authn_test

import (
	"net/http"
	"net/http/httptest"
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)

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

			provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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

			provider := authn.NewZitadelAuthProvider(mockClient, config)
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

			provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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

			provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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

	provider := authn.NewZitadelAuthProvider(mockClient, config)
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
