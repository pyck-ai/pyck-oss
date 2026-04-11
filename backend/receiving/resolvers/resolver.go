package resolvers

import (
	"github.com/99designs/gqlgen/graphql"
	"github.com/pyck-ai/pyck/backend/common/validator"
	m "github.com/pyck-ai/pyck/backend/receiving"
	ent "github.com/pyck-ai/pyck/backend/receiving/ent/gen"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName string
	client      *ent.Client
	validator   *validator.Validator
}

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
