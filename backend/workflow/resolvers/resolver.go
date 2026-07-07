package resolvers

import (
	"github.com/99designs/gqlgen/graphql"

	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/exec"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName    string
	client         *ent.Client
	validator      *validator.Validator
	workflowRouter *services.SignalRouter
}

func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, workflowRouter *services.SignalRouter) *Resolver {
	return &Resolver{
		serviceName:    serviceName,
		client:         client,
		validator:      validator,
		workflowRouter: workflowRouter,
	}
}

func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return exec.NewExecutableSchema(exec.Config{
		Resolvers: resolver,
	})
}
