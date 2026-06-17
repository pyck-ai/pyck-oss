package sdk_test

import (
	"context"
	"net"
	"testing"

	managementclient "github.com/zitadel/zitadel-go/v3/pkg/client/management"
	pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	metadata_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
)

type mockManagementServer struct {
	pb.UnimplementedManagementServiceServer

	capturedOrgID string
	response      []*metadata_pb.Metadata
}

func (s *mockManagementServer) ListOrgMetadata(ctx context.Context, _ *pb.ListOrgMetadataRequest) (*pb.ListOrgMetadataResponse, error) {
	if md, ok := grpcmd.FromIncomingContext(ctx); ok {
		vals := md.Get("x-zitadel-orgid")
		if len(vals) > 0 {
			s.capturedOrgID = vals[0]
		}
	}
	return &pb.ListOrgMetadataResponse{Result: s.response}, nil
}

func newTestClient(t *testing.T, mock *mockManagementServer) *sdk.ZitadelSdkClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterManagementServiceServer(srv, mock)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return sdk.NewTestSdkClient(&managementclient.Client{
		ManagementServiceClient: pb.NewManagementServiceClient(conn),
	})
}

func TestListOrgMetadataForOrg_SetsOrgHeader(t *testing.T) {
	t.Parallel()

	mock := &mockManagementServer{
		response: []*metadata_pb.Metadata{
			{Key: "flavour", Value: []byte("pyck-go")},
			{Key: "isPyckGo", Value: []byte("true")},
		},
	}

	client := newTestClient(t, mock)

	result, err := client.ListOrgMetadataForOrg(context.Background(), "org-42")
	if err != nil {
		t.Fatalf("ListOrgMetadataForOrg failed: %v", err)
	}

	if mock.capturedOrgID != "org-42" {
		t.Errorf("expected x-zitadel-orgid=org-42, got %q", mock.capturedOrgID)
	}

	if result["flavour"] != "pyck-go" {
		t.Errorf("expected flavour=pyck-go, got %q", result["flavour"])
	}
	if result["isPyckGo"] != "true" {
		t.Errorf("expected isPyckGo=true, got %q", result["isPyckGo"])
	}
}

func TestListOrgMetadataForOrg_DifferentOrgsGetDifferentHeaders(t *testing.T) {
	t.Parallel()

	mock := &mockManagementServer{}
	client := newTestClient(t, mock)

	orgs := []string{"org-1", "org-2", "org-3"}
	for _, orgID := range orgs {
		_, err := client.ListOrgMetadataForOrg(context.Background(), orgID)
		if err != nil {
			t.Fatalf("ListOrgMetadataForOrg(%s) failed: %v", orgID, err)
		}
		if mock.capturedOrgID != orgID {
			t.Errorf("call with orgID=%s: expected header %s, got %s", orgID, orgID, mock.capturedOrgID)
		}
	}
}

func TestListOrgMetadata_WithoutOrgHeader(t *testing.T) {
	t.Parallel()

	mock := &mockManagementServer{
		response: []*metadata_pb.Metadata{
			{Key: "key1", Value: []byte("val1")},
		},
	}
	client := newTestClient(t, mock)

	result, err := client.ListOrgMetadata(context.Background())
	if err != nil {
		t.Fatalf("ListOrgMetadata failed: %v", err)
	}

	if mock.capturedOrgID != "" {
		t.Errorf("expected no org header, got %q", mock.capturedOrgID)
	}

	if result["key1"] != "val1" {
		t.Errorf("expected key1=val1, got %q", result["key1"])
	}
}
