package sdk_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authzv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	projectv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
)

// GrantClient is the implementation both extend helpers are declared
// against; the compile-time assertion keeps a signature change from
// silently leaving one of the two call sites without an implementation.
var (
	_ sdk.ProjectGrantAPI      = (*sdk.GrantClient)(nil)
	_ sdk.UserAuthorizationAPI = (*sdk.GrantClient)(nil)
)

// =============================================================================
// ExtendProjectGrantRoles / ExtendUserAuthorizationRoles (the shared algorithm)
// =============================================================================

// fakeGrantAPI stands in for both grant APIs at the interface the extend
// helpers consume. grant is the state the reads report; updates records
// every full replace in order. concurrentRole models a second writer that
// lands a role between our read and our write: it is appended to the
// reported state right after a read answers, exactly where the lost-update
// window sits.
type fakeGrantAPI struct {
	grant           *sdk.Grant
	getErr          error
	updateErr       error
	concurrentRole  string
	staleProjection bool

	gets           int
	updates        [][]string
	updatedID      string
	updatedProject string
	updatedOrg     string
}

func (f *fakeGrantAPI) read() (*sdk.Grant, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	got := &sdk.Grant{ID: f.grant.ID, Roles: append([]string(nil), f.grant.Roles...)}
	if f.concurrentRole != "" {
		f.grant.Roles = append(f.grant.Roles, f.concurrentRole)
		f.concurrentRole = ""
	}
	return got, nil
}

func (f *fakeGrantAPI) write(roleKeys []string) error {
	f.updates = append(f.updates, roleKeys)
	if f.updateErr != nil {
		return f.updateErr
	}
	if !f.staleProjection {
		f.grant.Roles = roleKeys
	}
	return nil
}

func (f *fakeGrantAPI) GetProjectGrantRoles(_ context.Context, _, _ string) ([]string, error) {
	grant, err := f.read()
	if err != nil {
		return nil, err
	}
	return grant.Roles, nil
}

func (f *fakeGrantAPI) UpdateProjectGrant(_ context.Context, projectID, organizationID string, roleKeys []string) error {
	f.updatedProject, f.updatedOrg = projectID, organizationID
	return f.write(roleKeys)
}

func (f *fakeGrantAPI) GetUserAuthorization(_ context.Context, _, _, _ string) (*sdk.Grant, error) {
	return f.read()
}

func (f *fakeGrantAPI) UpdateUserAuthorization(_ context.Context, authorizationID string, roleKeys []string) error {
	f.updatedID = authorizationID
	return f.write(roleKeys)
}

// The update replaces the whole menu, so the request must carry the union
// of the read and the requested roles: sending only the addition would
// revoke every role already on the grant, and the user grants carrying
// them. The keys the caller already holds must stay and duplicates in the
// request must not be re-sent.
func TestExtendProjectGrantRoles_ReplacesTheMenuWithTheUnion(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{grant: &sdk.Grant{ID: "grant-1", Roles: []string{"writer", "reader"}}}

	roles, updated, err := sdk.ExtendProjectGrantRoles(t.Context(), f, "proj-1", "org-1", []string{"admin", "writer", "admin"})
	require.NoError(t, err)

	assert.True(t, updated, "a menu missing a requested role must report the replace")
	require.Len(t, f.updates, 1, "exactly one full replace")
	assert.Equal(t, []string{"writer", "reader", "admin"}, f.updates[0])
	assert.Equal(t, []string{"writer", "reader", "admin"}, roles)
	assert.Equal(t, "proj-1", f.updatedProject)
	assert.Equal(t, "org-1", f.updatedOrg)
}

// A menu that already covers every requested role must not be rewritten:
// a same-value full replace still bumps the grant and fans an event out to
// every consumer of Zitadel's projections for no change at all.
func TestExtendProjectGrantRoles_CoveredMenuIsNotRewritten(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{grant: &sdk.Grant{ID: "grant-1", Roles: []string{"admin", "writer", "reader"}}}

	roles, updated, err := sdk.ExtendProjectGrantRoles(t.Context(), f, "proj-1", "org-1", []string{"writer", "admin"})
	require.NoError(t, err)

	assert.False(t, updated)
	assert.Empty(t, f.updates, "a covered menu must not be replaced")
	assert.Equal(t, []string{"admin", "writer", "reader"}, roles)
}

// The returned roles are what the call wrote, not a re-read: Zitadel's
// grant projection lags the eventstore, so re-reading here would report the
// pre-update menu and callers would act on a menu they just widened. The
// helper must therefore issue exactly one read.
func TestExtendProjectGrantRoles_ReturnsTheWrittenMenuNotAReRead(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{
		grant:           &sdk.Grant{ID: "grant-1", Roles: []string{"writer"}},
		staleProjection: true,
	}

	roles, updated, err := sdk.ExtendProjectGrantRoles(t.Context(), f, "proj-1", "org-1", []string{"admin"})
	require.NoError(t, err)

	assert.True(t, updated)
	assert.Equal(t, 1, f.gets, "the helper must not re-read; the caller decides whether the lag is worth a verify")
	assert.Equal(t, []string{"writer", "admin"}, roles)
}

// The residual the doc comment names, pinned as behaviour: Zitadel offers
// no compare-and-set on either grant update, so a second writer replacing
// the menu between this read and this write is silently overwritten: the
// role it added is gone and no event re-adds it, so only re-running the
// operation that added it repairs the menu. The test exists so a future
// "verify" that claims to detect concurrency has to face it.
func TestExtendProjectGrantRoles_ConcurrentReplaceBetweenReadAndWriteIsLost(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{
		grant:          &sdk.Grant{ID: "grant-1", Roles: []string{"writer"}},
		concurrentRole: "picking_service",
	}

	roles, updated, err := sdk.ExtendProjectGrantRoles(t.Context(), f, "proj-1", "org-1", []string{"admin"})
	require.NoError(t, err, "the lost write is undetectable, so the call reports success")

	assert.True(t, updated)
	assert.Equal(t, []string{"writer", "admin"}, f.grant.Roles, "the concurrent writer's role is dropped by the full replace")
	assert.NotContains(t, roles, "picking_service")
}

// A read failure must surface with its sentinel intact: AddProjectGrant
// answers AlreadyExists from the eventstore while this reads the lagging
// projection, so the caller distinguishes "not projected yet" (retry) from
// a transport fault.
func TestExtendProjectGrantRoles_ReadFailureKeepsItsSentinel(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{
		grant:  &sdk.Grant{ID: "grant-1"},
		getErr: fmt.Errorf("%w: project %q in org %q", sdk.ErrProjectGrantNotFound, "proj-1", "org-1"),
	}

	roles, updated, err := sdk.ExtendProjectGrantRoles(t.Context(), f, "proj-1", "org-1", []string{"admin"})

	require.ErrorIs(t, err, sdk.ErrProjectGrantNotFound)
	assert.Nil(t, roles)
	assert.False(t, updated)
	assert.Empty(t, f.updates, "nothing may be written when the current menu is unknown")
}

// A failed replace must not be reported as a widening: the caller would
// grant roles from a menu Zitadel never accepted.
func TestExtendProjectGrantRoles_WriteFailureNamesTheOrg(t *testing.T) {
	t.Parallel()
	unavailable := status.Error(codes.Unavailable, "zitadel restarting")
	f := &fakeGrantAPI{grant: &sdk.Grant{ID: "grant-1", Roles: []string{"writer"}}, updateErr: unavailable}

	roles, updated, err := sdk.ExtendProjectGrantRoles(t.Context(), f, "proj-1", "org-1", []string{"admin"})

	require.ErrorIs(t, err, unavailable)
	require.ErrorContains(t, err, `org "org-1"`)
	assert.Nil(t, roles)
	assert.False(t, updated)
}

// The authorization update is addressed by authorization id, not by
// user/project/org, and is the same full replace, so the union goes out
// under the id the read reported.
func TestExtendUserAuthorizationRoles_ReplacesUnderTheAuthorizationID(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{grant: &sdk.Grant{ID: "authz-1", Roles: []string{"writer", "inventory_service"}}}

	authz, updated, err := sdk.ExtendUserAuthorizationRoles(t.Context(), f, "proj-1", "org-1", "user-1", []string{"admin", "writer"})
	require.NoError(t, err)

	assert.True(t, updated)
	assert.Equal(t, "authz-1", f.updatedID, "the replace must address the authorization the read reported")
	require.Len(t, f.updates, 1)
	assert.Equal(t, []string{"writer", "inventory_service", "admin"}, f.updates[0])
	assert.Equal(t, "authz-1", authz.ID)
	assert.Equal(t, []string{"writer", "inventory_service", "admin"}, authz.Roles)
}

func TestExtendUserAuthorizationRoles_CoveredRolesAreNotRewritten(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{grant: &sdk.Grant{ID: "authz-1", Roles: []string{"admin", "writer"}}}

	authz, updated, err := sdk.ExtendUserAuthorizationRoles(t.Context(), f, "proj-1", "org-1", "user-1", []string{"writer"})
	require.NoError(t, err)

	assert.False(t, updated)
	assert.Empty(t, f.updates)
	assert.Equal(t, []string{"admin", "writer"}, authz.Roles)
}

// A user with no authorization yet is not an error the helper can fix: the
// caller decides between creating one and retrying the lagging projection,
// so the sentinel must reach it.
func TestExtendUserAuthorizationRoles_MissingAuthorizationKeepsItsSentinel(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{
		grant:  &sdk.Grant{ID: "authz-1"},
		getErr: fmt.Errorf("%w: user %q on project %q in org %q", sdk.ErrUserGrantNotFound, "user-1", "proj-1", "org-1"),
	}

	authz, updated, err := sdk.ExtendUserAuthorizationRoles(t.Context(), f, "proj-1", "org-1", "user-1", []string{"admin"})

	require.ErrorIs(t, err, sdk.ErrUserGrantNotFound)
	assert.Nil(t, authz)
	assert.False(t, updated)
	assert.Empty(t, f.updates)
}

func TestExtendUserAuthorizationRoles_WriteFailureNamesUserAndOrg(t *testing.T) {
	t.Parallel()
	unavailable := status.Error(codes.Unavailable, "zitadel restarting")
	f := &fakeGrantAPI{grant: &sdk.Grant{ID: "authz-1", Roles: []string{"writer"}}, updateErr: unavailable}

	authz, updated, err := sdk.ExtendUserAuthorizationRoles(t.Context(), f, "proj-1", "org-1", "user-1", []string{"admin"})

	require.ErrorIs(t, err, unavailable)
	require.ErrorContains(t, err, `user "user-1"`)
	require.ErrorContains(t, err, `org "org-1"`)
	assert.Nil(t, authz)
	assert.False(t, updated)
}

// The same lost-update window on the user's own role set: the second
// writer's role never comes back.
func TestExtendUserAuthorizationRoles_ConcurrentReplaceBetweenReadAndWriteIsLost(t *testing.T) {
	t.Parallel()
	f := &fakeGrantAPI{
		grant:          &sdk.Grant{ID: "authz-1", Roles: []string{"writer"}},
		concurrentRole: "inventory_service",
	}

	_, updated, err := sdk.ExtendUserAuthorizationRoles(t.Context(), f, "proj-1", "org-1", "user-1", []string{"admin"})
	require.NoError(t, err)

	assert.True(t, updated)
	assert.Equal(t, []string{"writer", "admin"}, f.grant.Roles, "the concurrently granted role is dropped by the full replace")
}

// =============================================================================
// GrantClient (the gRPC calls the helpers compose)
// =============================================================================

// grantState is the Zitadel-side state the two mock services share.
type grantState struct {
	// projectGrantsV2 is what the granted-org filtered list answers: the
	// read GetProjectGrantRoles uses.
	projectGrantsV2 []*projectv2_pb.ProjectGrant

	authorizations []*authzv2_pb.Authorization
	listAuthzErr   error

	capturedProjectFilters []*projectv2_pb.ProjectGrantSearchFilter

	capturedUpdate  *projectv2_pb.UpdateProjectGrantRequest
	capturedFilters []*authzv2_pb.AuthorizationsSearchFilter
	capturedAuthz   *authzv2_pb.UpdateAuthorizationRequest
}

// mockProjectGrants serves the v2 project list (the granted-org filtered
// menu read) and the v2 project update (the full replace).
type mockProjectGrants struct {
	projectv2_pb.UnimplementedProjectServiceServer
	state *grantState
}

func (s *mockProjectGrants) ListProjectGrants(_ context.Context, req *projectv2_pb.ListProjectGrantsRequest) (*projectv2_pb.ListProjectGrantsResponse, error) {
	s.state.capturedProjectFilters = req.GetFilters()
	return &projectv2_pb.ListProjectGrantsResponse{ProjectGrants: s.state.projectGrantsV2}, nil
}

func (s *mockProjectGrants) UpdateProjectGrant(_ context.Context, req *projectv2_pb.UpdateProjectGrantRequest) (*projectv2_pb.UpdateProjectGrantResponse, error) {
	s.state.capturedUpdate = req
	return &projectv2_pb.UpdateProjectGrantResponse{}, nil
}

// mockAuthorizations serves the v2 authorization list and update.
type mockAuthorizations struct {
	authzv2_pb.UnimplementedAuthorizationServiceServer
	state *grantState
}

func (s *mockAuthorizations) ListAuthorizations(_ context.Context, req *authzv2_pb.ListAuthorizationsRequest) (*authzv2_pb.ListAuthorizationsResponse, error) {
	s.state.capturedFilters = req.GetFilters()
	if s.state.listAuthzErr != nil {
		return nil, s.state.listAuthzErr
	}
	return &authzv2_pb.ListAuthorizationsResponse{Authorizations: s.state.authorizations}, nil
}

func (s *mockAuthorizations) UpdateAuthorization(_ context.Context, req *authzv2_pb.UpdateAuthorizationRequest) (*authzv2_pb.UpdateAuthorizationResponse, error) {
	s.state.capturedAuthz = req
	return &authzv2_pb.UpdateAuthorizationResponse{}, nil
}

// newGrantTestClient wires the mocks behind a GrantClient over bufconn, the
// way a caller wires it over its already-dialled Zitadel connection.
func newGrantTestClient(t *testing.T, state *grantState) *sdk.GrantClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	projectv2_pb.RegisterProjectServiceServer(srv, &mockProjectGrants{state: state})
	authzv2_pb.RegisterAuthorizationServiceServer(srv, &mockAuthorizations{state: state})

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

	return sdk.NewGrantClient(conn)
}

func TestGrantClient_UpdateProjectGrant_SendsTheFullRoleSet(t *testing.T) {
	t.Parallel()
	state := &grantState{}
	client := newGrantTestClient(t, state)

	require.NoError(t, client.UpdateProjectGrant(t.Context(), "proj-1", "org-1", []string{"admin", "writer"}))

	require.NotNil(t, state.capturedUpdate)
	assert.Equal(t, "proj-1", state.capturedUpdate.GetProjectId())
	assert.Equal(t, "org-1", state.capturedUpdate.GetGrantedOrganizationId())
	assert.Equal(t, []string{"admin", "writer"}, state.capturedUpdate.GetRoleKeys())
}

// The lookup is scoped by user, project AND org: a cross-tenant user holds
// one authorization per org, and reading the wrong one would extend the
// roles it holds somewhere else.
func TestGrantClient_GetUserAuthorization_ScopesByUserProjectAndOrg(t *testing.T) {
	t.Parallel()
	state := &grantState{
		authorizations: []*authzv2_pb.Authorization{{
			Id:    "authz-1",
			Roles: []*authzv2_pb.Role{{Key: "writer"}, {Key: "inventory_service"}},
		}},
	}
	client := newGrantTestClient(t, state)

	authz, err := client.GetUserAuthorization(t.Context(), "proj-1", "org-1", "user-1")
	require.NoError(t, err)

	assert.Equal(t, "authz-1", authz.ID)
	assert.Equal(t, []string{"writer", "inventory_service"}, authz.Roles)

	require.Len(t, state.capturedFilters, 3)
	assert.Equal(t, []string{"user-1"}, state.capturedFilters[0].GetInUserIds().GetIds())
	assert.Equal(t, "proj-1", state.capturedFilters[1].GetProjectId().GetId())
	assert.Equal(t, "org-1", state.capturedFilters[2].GetOrganizationId().GetId())
}

func TestGrantClient_GetUserAuthorization_NoAuthorizationIsItsSentinel(t *testing.T) {
	t.Parallel()
	state := &grantState{}
	client := newGrantTestClient(t, state)

	authz, err := client.GetUserAuthorization(t.Context(), "proj-1", "org-1", "user-1")

	require.ErrorIs(t, err, sdk.ErrUserGrantNotFound)
	require.ErrorContains(t, err, `user "user-1"`)
	assert.Nil(t, authz)
}

func TestGrantClient_GetUserAuthorization_ListFailureSurfaces(t *testing.T) {
	t.Parallel()
	state := &grantState{listAuthzErr: status.Error(codes.Unavailable, "zitadel restarting")}
	client := newGrantTestClient(t, state)

	_, err := client.GetUserAuthorization(t.Context(), "proj-1", "org-1", "user-1")

	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.NotErrorIs(t, err, sdk.ErrUserGrantNotFound, "a transport fault must not read as a settled absence")
}

func TestGrantClient_UpdateUserAuthorization_SendsIDAndFullRoleSet(t *testing.T) {
	t.Parallel()
	state := &grantState{}
	client := newGrantTestClient(t, state)

	require.NoError(t, client.UpdateUserAuthorization(t.Context(), "authz-1", []string{"admin", "writer"}))

	require.NotNil(t, state.capturedAuthz)
	assert.Equal(t, "authz-1", state.capturedAuthz.GetId())
	assert.Equal(t, []string{"admin", "writer"}, state.capturedAuthz.GetRoleKeys())
}

// The menu read must go through the list filtered by project AND granted
// organization: without the org filter it would page every tenant's grant
// on the project and stop finding the org's once they outgrow one page.
func TestGrantClient_GetProjectGrantRoles_FiltersByProjectAndOrg(t *testing.T) {
	t.Parallel()
	state := &grantState{
		projectGrantsV2: []*projectv2_pb.ProjectGrant{
			{GrantedOrganizationId: "org-1", GrantedRoleKeys: []string{"admin", "writer"}},
		},
	}
	client := newGrantTestClient(t, state)

	roles, err := client.GetProjectGrantRoles(t.Context(), "proj-1", "org-1")
	require.NoError(t, err)

	assert.Equal(t, []string{"admin", "writer"}, roles)
	require.Len(t, state.capturedProjectFilters, 2)
	assert.Equal(t, []string{"proj-1"}, state.capturedProjectFilters[0].GetInProjectIdsFilter().GetIds())
	assert.Equal(t, "org-1", state.capturedProjectFilters[1].GetGrantedOrganizationIdFilter().GetId())
}

// An org with no grant on the project must surface the sentinel, so
// assignRoles can answer "tenant has no project grant" instead of writing a
// menu onto a grant that does not exist.
func TestGrantClient_GetProjectGrantRoles_NoGrantReportsNotFound(t *testing.T) {
	t.Parallel()
	client := newGrantTestClient(t, &grantState{})

	roles, err := client.GetProjectGrantRoles(t.Context(), "proj-1", "org-1")

	require.ErrorIs(t, err, sdk.ErrProjectGrantNotFound)
	require.ErrorContains(t, err, `org "org-1"`)
	assert.Nil(t, roles)
}

// =============================================================================
