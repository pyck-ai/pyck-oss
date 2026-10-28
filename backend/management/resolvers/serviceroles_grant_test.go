package resolvers_test

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	authz_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	proj_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// fakeGrants stands in for Zitadel's project v2 and authorization v2
// services: one org's project-grant menu and one user's authorization.
type fakeGrants struct {
	proj_pb.UnimplementedProjectServiceServer
	authz_pb.UnimplementedAuthorizationServiceServer

	mu sync.Mutex
	// menu is the org's project-grant role menu; nil means no grant.
	menu []string
	// authzRoles is the user's authorization; nil means none.
	authzRoles  []string
	menuUpdates [][]string
	authzWrites [][]string
	creates     []*authz_pb.CreateAuthorizationRequest
	updateErr   error
}

func (f *fakeGrants) ListProjectGrants(context.Context, *proj_pb.ListProjectGrantsRequest) (*proj_pb.ListProjectGrantsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.menu == nil {
		return &proj_pb.ListProjectGrantsResponse{}, nil
	}
	return &proj_pb.ListProjectGrantsResponse{ProjectGrants: []*proj_pb.ProjectGrant{{GrantedRoleKeys: slices.Clone(f.menu)}}}, nil
}

func (f *fakeGrants) UpdateProjectGrant(_ context.Context, req *proj_pb.UpdateProjectGrantRequest) (*proj_pb.UpdateProjectGrantResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.menuUpdates = append(f.menuUpdates, req.GetRoleKeys())
	f.menu = req.GetRoleKeys()
	return &proj_pb.UpdateProjectGrantResponse{}, nil
}

func (f *fakeGrants) ListAuthorizations(context.Context, *authz_pb.ListAuthorizationsRequest) (*authz_pb.ListAuthorizationsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authzRoles == nil {
		return &authz_pb.ListAuthorizationsResponse{}, nil
	}
	roles := make([]*authz_pb.Role, 0, len(f.authzRoles))
	for _, k := range f.authzRoles {
		roles = append(roles, &authz_pb.Role{Key: k})
	}
	return &authz_pb.ListAuthorizationsResponse{Authorizations: []*authz_pb.Authorization{{Id: "authz-1", Roles: roles}}}, nil
}

func (f *fakeGrants) UpdateAuthorization(_ context.Context, req *authz_pb.UpdateAuthorizationRequest) (*authz_pb.UpdateAuthorizationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authzWrites = append(f.authzWrites, req.GetRoleKeys())
	f.authzRoles = req.GetRoleKeys()
	return &authz_pb.UpdateAuthorizationResponse{}, nil
}

func (f *fakeGrants) CreateAuthorization(_ context.Context, req *authz_pb.CreateAuthorizationRequest) (*authz_pb.CreateAuthorizationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, req)
	f.authzRoles = req.GetRoleKeys()
	return &authz_pb.CreateAuthorizationResponse{Id: "authz-new"}, nil
}

// dialFakeGrants serves f over an in-process gRPC connection.
func dialFakeGrants(t *testing.T, f *fakeGrants) *zitadelsdk.Connection {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	proj_pb.RegisterProjectServiceServer(srv, f)
	authz_pb.RegisterAuthorizationServiceServer(srv, f)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		if err := <-serveErr; err != nil {
			t.Logf("bufconn server: %v", err)
		}
	})

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("close bufconn client: %v", err)
		}
	})
	return &zitadelsdk.Connection{ClientConn: conn}
}

var assignRolesMut = resolver.ParseTemplate(`mutation {
	assignRoles(input: { tenantId: "{{.TenantID}}", userId: "{{.UserID}}", roles: [{{range $i, $r := .Roles}}{{if $i}}, {{end}}"{{$r}}"{{end}}] }) {
		userId
		roles
	}
}`)

type assignRolesData struct {
	AssignRoles struct {
		UserID string
		Roles  []string
	}
}

// assignRolesEnv seeds tenant A and one of its users and returns the
// environment wired to f, plus the template args for that user.
func assignRolesEnv(t *testing.T, f *fakeGrants, roles ...string) (*testEnv, map[string]any) {
	t.Helper()
	te := setupWithZitadel(t, dialFakeGrants(t, f))
	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Create()
	user := te.newUser(te.ctx(systemUser), userA).Create()
	return te, map[string]any{"TenantID": resolver.TenantA, "UserID": user.ID, "Roles": roles}
}

// assignRoles goes through the shared read-union-replace helpers: both
// Zitadel writes are full replaces, so what is written must be the union of
// what the org's menu and the user's authorization already hold and the
// requested roles, never the addition alone, which would revoke the rest.
func TestAssignRoles_WidensMenuAndAuthorizationWithTheUnion(t *testing.T) {
	t.Parallel()

	f := &fakeGrants{
		menu:       []string{"admin", "writer", "reader", "picking_service"},
		authzRoles: []string{"admin", "picking_service"},
	}
	te, args := assignRolesEnv(t, f, "inventory_service")
	defer te.Close(t)

	data := execOK[assignRolesData](te, te.ctx(userA), assignRolesMut, args)

	assert.Equal(t, []string{"inventory_service", "picking_service"}, data.AssignRoles.Roles,
		"the answer lists every service role held afterwards, ladder roles filtered out")
	require.Len(t, f.menuUpdates, 1)
	assert.ElementsMatch(t, []string{"admin", "writer", "reader", "picking_service", "inventory_service"}, f.menuUpdates[0])
	require.Len(t, f.authzWrites, 1)
	assert.ElementsMatch(t, []string{"admin", "picking_service", "inventory_service"}, f.authzWrites[0])
	assert.Empty(t, f.creates)
}

// A request for roles already held rewrites nothing in Zitadel.
func TestAssignRoles_HeldRolesAreANoOp(t *testing.T) {
	t.Parallel()

	f := &fakeGrants{
		menu:       []string{"admin", "inventory_service"},
		authzRoles: []string{"admin", "inventory_service"},
	}
	te, args := assignRolesEnv(t, f, "inventory_service")
	defer te.Close(t)

	data := execOK[assignRolesData](te, te.ctx(userA), assignRolesMut, args)

	assert.Equal(t, []string{"inventory_service"}, data.AssignRoles.Roles)
	assert.Empty(t, f.menuUpdates)
	assert.Empty(t, f.authzWrites)
	assert.Empty(t, f.creates)
}

// A user without an authorization on the project gets one with exactly the
// requested roles, after the menu was widened to carry them.
func TestAssignRoles_CreatesAnAuthorizationForAUserWithout(t *testing.T) {
	t.Parallel()

	f := &fakeGrants{menu: []string{"admin"}}
	te, args := assignRolesEnv(t, f, "picking_service", "inventory_service", "picking_service")
	defer te.Close(t)

	data := execOK[assignRolesData](te, te.ctx(userA), assignRolesMut, args)

	assert.Equal(t, []string{"inventory_service", "picking_service"}, data.AssignRoles.Roles)
	require.Len(t, f.menuUpdates, 1)
	assert.ElementsMatch(t, []string{"admin", "picking_service", "inventory_service"}, f.menuUpdates[0])
	require.Len(t, f.creates, 1)
	assert.Equal(t, []string{"inventory_service", "picking_service"}, f.creates[0].GetRoleKeys(),
		"created with the requested roles, deduplicated")
	assert.Equal(t, resolver.TenantA.String(), f.creates[0].GetOrganizationId())
	assert.Empty(t, f.authzWrites)
}

// An org without a project grant on the pyck project cannot carry any role:
// the request is refused with the tenant-level error, and nothing is
// written.
func TestAssignRoles_RefusesAnOrgWithoutAProjectGrant(t *testing.T) {
	t.Parallel()

	f := &fakeGrants{authzRoles: []string{"admin"}}
	te, args := assignRolesEnv(t, f, "inventory_service")
	defer te.Close(t)

	execErr(te, te.ctx(userA), assignRolesMut, args, "tenant has no project grant")

	assert.Empty(t, f.menuUpdates)
	assert.Empty(t, f.authzWrites)
	assert.Empty(t, f.creates)
}

// A menu write Zitadel refuses stops the assignment before the user's
// authorization is touched: the user must not be granted a role the menu
// does not carry.
func TestAssignRoles_MenuWriteFailureGrantsNothing(t *testing.T) {
	t.Parallel()

	f := &fakeGrants{
		menu:       []string{"admin"},
		authzRoles: []string{"admin"},
		updateErr:  status.Error(codes.PermissionDenied, "Errors.Project.Grant.NoPermission"),
	}
	te, args := assignRolesEnv(t, f, "inventory_service")
	defer te.Close(t)

	execErr(te, te.ctx(userA), assignRolesMut, args, "extending project grant")

	assert.Empty(t, f.authzWrites)
	assert.Empty(t, f.creates)
}

// Only the tenant's admin may assign roles; a writer is refused before any
// Zitadel call.
func TestAssignRoles_RefusesANonAdmin(t *testing.T) {
	t.Parallel()

	f := &fakeGrants{menu: []string{"admin"}, authzRoles: []string{"admin"}}
	te, args := assignRolesEnv(t, f, "inventory_service")
	defer te.Close(t)

	execErr(te, te.ctx(userAWriter), assignRolesMut, args, "admin role required")

	assert.Empty(t, f.menuUpdates)
	assert.Empty(t, f.authzWrites)
	assert.Empty(t, f.creates)
}
