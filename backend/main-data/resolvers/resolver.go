package resolvers

import (
	"github.com/99designs/gqlgen/graphql"

	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/main-data/ent/gen"
	"github.com/pyck-ai/pyck/backend/main-data/exec"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName string
	client      *ent.Client
	validator   *validator.Validator
}

// NewResolver creates a new resolver.
func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator) *Resolver {
	return &Resolver{
		serviceName: serviceName,
		client:      client,
		validator:   validator,
	}
}

func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return exec.NewExecutableSchema(exec.Config{
		Resolvers: resolver,
	})
}
