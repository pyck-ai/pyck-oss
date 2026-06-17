package resolvers

import (
	"github.com/99designs/gqlgen/graphql"

	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/exec"
	"github.com/pyck-ai/pyck/backend/inventory/service/stock"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName string
	client      *ent.Client
	validator   *validator.Validator
	stock       stock.Service
}

// NewResolver creates a new resolver instance.
func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, stockService stock.Service) *Resolver {
	return &Resolver{
		serviceName: serviceName,
		client:      client,
		validator:   validator,
		stock:       stockService,
	}
}

// NewSchema creates a graphql executable schema.
func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return exec.NewExecutableSchema(exec.Config{
		Resolvers: resolver,
	})
}
