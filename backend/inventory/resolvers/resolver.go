package resolvers

import (
	"errors"

	"github.com/99designs/gqlgen/graphql"

	"github.com/pyck-ai/pyck/backend/common/validator"

	m "github.com/pyck-ai/pyck/backend/inventory"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/services"
)

// ErrDeleteExecutedMovement is returned when attempting to delete an executed movement.
var ErrDeleteExecutedMovement = errors.New("cannot delete an executed movement")

// ErrCollectionHasMovements is returned when attempting to delete a collection that still has movements.
var ErrCollectionHasMovements = errors.New("cannot delete a collection that still has movements")

// Resolver is the resolver root.
type Resolver struct {
	serviceName           string
	client                *ent.Client
	validator             *validator.Validator
	inventoryStockService *services.InventoryStockService
}

// NewResolver creates a new resolver instance.
func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, inventoryStockService *services.InventoryStockService) *Resolver {
	return &Resolver{
		serviceName:           serviceName,
		client:                client,
		validator:             validator,
		inventoryStockService: inventoryStockService,
	}
}

// NewSchema creates a graphql executable schema.
func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return m.NewExecutableSchema(m.Config{
		Resolvers: resolver,
	})
}
