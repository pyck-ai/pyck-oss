package resolvers

import (
	"time"

	"github.com/99designs/gqlgen/graphql"

	"github.com/pyck-ai/pyck/backend/common/memkv"
	"github.com/pyck-ai/pyck/backend/common/validator"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/exec"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// tenantTemplateCacheTTL bounds how stale a cached tenant UI template may be.
// Templates change very rarely (system-role mutation only), so a short TTL keeps
// the remoteUI hot path off the management service without risking staleness.
const tenantTemplateCacheTTL = 5 * time.Minute

// RemoteUIDefaults are the system-wide fallbacks for per-workflow UI bundle
// resolution, sourced from service config. A zero value disables both fallbacks.
type RemoteUIDefaults struct {
	// Templates is the default web/mobile URL template used when a tenant has none
	// stored. These are Go templates rendered with the full UITemplateContext
	// ({{.Slug}}/{{.Version}}/{{.TenantID}}/{{.Flavour}}/{{.Env}}) and may branch
	// on {{if .Flavour}}. Empty fields disable that platform's fallback.
	Templates commonworkflow.UIBundleTemplate
	// Bundle is the default slug/version served when an execution has no readable
	// bundle on its pinned version. Nil disables the fallback.
	Bundle *commonworkflow.UIBundle
	// Env is the environment name exposed to default templates as {{.Env}}.
	Env string
}

// Resolver is the resolver root.
type Resolver struct {
	serviceName      string
	client           *ent.Client
	validator        *validator.Validator
	workflowRouter   *services.SignalRouter
	mgmtClient       managementapi.Client
	remoteUIDefaults RemoteUIDefaults
	tenantTemplates  *memkv.InMemoryKVStore
}

func NewResolver(serviceName string, client *ent.Client, validator *validator.Validator, workflowRouter *services.SignalRouter, mgmtClient managementapi.Client, remoteUIDefaults RemoteUIDefaults) *Resolver {
	return &Resolver{
		serviceName:      serviceName,
		client:           client,
		validator:        validator,
		workflowRouter:   workflowRouter,
		mgmtClient:       mgmtClient,
		remoteUIDefaults: remoteUIDefaults,
		tenantTemplates:  memkv.NewInMemoryKVStore(tenantTemplateCacheTTL),
	}
}

func NewSchema(resolver *Resolver) graphql.ExecutableSchema {
	return exec.NewExecutableSchema(exec.Config{
		Resolvers: resolver,
	})
}
