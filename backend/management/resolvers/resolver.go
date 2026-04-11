package resolvers

import (
	"github.com/99designs/gqlgen/graphql"
	"github.com/pyck-ai/pyck/backend/common/validator"
	"github.com/pyck-ai/pyck/backend/common/workflow"
	m "github.com/pyck-ai/pyck/backend/management"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName    string
	client         *ent.Client
	workflowClient *workflow.Client
	validator      *validator.Validator
}

// NewResolver creates a new resolver instance
func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, workflowClient *workflow.Client) *Resolver {
	return &Resolver{
		serviceName:    serviceName,
		client:         client,
		workflowClient: workflowClient,
		validator:      validator,
	}
}

// NewSchema creates a graphql executable schema.
func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return m.NewExecutableSchema(m.Config{
		Resolvers: resolver,
	})
}
