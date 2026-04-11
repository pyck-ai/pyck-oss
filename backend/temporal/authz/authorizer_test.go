package authz_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/temporal/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/server/common/api"
	"go.temporal.io/server/common/authorization"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestNewAuthorizer(t *testing.T) {
	ctx := t.Context()

	t.Run("WithNilBaseAuthorizer", func(t *testing.T) {
		auth := authz.NewAuthorizer(ctx, nil)
		require.NotNil(t, auth)
	})

	t.Run("WithBaseAuthorizer", func(t *testing.T) {
		baseAuth := authorization.NewNoopAuthorizer()
		auth := authz.NewAuthorizer(ctx, baseAuth)
		require.NotNil(t, auth)
	})
}

func TestAuthorizer_PublicAPIs(t *testing.T) {
	ctx := t.Context()
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")
	auth := authz.NewAuthorizer(ctx, nil)

	testCases := []struct {
		name           string
		api            string
		claims         *authorization.Claims
		expectedReason string
	}{
		{
			name: "HealthCheck_WithExtensions",
			api:  grpc_health_v1.Health_Check_FullMethodName,
			claims: &authorization.Claims{
				Subject: userID.String(),
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "test-user",
					},
				},
			},
			expectedReason: "API is public",
		},
		{
			name: "GetSystemInfo_WithExtensions",
			api:  api.WorkflowServicePrefix + "GetSystemInfo",
			claims: &authorization.Claims{
				Subject: userID.String(),
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "test-user",
					},
				},
			},
			expectedReason: "API is public",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			target := &authorization.CallTarget{
				APIName: tc.api,
			}

			result, err := auth.Authorize(ctx, tc.claims, target)

			require.NoError(t, err)
			assert.Equal(t, authorization.DecisionAllow, result.Decision)
			assert.Equal(t, tc.expectedReason, result.Reason)
		})
	}
}

func TestAuthorizer_DisabledAPIs(t *testing.T) {
	ctx := t.Context()
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")

	auth := authz.NewAuthorizer(ctx, nil)

	testCases := []struct {
		name string
		api  string
	}{
		{
			name: "RegisterNamespace",
			api:  api.WorkflowServicePrefix + "RegisterNamespace",
		},
		{
			name: "UpdateNamespace",
			api:  api.WorkflowServicePrefix + "UpdateNamespace",
		},
		{
			name: "DeprecateNamespace",
			api:  api.WorkflowServicePrefix + "DeprecateNamespace",
		},
		{
			name: "DeleteNamespace",
			api:  api.OperatorServicePrefix + "DeleteNamespace",
		},
		{
			name: "NexusService",
			api:  api.NexusServicePrefix,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test with authenticated user
			claims := &authorization.Claims{
				Subject: userID.String(),
				System:  authorization.RoleReader | authorization.RoleWriter | authorization.RoleAdmin,
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "admin-user",
					},
				},
			}

			target := &authorization.CallTarget{
				APIName: tc.api,
			}

			result, err := auth.Authorize(ctx, claims, target)

			require.NoError(t, err)
			assert.Equal(t, authorization.DecisionDeny, result.Decision)
			assert.Equal(t, "API is disabled", result.Reason)
		})
	}
}

func TestAuthorizer_AuthenticatedAPIs(t *testing.T) {
	ctx := t.Context()
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")

	auth := authz.NewAuthorizer(ctx, nil)

	authenticatedAPIs := []string{
		api.WorkflowServicePrefix + "GetClusterInfo",
		api.WorkflowServicePrefix + "GetSearchAttributes",
		api.WorkflowServicePrefix + "ListNamespaces",
	}

	for _, apiName := range authenticatedAPIs {
		t.Run(apiName, func(t *testing.T) {
			t.Run("Unauthenticated_Denied", func(t *testing.T) {
				target := &authorization.CallTarget{
					APIName: apiName,
				}

				// Test with unauthenticated user (has extensions but not authenticated)
				claims := &authorization.Claims{
					Extensions: &authz.ClaimMapperExtensions{
						User: authn.User{}, // Not authenticated
					},
				}
				result, err := auth.Authorize(ctx, claims, target)
				require.NoError(t, err)
				assert.Equal(t, authorization.DecisionDeny, result.Decision)
				assert.Equal(t, "User is not authenticated", result.Reason)
			})

			t.Run("Authenticated_Allowed", func(t *testing.T) {
				claims := &authorization.Claims{
					Subject: userID.String(),
					Extensions: &authz.ClaimMapperExtensions{
						User: authn.User{
							ID:       userID,
							TenantID: tenantID,
							Username: "test-user",
						},
					},
				}

				target := &authorization.CallTarget{
					APIName: apiName,
				}

				result, err := auth.Authorize(ctx, claims, target)

				require.NoError(t, err)
				assert.Equal(t, authorization.DecisionAllow, result.Decision)
				assert.Equal(t, "API has no role requirement", result.Reason)
			})
		})
	}
}

func TestAuthorizer_InternalCalls(t *testing.T) {
	ctx := t.Context()
	baseAuth := authorization.NewDefaultAuthorizer()
	auth := authz.NewAuthorizer(ctx, baseAuth)

	t.Run("NoExtensions_FallbackToBaseAuthorizer", func(t *testing.T) {
		// Claims without extensions simulate internal-frontend calls
		claims := &authorization.Claims{
			Subject: "internal",
			System:  authorization.RoleReader | authorization.RoleWriter,
		}

		target := &authorization.CallTarget{
			APIName:   api.WorkflowServicePrefix + "StartWorkflowExecution",
			Namespace: "test-namespace",
		}

		result, err := auth.Authorize(ctx, claims, target)

		require.NoError(t, err)
		// Base authorizer behavior - should allow with system role
		assert.Equal(t, authorization.DecisionAllow, result.Decision)
	})
}

func TestAuthorizer_NamespaceRoles(t *testing.T) {
	ctx := t.Context()
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")
	namespace := tenantID.String()

	// Use nil base authorizer to test only our custom logic
	auth := authz.NewAuthorizer(ctx, nil)

	testCases := []struct {
		name           string
		userRole       authn.Role
		temporalRole   authorization.Role
		apiName        string
		expectedResult authorization.Decision
		expectedReason string
	}{
		{
			name:           "NoRole_FallsBackToBase",
			userRole:       authn.ROLE_NONE,
			temporalRole:   authorization.RoleUndefined,
			apiName:        api.WorkflowServicePrefix + "StartWorkflowExecution",
			expectedResult: authorization.DecisionAllow, // NoopAuthorizer allows everything
			expectedReason: "",
		},
		{
			name:           "Reader_Allowed",
			userRole:       authn.ROLE_READER,
			temporalRole:   authorization.RoleReader,
			apiName:        api.WorkflowServicePrefix + "DescribeWorkflowExecution",
			expectedResult: authorization.DecisionAllow,
			expectedReason: "",
		},
		{
			name:           "Writer_Allowed",
			userRole:       authn.ROLE_WRITER,
			temporalRole:   authorization.RoleReader | authorization.RoleWriter,
			apiName:        api.WorkflowServicePrefix + "StartWorkflowExecution",
			expectedResult: authorization.DecisionAllow,
			expectedReason: "",
		},
		{
			name:           "Admin_Allowed",
			userRole:       authn.ROLE_ADMIN,
			temporalRole:   authorization.RoleReader | authorization.RoleWriter | authorization.RoleAdmin,
			apiName:        api.WorkflowServicePrefix + "StartWorkflowExecution",
			expectedResult: authorization.DecisionAllow,
			expectedReason: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			claims := &authorization.Claims{
				Subject: userID.String(),
				Namespaces: map[string]authorization.Role{
					namespace: tc.temporalRole,
				},
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "test-user",
						Roles: map[uuid.UUID]authn.Role{
							tenantID: tc.userRole,
						},
					},
				},
			}

			target := &authorization.CallTarget{
				APIName:   tc.apiName,
				Namespace: namespace,
			}

			result, err := auth.Authorize(ctx, claims, target)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedResult, result.Decision)
			if tc.expectedReason != "" {
				assert.Contains(t, result.Reason, tc.expectedReason)
			}
		})
	}
}

func TestAuthorizer_SystemRole(t *testing.T) {
	ctx := t.Context()
	auth := authz.NewAuthorizer(ctx, authorization.NewDefaultAuthorizer())

	t.Run("SystemUser_AllowedAllNamespaces", func(t *testing.T) {
		claims := &authorization.Claims{
			Subject: uuid.Max.String(),
			System:  authorization.RoleReader | authorization.RoleWriter,
			Extensions: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       uuid.Max,
					TenantID: uuid.Max,
					Username: "system",
				},
			},
		}

		target := &authorization.CallTarget{
			APIName:   api.WorkflowServicePrefix + "StartWorkflowExecution",
			Namespace: uuid.MustParse("4a530f05-2904-4d66-ad27-25f69f0d36ba").String(),
		}

		result, err := auth.Authorize(ctx, claims, target)

		require.NoError(t, err)
		assert.Equal(t, authorization.DecisionAllow, result.Decision)
	})

	t.Run("RegularUser_DeniedWithoutNamespaceRole", func(t *testing.T) {
		userID := uuid.MustParse("5d027f73-923c-4936-9aa6-7371aceac69d")
		tenantID := uuid.MustParse("acdc84a1-6457-44b3-a1e3-f2e7ae8cb380")
		otherNamespace := uuid.MustParse("5bc08231-157f-4862-a0df-98ac752e1c3b").String()

		claims := &authorization.Claims{
			Subject: userID.String(),
			System:  authorization.RoleUndefined,
			Namespaces: map[string]authorization.Role{
				tenantID.String(): authorization.RoleReader | authorization.RoleWriter | authorization.RoleAdmin,
			},
			Extensions: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       userID,
					TenantID: tenantID,
					Username: "test-user",
				},
			},
		}

		target := &authorization.CallTarget{
			APIName:   api.WorkflowServicePrefix + "StartWorkflowExecution",
			Namespace: otherNamespace,
		}

		result, err := auth.Authorize(ctx, claims, target)

		require.NoError(t, err)
		assert.Equal(t, authorization.DecisionDeny, result.Decision)
	})
}
