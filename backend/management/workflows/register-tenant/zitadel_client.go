package registertenant

import (
	"context"
	"fmt"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	authzv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	permv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/message"
	orgv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/dial"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"

	"github.com/pyck-ai/pyck/backend/management/core"
)

// zitadelClients bundles the generated Zitadel v2 service clients the
// provisioning steps call, all multiplexed over one gRPC connection. The
// generated interfaces are also the test seam: a test builds the bundle
// from fakes that embed the interface and override only the methods the
// step under test drives, so the requests an activity sends are asserted
// as the protobufs that go on the wire. No client is org-scoped: every
// v2 request names its organization explicitly.
type zitadelClients struct {
	users    userv2.UserServiceClient
	orgs     orgv2.OrganizationServiceClient
	projects projectv2.ProjectServiceClient
	authz    authzv2.AuthorizationServiceClient
	perms    permv2.InternalPermissionServiceClient

	// close releases the underlying connection; a test-built bundle has
	// none and records the release instead, so the per-step defer stays
	// observable.
	close func(ctx context.Context)
}

// grants serves the shared read-union-replace grant helpers from the
// bundle's project and authorization clients, so a test-built bundle sees
// the requests they send.
func (c *zitadelClients) grants() *sdk.GrantClient {
	return sdk.NewGrantClientOver(c.projects, c.authz)
}

// Close releases the bundle's connection. Failing to close only leaks,
// so the error is logged rather than propagated.
func (c *zitadelClients) Close(ctx context.Context) {
	if c.close != nil {
		c.close(ctx)
	}
}

// dialZitadel opens one gRPC connection to the Zitadel of core.Config
// with the service account's JWT-profile credentials and serves every v2
// service client from it. One dial per activity attempt, released by the
// activity's defer: a step is a handful of round-trips, and holding a
// connection across attempts would tie its credential refresh to the
// worker's lifetime instead of the step's.
func dialZitadel(ctx context.Context) (*zitadelClients, error) {
	options := dial.ConnectionOptions(ctx,
		core.Config.ZitadelOAuthURL, core.Config.ZitadelAudience, core.Config.ZitadelServiceKeyPath,
		core.Config.ZitadelTlsInsecure)
	conn, err := zitadel.NewConnection(ctx, core.Config.ZitadelAudience, core.Config.ZitadelGrpcAddr,
		[]string{oidc.ScopeOpenID, "urn:zitadel:iam:org:project:id:zitadel:aud"},
		options...,
	)
	if err != nil {
		return nil, fmt.Errorf("connect to zitadel: %w", err)
	}

	return &zitadelClients{
		users:    userv2.NewUserServiceClient(conn),
		orgs:     orgv2.NewOrganizationServiceClient(conn),
		projects: projectv2.NewProjectServiceClient(conn),
		authz:    authzv2.NewAuthorizationServiceClient(conn),
		perms:    permv2.NewInternalPermissionServiceClient(conn),
		close: func(ctx context.Context) {
			if err := conn.Close(); err != nil {
				log.ForContext(ctx).Warn().Err(err).
					Str("component", "zitadel-client").
					Msg("could not close grpc connection")
			}
		},
	}, nil
}

// projectionLag reports whether err is Zitadel answering for an object its
// eventstore already holds but its projection tables do not yet. A command
// validates its preconditions against the projections, so a request naming
// an organization or user created moments earlier is answered NotFound or
// FailedPrecondition until the projection catches up, and the identical
// request succeeds once it has. The answer is only meaningful for an object
// this run wrote: for any other object the same two codes are the settled
// refusal they look like.
func projectionLag(err error) bool {
	switch status.Code(err) {
	case codes.NotFound, codes.FailedPrecondition:
		return true
	default:
		return false
	}
}

// zitadelErrIDHashUnsupported is the ID of Zitadel's refusal of a password
// hash whose encoding none of its configured verifiers accepts.
const zitadelErrIDHashUnsupported = "USER-JDk4t"

// zitadelErrorID returns the ID Zitadel attaches to a gRPC error as a
// message.ErrorDetail (the "(USER-JDk4t)" its message ends with), or ""
// for an error that carries none: a non-gRPC error included, which
// status.Convert turns into an Unknown status without details. The ID
// tells two refusals with the same status code apart without parsing the
// localised message.
func zitadelErrorID(err error) string {
	for _, d := range status.Convert(err).Details() {
		if detail, ok := d.(*message.ErrorDetail); ok {
			return detail.GetId()
		}
	}
	return ""
}
