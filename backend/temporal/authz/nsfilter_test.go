package authz_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/temporal/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	namespacepb "go.temporal.io/api/namespace/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/common/authorization"
	"google.golang.org/grpc"
)

func TestNamespaceFilter_NonListNamespacesRequest(t *testing.T) {
	ctx := t.Context()
	interceptor := authz.NewNamespaceFilter(ctx)

	// Test with a different request type - should pass through unchanged
	request := &workflowservice.GetClusterInfoRequest{}
	expectedResponse := &workflowservice.GetClusterInfoResponse{
		ClusterName: "test-cluster",
	}

	nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return expectedResponse, nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/GetClusterInfo",
	}

	response, err := interceptor(ctx, request, info, nextHandler)

	require.NoError(t, err)
	assert.Equal(t, expectedResponse, response)
}

func TestNamespaceFilter_InternalCall(t *testing.T) {
	ctx := t.Context()
	interceptor := authz.NewNamespaceFilter(ctx)

	// Create response with multiple namespaces
	namespaces := []*workflowservice.DescribeNamespaceResponse{
		{
			NamespaceInfo: &namespacepb.NamespaceInfo{
				Name: "namespace-1",
				Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
			},
		},
		{
			NamespaceInfo: &namespacepb.NamespaceInfo{
				Name: "namespace-2",
				Id:   uuid.MustParse("35dbbea0-2503-4150-912e-331fc5961866").String(),
			},
		},
	}

	request := &workflowservice.ListNamespacesRequest{}
	originalResponse := &workflowservice.ListNamespacesResponse{
		Namespaces: namespaces,
	}

	nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return originalResponse, nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
	}

	// No claims in context (internal call) - should return unmodified
	response, err := interceptor(ctx, request, info, nextHandler)

	require.NoError(t, err)
	resp, ok := response.(*workflowservice.ListNamespacesResponse)
	require.True(t, ok)
	assert.Len(t, resp.GetNamespaces(), 2)
	assert.Equal(t, namespaces, resp.GetNamespaces())
}

func TestNamespaceFilter_SystemUser(t *testing.T) {
	// System user should see all namespaces
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

	ctx := context.WithValue(t.Context(), authorization.MappedClaims, claims)
	interceptor := authz.NewNamespaceFilter(ctx)

	namespaces := []*workflowservice.DescribeNamespaceResponse{
		{
			NamespaceInfo: &namespacepb.NamespaceInfo{
				Name: "namespace-1",
				Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
			},
		},
		{
			NamespaceInfo: &namespacepb.NamespaceInfo{
				Name: "namespace-2",
				Id:   uuid.MustParse("35dbbea0-2503-4150-912e-331fc5961866").String(),
			},
		},
		{
			NamespaceInfo: &namespacepb.NamespaceInfo{
				Name: "namespace-3",
				Id:   uuid.MustParse("945fa704-6d90-49ac-a9a0-9f65427c4c6f").String(),
			},
		},
	}

	request := &workflowservice.ListNamespacesRequest{}
	originalResponse := &workflowservice.ListNamespacesResponse{
		Namespaces: namespaces,
	}

	nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return originalResponse, nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
	}

	response, err := interceptor(ctx, request, info, nextHandler)

	require.NoError(t, err)
	resp, ok := response.(*workflowservice.ListNamespacesResponse)
	require.True(t, ok)
	assert.Len(t, resp.GetNamespaces(), 3, "System user should see all namespaces")
	assert.Equal(t, namespaces, resp.GetNamespaces())
}

func TestNamespaceFilter_RegularUser(t *testing.T) {
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")

	ns1 := tenantID.String()
	ns2 := uuid.MustParse("acdc84a1-6457-44b3-a1e3-f2e7ae8cb380").String()
	ns3 := uuid.MustParse("5bc08231-157f-4862-a0df-98ac752e1c3b").String()

	testCases := []struct {
		name              string
		userRoles         map[string]authorization.Role
		namespaces        []*workflowservice.DescribeNamespaceResponse
		expectedCount     int
		expectedNamespace []string
	}{
		{
			name: "UserWithReaderRole_SeesOneNamespace",
			userRoles: map[string]authorization.Role{
				ns1: authorization.RoleReader,
			},
			namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns1,
						Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
					},
				},
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns2,
						Id:   uuid.MustParse("35dbbea0-2503-4150-912e-331fc5961866").String(),
					},
				},
			},
			expectedCount:     1,
			expectedNamespace: []string{ns1},
		},
		{
			name: "UserWithMultipleRoles_SeesMultipleNamespaces",
			userRoles: map[string]authorization.Role{
				ns1: authorization.RoleReader,
				ns2: authorization.RoleReader | authorization.RoleWriter,
			},
			namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns1,
						Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
					},
				},
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns2,
						Id:   uuid.MustParse("35dbbea0-2503-4150-912e-331fc5961866").String(),
					},
				},
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns3,
						Id:   uuid.MustParse("945fa704-6d90-49ac-a9a0-9f65427c4c6f").String(),
					},
				},
			},
			expectedCount:     2,
			expectedNamespace: []string{ns1, ns2},
		},
		{
			name: "UserWithNoRoles_SeesNoNamespaces",
			userRoles: map[string]authorization.Role{
				ns1: authorization.RoleUndefined,
			},
			namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns1,
						Id:   uuid.New().String(),
					},
				},
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns2,
						Id:   uuid.New().String(),
					},
				},
			},
			expectedCount:     0,
			expectedNamespace: []string{},
		},
		{
			name: "UserWithWriterOnly_StillSeesNamespace",
			userRoles: map[string]authorization.Role{
				ns1: authorization.RoleWriter, // Writer without Reader
			},
			namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns1,
						Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
					},
				},
			},
			expectedCount:     0,
			expectedNamespace: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			claims := &authorization.Claims{
				Subject:    userID.String(),
				Namespaces: tc.userRoles,
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "test-user",
					},
				},
			}

			ctx := context.WithValue(t.Context(), authorization.MappedClaims, claims)
			interceptor := authz.NewNamespaceFilter(ctx)

			request := &workflowservice.ListNamespacesRequest{}
			originalResponse := &workflowservice.ListNamespacesResponse{
				Namespaces: tc.namespaces,
			}

			nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
				return originalResponse, nil
			}

			info := &grpc.UnaryServerInfo{
				FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
			}

			response, err := interceptor(ctx, request, info, nextHandler)

			require.NoError(t, err)
			resp, ok := response.(*workflowservice.ListNamespacesResponse)
			require.True(t, ok)
			assert.Len(t, resp.GetNamespaces(), tc.expectedCount)

			// Verify the correct namespaces are included
			for _, expectedNS := range tc.expectedNamespace {
				found := false
				for _, ns := range resp.GetNamespaces() {
					if ns.GetNamespaceInfo().GetName() == expectedNS {
						found = true
						break
					}
				}
				assert.True(t, found, "Expected namespace %s not found in filtered response", expectedNS)
			}
		})
	}
}

func TestNamespaceFilter_EdgeCases(t *testing.T) {
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")
	ns1 := tenantID.String()

	t.Run("EmptyNamespaceList_ReturnsEmpty", func(t *testing.T) {
		claims := &authorization.Claims{
			Subject: userID.String(),
			Namespaces: map[string]authorization.Role{
				ns1: authorization.RoleReader,
			},
			Extensions: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       userID,
					TenantID: tenantID,
					Username: "test-user",
				},
			},
		}

		ctx := context.WithValue(t.Context(), authorization.MappedClaims, claims)
		interceptor := authz.NewNamespaceFilter(ctx)

		request := &workflowservice.ListNamespacesRequest{}
		originalResponse := &workflowservice.ListNamespacesResponse{
			Namespaces: []*workflowservice.DescribeNamespaceResponse{},
		}

		nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return originalResponse, nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
		}

		response, err := interceptor(ctx, request, info, nextHandler)

		require.NoError(t, err)
		resp, ok := response.(*workflowservice.ListNamespacesResponse)
		require.True(t, ok)
		assert.Len(t, resp.GetNamespaces(), 0)
	})

	t.Run("NamespaceWithoutInfo_Skipped", func(t *testing.T) {
		claims := &authorization.Claims{
			Subject: userID.String(),
			Namespaces: map[string]authorization.Role{
				ns1: authorization.RoleReader,
			},
			Extensions: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       userID,
					TenantID: tenantID,
					Username: "test-user",
				},
			},
		}

		ctx := context.WithValue(t.Context(), authorization.MappedClaims, claims)
		interceptor := authz.NewNamespaceFilter(ctx)

		request := &workflowservice.ListNamespacesRequest{}
		originalResponse := &workflowservice.ListNamespacesResponse{
			Namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: nil, // Missing namespace info
				},
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns1,
						Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
					},
				},
			},
		}

		nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return originalResponse, nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
		}

		response, err := interceptor(ctx, request, info, nextHandler)

		require.NoError(t, err)
		resp, ok := response.(*workflowservice.ListNamespacesResponse)
		require.True(t, ok)
		assert.Len(t, resp.GetNamespaces(), 1)
		assert.Equal(t, ns1, resp.GetNamespaces()[0].GetNamespaceInfo().GetName())
	})

	t.Run("NamespaceWithEmptyName_Skipped", func(t *testing.T) {
		claims := &authorization.Claims{
			Subject: userID.String(),
			Namespaces: map[string]authorization.Role{
				ns1: authorization.RoleReader,
			},
			Extensions: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       userID,
					TenantID: tenantID,
					Username: "test-user",
				},
			},
		}

		ctx := context.WithValue(t.Context(), authorization.MappedClaims, claims)
		interceptor := authz.NewNamespaceFilter(ctx)

		request := &workflowservice.ListNamespacesRequest{}
		originalResponse := &workflowservice.ListNamespacesResponse{
			Namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: "", // Empty name
						Id:   uuid.MustParse("1a9ae78b-ffa4-4134-91e4-bb8cd3c1d402").String(),
					},
				},
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns1,
						Id:   uuid.MustParse("cb359ebb-a44e-4bae-aec9-87f04298bd1a").String(),
					},
				},
			},
		}

		nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return originalResponse, nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
		}

		response, err := interceptor(ctx, request, info, nextHandler)

		require.NoError(t, err)
		resp, ok := response.(*workflowservice.ListNamespacesResponse)
		require.True(t, ok)
		assert.Len(t, resp.GetNamespaces(), 1)
		assert.Equal(t, ns1, resp.GetNamespaces()[0].GetNamespaceInfo().GetName())
	})

	t.Run("AllNamespacesFiltered_ReturnsEmptyResponse", func(t *testing.T) {
		ns2 := uuid.MustParse("acdc84a1-6457-44b3-a1e3-f2e7ae8cb380").String()

		claims := &authorization.Claims{
			Subject: userID.String(),
			Namespaces: map[string]authorization.Role{
				ns1: authorization.RoleReader,
			},
			Extensions: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       userID,
					TenantID: tenantID,
					Username: "test-user",
				},
			},
		}

		ctx := context.WithValue(t.Context(), authorization.MappedClaims, claims)
		interceptor := authz.NewNamespaceFilter(ctx)

		request := &workflowservice.ListNamespacesRequest{}
		originalResponse := &workflowservice.ListNamespacesResponse{
			Namespaces: []*workflowservice.DescribeNamespaceResponse{
				{
					NamespaceInfo: &namespacepb.NamespaceInfo{
						Name: ns2, // User doesn't have access
						Id:   uuid.MustParse("35dbbea0-2503-4150-912e-331fc5961866").String(),
					},
				},
			},
		}

		nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return originalResponse, nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces",
		}

		response, err := interceptor(ctx, request, info, nextHandler)

		require.NoError(t, err)
		resp, ok := response.(*workflowservice.ListNamespacesResponse)
		require.True(t, ok)
		assert.Len(t, resp.GetNamespaces(), 0)
	})
}
