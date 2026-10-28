package authz_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
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
	mapper, err := authz.NewClaimMapper(ctx, mockAuth, livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants, 2*time.Second)
	require.NoError(t, err)

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

			mapper, err := authz.NewClaimMapper(ctx, mockAuth, livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants, 2*time.Second)
			require.NoError(t, err)

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

// authenticateAs wires a MockAuthProvider that answers "user-token" with
// user, the fixture of the namespace-liveness tests below.
func authenticateAs(user authn.User) *mocks.MockAuthProvider {
	mockAuth := &mocks.MockAuthProvider{}
	mockAuth.On("Authenticate", mock.Anything, "user-token").Return(user, nil)
	return mockAuth
}

// A role grant into a tenant the source reports as not served must not map
// to a Temporal namespace: the grant may outlive the tenant row (stale
// Zitadel grant into a soft-deleted or never-registered tenant), and the
// namespace's workflow histories and signals must go with the row.
func TestClaimMapper_GetClaims_FiltersDeadTenantNamespaces(t *testing.T) {
	t.Parallel()

	liveTenant := uuid.New()
	deadTenant := uuid.New()

	mockAuth := authenticateAs(authn.User{
		ID:       uuid.New(),
		TenantID: liveTenant,
		Roles: map[uuid.UUID]authn.Role{
			liveTenant: authn.ROLE_READER,
			deadTenant: authn.ROLE_ADMIN,
		},
	})
	source := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{deadTenant: {}}}
	mapper, err := authz.NewClaimMapper(t.Context(), mockAuth, livenessmocks.NewTestCache(t), source.Lookup, 2*time.Second)
	require.NoError(t, err)

	claims, err := mapper.GetClaims(&authorization.AuthInfo{AuthToken: "Bearer user-token"})
	require.NoError(t, err)
	require.Len(t, claims.Namespaces, 1)
	assert.Equal(t, authorization.RoleReader, claims.Namespaces[liveTenant.String()])
	assert.NotContains(t, claims.Namespaces, deadTenant.String())
}

// A source fault denies namespaces with no cached verdict: an unverifiable
// tenant mints no claim; failing open would reopen a deleted tenant's
// workflow histories for the length of a source outage. Authentication
// itself still succeeds; the claims simply carry no namespaces.
func TestClaimMapper_GetClaims_SourceErrorDeniesUncached(t *testing.T) {
	t.Parallel()

	tenant1 := uuid.New()
	tenant2 := uuid.New()

	mockAuth := authenticateAs(authn.User{
		ID:       uuid.New(),
		TenantID: tenant1,
		Roles: map[uuid.UUID]authn.Role{
			tenant1: authn.ROLE_READER,
			tenant2: authn.ROLE_WRITER,
		},
	})
	source := &livenessmocks.Source{Err: assert.AnError}
	mapper, err := authz.NewClaimMapper(t.Context(), mockAuth, livenessmocks.NewTestCache(t), source.Lookup, 2*time.Second)
	require.NoError(t, err)

	claims, err := mapper.GetClaims(&authorization.AuthInfo{AuthToken: "Bearer user-token"})
	require.NoError(t, err)
	assert.Empty(t, claims.Namespaces,
		"no namespace claim may be minted for a tenant the mapper cannot verify")
}

// Cached verdicts of both signs keep applying while the source is down: a
// tenant this replica verified stays claimable for its TTL, a tenant it
// knows is dead stays filtered, and an unverifiable one mints nothing;
// the fault must neither reopen a revoked namespace nor revoke a verified
// one.
func TestClaimMapper_GetClaims_CachedVerdictsHoldThroughFault(t *testing.T) {
	t.Parallel()

	cachedLive := uuid.New()
	cachedDead := uuid.New()
	unknown := uuid.New()

	source := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{cachedDead: {}}}
	cache := livenessmocks.NewTestCache(t)
	_, err := cache.LookupTenants(t.Context(), []uuid.UUID{cachedLive, cachedDead}, source.Lookup)
	require.NoError(t, err)
	source.Err = assert.AnError

	mockAuth := authenticateAs(authn.User{
		ID:       uuid.New(),
		TenantID: cachedLive,
		Roles: map[uuid.UUID]authn.Role{
			cachedLive: authn.ROLE_READER,
			cachedDead: authn.ROLE_ADMIN,
			unknown:    authn.ROLE_WRITER,
		},
	})
	mapper, err := authz.NewClaimMapper(t.Context(), mockAuth, cache, source.Lookup, 2*time.Second)
	require.NoError(t, err)

	claims, err := mapper.GetClaims(&authorization.AuthInfo{AuthToken: "Bearer user-token"})
	require.NoError(t, err)
	require.Len(t, claims.Namespaces, 1)
	assert.Equal(t, authorization.RoleReader, claims.Namespaces[cachedLive.String()])
	assert.NotContains(t, claims.Namespaces, cachedDead.String())
	assert.NotContains(t, claims.Namespaces, unknown.String())
}

// System users bypass the liveness filter: their namespace map is built
// exactly as it always was and the source is never consulted; the source
// is itself reached through a system token, so gating system traffic on it
// would make every inter-service call depend on the probe it serves.
func TestClaimMapper_GetClaims_SystemUserSkipsLivenessFilter(t *testing.T) {
	t.Parallel()

	deadTenant := uuid.New()

	mockAuth := authenticateAs(authn.User{
		ID:       uuid.Max,
		TenantID: uuid.Max,
		Username: "system",
		Roles: map[uuid.UUID]authn.Role{
			deadTenant: authn.ROLE_ADMIN,
		},
	})
	source := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{deadTenant: {}}}
	mapper, err := authz.NewClaimMapper(t.Context(), mockAuth, livenessmocks.NewTestCache(t), source.Lookup, 2*time.Second)
	require.NoError(t, err)

	claims, err := mapper.GetClaims(&authorization.AuthInfo{AuthToken: "Bearer user-token"})
	require.NoError(t, err)
	require.Len(t, claims.Namespaces, 1)
	assert.Equal(t, authorization.RoleReader|authorization.RoleWriter|authorization.RoleAdmin, claims.Namespaces[deadTenant.String()])
	assert.Zero(t, source.Calls(), "a system user's claims must never consult the tenant source")
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

// Temporal hands the claim mapper no context of its own (the ClaimMapper
// interface passes only *authorization.AuthInfo) so the mapping runs on the
// process context, which nothing ever cancels. Without a deadline of its own
// a dependency that accepts the connection and then stalls holds one frontend
// goroutine per request, on the auth path of every gRPC call the public
// frontend serves. The introspection client sets no timeout and the request
// it builds is context-aware, so the mapper's own deadline is the only thing
// that ends the call.
func TestClaimMapper_GetClaims_StalledDependencyIsBounded(t *testing.T) {
	t.Parallel()

	mockAuth := &mocks.MockAuthProvider{}
	mockAuth.On("Authenticate", mock.Anything, "user-token").
		Run(func(args mock.Arguments) {
			callCtx, ok := args.Get(0).(context.Context)
			require.True(t, ok, "the mapper must hand Authenticate a context")
			_, hasDeadline := callCtx.Deadline()
			require.True(t, hasDeadline,
				"the mapper must bound a call nothing else can cancel: its context is the process context")

			select {
			case <-callCtx.Done():
			case <-time.After(30 * time.Second):
				// Unreached while the deadline above holds; the arm exists so
				// a regression fails the elapsed assertion instead of hanging
				// the suite forever.
			}
		}).
		Return(authn.User{}, context.DeadlineExceeded)

	mapper, err := authz.NewClaimMapper(t.Context(), mockAuth, livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants, 2*time.Second)
	require.NoError(t, err)

	start := time.Now()
	_, err = mapper.GetClaims(&authorization.AuthInfo{AuthToken: "Bearer user-token"})
	waited := time.Since(start)

	require.Error(t, err, "a stalled dependency must refuse, not hang")
	assert.Less(t, waited, 20*time.Second,
		"a stalled dependency must not pin a frontend goroutine for the life of the process")
}
