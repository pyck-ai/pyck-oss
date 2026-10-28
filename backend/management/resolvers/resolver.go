package resolvers

import (
	"context"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	"golang.org/x/crypto/bcrypt"

	"github.com/pyck-ai/pyck/backend/common/validator"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/exec"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName    string
	client         *ent.Client
	workflowClient *workflow.Client
	validator      *validator.Validator
	// registrationWait and registrationPoll bound how long registerTenant
	// waits for the registration run, and how often it re-asks Temporal
	// for a run that does not exist yet. Immutable after construction
	// (tests shorten them).
	registrationWait time.Duration
	registrationPoll time.Duration
	// hashPassword hashes the admin password registerTenant stores on the
	// pending row (bcrypt.GenerateFromPassword in production). Immutable
	// after construction (tests make it fail: no request input reaches
	// bcrypt's own refusals once the password rule has passed).
	hashPassword func(password []byte, cost int) ([]byte, error)
	// zitadelConn is the shared system service-account gRPC connection
	// used by the service-role resolvers (assignRoles/removeRoles and the
	// project-grant menu behind them); the tenant-lifecycle workflows
	// hold their own reference. The auth path never uses it: tenant
	// liveness is answered from the tenant table.
	zitadelConn *zitadelsdk.Connection
	// eventStreams puts the stream of a tenant being registered or restored.
	eventStreams TenantStreams
}

// TenantStreams creates and deletes a tenant's event stream. It is implemented
// by *events.StreamTopology.
type TenantStreams interface {
	EnsureTenant(ctx context.Context, tenantID uuid.UUID) error
	DeleteTenant(ctx context.Context, tenantID uuid.UUID) error
}

// NewResolver creates a new resolver instance
func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, workflowClient *workflow.Client, zitadelConn *zitadelsdk.Connection, eventStreams TenantStreams) *Resolver {
	return &Resolver{
		serviceName:      serviceName,
		client:           client,
		workflowClient:   workflowClient,
		validator:        validator,
		zitadelConn:      zitadelConn,
		registrationWait: core.Config.RegistrationWait,
		registrationPoll: core.Config.RegistrationPoll,
		hashPassword:     bcrypt.GenerateFromPassword,
		eventStreams:     eventStreams,
	}
}

// NewSchema creates a graphql executable schema.
func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return exec.NewExecutableSchema(exec.Config{
		Resolvers: resolver,
	})
}
