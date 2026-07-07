package authn_test

import (
	"context"
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
