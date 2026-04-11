package resolvers

import (
	"github.com/pyck-ai/pyck/backend/common/validator"
	m "github.com/pyck-ai/pyck/backend/main-data"
	ent "github.com/pyck-ai/pyck/backend/main-data/ent/gen"

	"github.com/99designs/gqlgen/graphql"
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
	return m.NewExecutableSchema(m.Config{
		Resolvers: resolver,
	})
}
