package authz_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/temporal/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/server/common/authorization"
)

func TestClaimMapper_AuthInfoRequired(t *testing.T) {
	ctx := t.Context()
	mockAuth := &mocks.MockAuthProvider{}
	mapper := authz.NewClaimMapper(ctx, mockAuth)

	require.True(t, mapper.AuthInfoRequired())
}

func TestClaimMapper_GetClaims(t *testing.T) {
	tenant1 := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")
	tenant2 := uuid.MustParse("acdc84a1-6457-44b3-a1e3-f2e7ae8cb380")
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")

	testCases := []struct {
		name           string
		authInfo       *authorization.AuthInfo
		mockSetup      func(*mocks.MockAuthProvider)
		expectError    bool
		errorContains  string
		validateClaims func(*testing.T, *authorization.Claims)
	}{
		{
			name:          "NoAuthInfo_ReturnsError",
			authInfo:      nil,
			mockSetup:     func(m *mocks.MockAuthProvider) {},
			expectError:   true,
			errorContains: "no auth info",
		},
		{
			name: "EmptyAuthToken_ReturnsError",
			authInfo: &authorization.AuthInfo{
				AuthToken: "",
			},
			mockSetup:     func(m *mocks.MockAuthProvider) {},
			expectError:   true,
			errorContains: "no auth info",
		},
		{
			name: "InvalidToken_ReturnsError",
			authInfo: &authorization.AuthInfo{
				AuthToken: "Bearer invalid-token",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				m.On("Authenticate", mock.Anything, "invalid-token").Return(authn.User{}, assert.AnError)
			},
			expectError:   true,
			errorContains: "authentication failed",
		},
		{
			name: "UnauthenticatedUser_ReturnsError",
			authInfo: &authorization.AuthInfo{
				AuthToken: "Bearer valid-token",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				m.On("Authenticate", mock.Anything, "valid-token").Return(authn.User{}, nil)
			},
			expectError:   true,
			errorContains: "authentication failed",
		},
		{
			name: "ValidToken_RegularUser_WithReaderRole",
			authInfo: &authorization.AuthInfo{
				AuthToken: "Bearer valid-token",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				user := authn.User{
					ID:       userID,
					TenantID: tenant1,
					Username: "test-user",
					Roles: map[uuid.UUID]authn.Role{
						tenant1: authn.ROLE_READER,
					},
				}
				m.On("Authenticate", mock.Anything, "valid-token").Return(user, nil)
			},
			expectError: false,
			validateClaims: func(t *testing.T, claims *authorization.Claims) {
				t.Helper()
				assert.Equal(t, userID.String(), claims.Subject)
				assert.Equal(t, authorization.RoleUndefined, claims.System)
				require.Len(t, claims.Namespaces, 1)
				assert.Equal(t, authorization.RoleReader, claims.Namespaces[tenant1.String()])

				ext, ok := claims.Extensions.(*authz.ClaimMapperExtensions)
				require.True(t, ok)
				assert.Equal(t, userID, ext.User.ID)
			},
		},
		{
			name: "ValidToken_RegularUser_WithWriterRole",
			authInfo: &authorization.AuthInfo{
				AuthToken: "valid-token",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				user := authn.User{
					ID:       userID,
					TenantID: tenant1,
					Username: "test-user",
					Roles: map[uuid.UUID]authn.Role{
						tenant1: authn.ROLE_WRITER,
					},
				}
				m.On("Authenticate", mock.Anything, "valid-token").Return(user, nil)
			},
			expectError: false,
			validateClaims: func(t *testing.T, claims *authorization.Claims) {
				t.Helper()
				assert.Equal(t, userID.String(), claims.Subject)
				assert.Equal(t, authorization.RoleUndefined, claims.System)
				require.Len(t, claims.Namespaces, 1)
				assert.Equal(t, authorization.RoleReader|authorization.RoleWriter, claims.Namespaces[tenant1.String()])
			},
		},
		{
			name: "ValidToken_RegularUser_WithAdminRole",
			authInfo: &authorization.AuthInfo{
				AuthToken: "  Bearer  valid-token  ",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				user := authn.User{
					ID:       userID,
					TenantID: tenant1,
					Username: "test-user",
					Roles: map[uuid.UUID]authn.Role{
						tenant1: authn.ROLE_ADMIN,
					},
				}
				m.On("Authenticate", mock.Anything, "valid-token").Return(user, nil)
			},
			expectError: false,
			validateClaims: func(t *testing.T, claims *authorization.Claims) {
				t.Helper()
				assert.Equal(t, userID.String(), claims.Subject)
				assert.Equal(t, authorization.RoleUndefined, claims.System)
				require.Len(t, claims.Namespaces, 1)
				assert.Equal(t, authorization.RoleReader|authorization.RoleWriter|authorization.RoleAdmin, claims.Namespaces[tenant1.String()])
			},
		},
		{
			name: "ValidToken_RegularUser_MultipleRoles",
			authInfo: &authorization.AuthInfo{
				AuthToken: "Bearer valid-token",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				user := authn.User{
					ID:       userID,
					TenantID: tenant1,
					Username: "test-user",
					Roles: map[uuid.UUID]authn.Role{
						tenant1: authn.ROLE_READER,
						tenant2: authn.ROLE_ADMIN,
					},
				}
				m.On("Authenticate", mock.Anything, "valid-token").Return(user, nil)
			},
			expectError: false,
			validateClaims: func(t *testing.T, claims *authorization.Claims) {
				t.Helper()
				assert.Equal(t, userID.String(), claims.Subject)
				assert.Equal(t, authorization.RoleUndefined, claims.System)
				require.Len(t, claims.Namespaces, 2)
				assert.Equal(t, authorization.RoleReader, claims.Namespaces[tenant1.String()])
				assert.Equal(t, authorization.RoleReader|authorization.RoleWriter|authorization.RoleAdmin, claims.Namespaces[tenant2.String()])
			},
		},
		{
			name: "ValidToken_SystemUser",
			authInfo: &authorization.AuthInfo{
				AuthToken: "Bearer system-token",
			},
			mockSetup: func(m *mocks.MockAuthProvider) {
				user := authn.User{
					ID:       uuid.Max,
					TenantID: uuid.Max,
					Username: "system",
					Roles:    map[uuid.UUID]authn.Role{},
				}
				m.On("Authenticate", mock.Anything, "system-token").Return(user, nil)
			},
			expectError: false,
			validateClaims: func(t *testing.T, claims *authorization.Claims) {
				t.Helper()
				assert.Equal(t, uuid.Max.String(), claims.Subject)
				assert.Equal(t, authorization.RoleReader|authorization.RoleWriter, claims.System)
				require.Len(t, claims.Namespaces, 0)

				ext, ok := claims.Extensions.(*authz.ClaimMapperExtensions)
				require.True(t, ok)
				assert.True(t, ext.User.IsSystemUser())
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			mockAuth := &mocks.MockAuthProvider{}
			tc.mockSetup(mockAuth)

			mapper := authz.NewClaimMapper(ctx, mockAuth)

			claims, err := mapper.GetClaims(tc.authInfo)

			if tc.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
				require.NotNil(t, claims)
				tc.validateClaims(t, claims)
			}

			mockAuth.AssertExpectations(t)
		})
	}
}

func TestTemporalRole(t *testing.T) {
	testCases := []struct {
		pyckRole     authn.Role
		temporalRole authorization.Role
	}{
		{
			pyckRole:     authn.ROLE_NONE,
			temporalRole: authorization.RoleUndefined,
		},
		{
			pyckRole:     authn.ROLE_READER,
			temporalRole: authorization.RoleReader,
		},
		{
			pyckRole:     authn.ROLE_WRITER,
			temporalRole: authorization.RoleReader | authorization.RoleWriter,
		},
		{
			pyckRole:     authn.ROLE_ADMIN,
			temporalRole: authorization.RoleReader | authorization.RoleWriter | authorization.RoleAdmin,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.pyckRole.String(), func(t *testing.T) {
			result := authz.TemporalRole(tc.pyckRole)
			assert.Equal(t, tc.temporalRole, result)
		})
	}
}

func TestPyckRole(t *testing.T) {
	testCases := []struct {
		name         string
		temporalRole authorization.Role
		pyckRole     authn.Role
	}{
		{
			name:         "Undefined",
			temporalRole: authorization.RoleUndefined,
			pyckRole:     authn.ROLE_NONE,
		},
		{
			name:         "Reader",
			temporalRole: authorization.RoleReader,
			pyckRole:     authn.ROLE_READER,
		},
		{
			name:         "Writer",
			temporalRole: authorization.RoleReader | authorization.RoleWriter,
			pyckRole:     authn.ROLE_WRITER,
		},
		{
			name:         "Admin",
			temporalRole: authorization.RoleReader | authorization.RoleWriter | authorization.RoleAdmin,
			pyckRole:     authn.ROLE_ADMIN,
		},
		{
			name:         "WriterOnly_WithoutReader",
			temporalRole: authorization.RoleWriter,
			pyckRole:     authn.ROLE_WRITER,
		},
		{
			name:         "AdminOnly_WithoutReaderWriter",
			temporalRole: authorization.RoleAdmin,
			pyckRole:     authn.ROLE_ADMIN,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := authz.PyckRole(tc.temporalRole)
			assert.Equal(t, tc.pyckRole, result)
		})
	}
}
