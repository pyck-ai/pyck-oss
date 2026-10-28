package sdk_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	managementclient "github.com/zitadel/zitadel-go/v3/pkg/client/management"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	orgv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
)

// =============================================================================
// GetOrganizationByID (org v2)
// =============================================================================

// mockOrgServer stands in for Zitadel's org v2 service: listResult answers
// the lookup by ID, listErr fails it.
type mockOrgServer struct {
	orgv2_pb.UnimplementedOrganizationServiceServer

	listErr    error
	listResult []*orgv2_pb.Organization

	capturedList *orgv2_pb.ListOrganizationsRequest
}

func (s *mockOrgServer) ListOrganizations(_ context.Context, req *orgv2_pb.ListOrganizationsRequest) (*orgv2_pb.ListOrganizationsResponse, error) {
	s.capturedList = req
	if s.listErr != nil {
		return nil, s.listErr
	}
	return &orgv2_pb.ListOrganizationsResponse{Result: s.listResult}, nil
}

// newOrgTestClient wires the org v2 mock behind the management client's
// Connection, which is what GetOrganizationByID dials the v2 service on.
func newOrgTestClient(t *testing.T, mock *mockOrgServer) *sdk.ZitadelSdkClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	orgv2_pb.RegisterOrganizationServiceServer(srv, mock)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.GracefulStop()
		if err := <-serveErr; err != nil {
			t.Errorf("bufconn server: %v", err)
		}
	})

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "dial bufconn")
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("close bufconn client: %v", err)
		}
	})

	return sdk.NewTestSdkClient(&managementclient.Client{
		Connection: &zitadel.Connection{ClientConn: conn},
	})
}

// The lookup is by ID, and a live org (ACTIVE or INACTIVE) is found.
func TestGetOrganizationByID_FindsLiveOrg(t *testing.T) {
	t.Parallel()

	mock := &mockOrgServer{
		listResult: []*orgv2_pb.Organization{{Id: "org-1", Name: "Acme", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE}},
	}
	client := newOrgTestClient(t, mock)

	org, err := client.GetOrganizationByID(t.Context(), "org-1")
	require.NoError(t, err)
	assert.Equal(t, "org-1", org.ID)

	require.NotNil(t, mock.capturedList)
	require.Len(t, mock.capturedList.GetQueries(), 1)
	assert.Equal(t, "org-1", mock.capturedList.GetQueries()[0].GetIdQuery().GetId())
}

// An org Zitadel has removed is gone for pyck whether the projection has
// already dropped it or still lists it as REMOVED; both must be the
// sentinel so the caller can skip instead of treating "no users" as truth.
func TestGetOrganizationByID_AbsentAndRemovedAreNotFound(t *testing.T) {
	t.Parallel()

	cases := map[string][]*orgv2_pb.Organization{
		"absent":  nil,
		"removed": {{Id: "org-1", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_REMOVED}},
	}
	for name, listResult := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := newOrgTestClient(t, &mockOrgServer{listResult: listResult})

			org, err := client.GetOrganizationByID(t.Context(), "org-1")
			require.ErrorIs(t, err, sdk.ErrOrganizationNotFound)
			require.ErrorContains(t, err, "org-1")
			assert.Nil(t, org)
		})
	}
}

// A transport failure is not "not found": it keeps its gRPC code so the
// activity retry policy sees a retryable error.
func TestGetOrganizationByID_PassesThroughOtherErrors(t *testing.T) {
	t.Parallel()

	client := newOrgTestClient(t, &mockOrgServer{listErr: status.Error(codes.Unavailable, "zitadel restarting")})

	org, err := client.GetOrganizationByID(t.Context(), "org-1")
	require.Error(t, err)
	require.NotErrorIs(t, err, sdk.ErrOrganizationNotFound)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Nil(t, org)
}

// The ID filter is re-checked client-side: a server answering the org-1
// query with some other live org must not be taken as "org-1 exists", or a
// deleted org would be treated as live whenever the listing is unfiltered.
func TestGetOrganizationByID_IgnoresOrgWithOtherID(t *testing.T) {
	t.Parallel()

	client := newOrgTestClient(t, &mockOrgServer{
		listResult: []*orgv2_pb.Organization{{Id: "org-2", Name: "Other", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE}},
	})

	org, err := client.GetOrganizationByID(t.Context(), "org-1")
	require.ErrorIs(t, err, sdk.ErrOrganizationNotFound)
	require.ErrorContains(t, err, "org-1")
	assert.Nil(t, org)
}
