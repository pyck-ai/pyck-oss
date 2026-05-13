package resolvers

import (
	"github.com/99designs/gqlgen/graphql"

	"github.com/pyck-ai/pyck/backend/common/validator"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	m "github.com/pyck-ai/pyck/backend/file"
	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	"github.com/pyck-ai/pyck/backend/file/services"
)

// Resolver is the resolver root.
type Resolver struct {
	serviceName    string
	client         *ent.Client
	validator      *validator.Validator
	s3Storage      *services.S3StorageService
	workflowClient *workflow.Client
}

func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, s3Storage *services.S3StorageService, workflowClient *workflow.Client) *Resolver {
	return &Resolver{
		serviceName:    serviceName,
		client:         client,
		validator:      validator,
		s3Storage:      s3Storage,
		workflowClient: workflowClient,
	}
}

func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return m.NewExecutableSchema(m.Config{
		Resolvers: resolver,
	})
}
