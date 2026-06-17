package resolvers

import (
	"github.com/99designs/gqlgen/graphql"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/backend/common/validator"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/exec"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName    string
	client         *ent.Client
	workflowClient *workflow.Client
	validator      *validator.Validator
	// zitadelConn is the shared system service-account gRPC connection
	// used by tenant-lifecycle workflows AND by the organization
	// resolver for the v2 SDK lookups (user/v2.GetUserByID +
	// org/v2.ListOrganizations).
	zitadelConn *zitadelsdk.Connection
}

// NewResolver creates a new resolver instance
func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, workflowClient *workflow.Client, zitadelConn *zitadelsdk.Connection) *Resolver {
	return &Resolver{
		serviceName:    serviceName,
		client:         client,
		workflowClient: workflowClient,
		validator:      validator,
		zitadelConn:    zitadelConn,
	}
}

// NewSchema creates a graphql executable schema.
func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return exec.NewExecutableSchema(exec.Config{
		Resolvers: resolver,
	})
}
