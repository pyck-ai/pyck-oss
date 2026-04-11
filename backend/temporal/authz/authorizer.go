package authz

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/log"
	"go.temporal.io/server/common/api"
	"go.temporal.io/server/common/authorization"
	"google.golang.org/grpc/health/grpc_health_v1"
	"lab.nexedi.com/kirr/go123/xcontext"
)

type acl struct {
	Disabled     bool
	Public       bool
	RequiredRole authorization.Role
}

var aclOverride = map[string]acl{
	// public
	grpc_health_v1.Health_Check_FullMethodName:  {Public: true},
	api.WorkflowServicePrefix + "GetSystemInfo": {Public: true},
	// disabled
	api.NexusServicePrefix:                           {Disabled: true},
	api.WorkflowServicePrefix + "RegisterNamespace":  {Disabled: true},
	api.WorkflowServicePrefix + "UpdateNamespace":    {Disabled: true},
	api.WorkflowServicePrefix + "DeprecateNamespace": {Disabled: true},
	api.OperatorServicePrefix + "DeleteNamespace":    {Disabled: true},
	// any authenticated user
	api.WorkflowServicePrefix + "GetClusterInfo":      {RequiredRole: authorization.RoleUndefined},
	api.WorkflowServicePrefix + "GetSearchAttributes": {RequiredRole: authorization.RoleUndefined},
	api.WorkflowServicePrefix + "ListNamespaces":      {RequiredRole: authorization.RoleUndefined},
}

// NewAuthorizer returns an authorization.Authorizer that wraps the provided
// baseAuthorizer. If baseAuthorizer is nil, a no-op authorizer is used. The
// returned authorizer adds logging and enforces Pyck-specific ACL overrides.
//
// It expects the claims to be mapped using a ClaimMapper that produces
// Pyck-specific claims (i.e. using PyckRole and ClaimMapperExtensions).
//
// The following ACL overrides are applied:
// - /grpc.health.v1.Health/Check is public
// - /temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo is public
// - /temporal.api.nexussservice.v1.NexusService/* is disabled
// - /temporal.api.workflowservice.v1.WorkflowService/RegisterNamespace is disabled
// - /temporal.api.workflowservice.v1.WorkflowService/UpdateNamespace is disabled
// - /temporal.api.workflowservice.v1.WorkflowService/DeprecateNamespace is disabled
// - /temporal.api.operatorservice.v1.OperatorService/DeleteNamespace is disabled
// - /temporal.api.workflowservice.v1.WorkflowService/GetClusterInfo requires authentication
// - /temporal.api.workflowservice.v1.WorkflowService/GetSearchAttributes requires authentication
// - /temporal.api.workflowservice.v1.WorkflowService/ListNamespaces requires authentication
//
// All other APIs are authorized using the provided baseAuthorizer.
func NewAuthorizer(ctx context.Context, baseAuthorizer authorization.Authorizer) *authorizer {
	if baseAuthorizer == nil {
		baseAuthorizer = authorization.NewNoopAuthorizer()
	}

	return &authorizer{
		contextBase: func() context.Context { return ctx },
		base:        baseAuthorizer,
	}
}

type authorizer struct {
	contextBase func() context.Context
	base        authorization.Authorizer
}

var _ authorization.Authorizer = (*authorizer)(nil)

func (a *authorizer) Authorize(ctx context.Context, claims *authorization.Claims, target *authorization.CallTarget) (result authorization.Result, err error) {
	ctx, _ = xcontext.Merge(ctx, a.contextBase())

	ext := GetClaimExtensions(claims)

	if ext == nil {
		// GetClaimExtensions returns nil if our custom claim mapper did not
		// run. This usually means we are dealing with a internal-frontend call,
		// so we just fall back to the base authorizer. We also do not log
		// anything in this case, as internal calls are expected to not have any
		// restrictions.
		return a.base.Authorize(ctx, claims, target)
	}
	fields := []any{
		"api", target.APIName,
	}

	defer func() {
		if err != nil {
			log.ForContext(ctx).Error().
				Err(err).
				Fields(fields).
				Msg("authorization error")
			return
		}

		if result.Reason != "" {
			fields = append(fields, "reason", result.Reason)
		}

		switch result.Decision {
		case authorization.DecisionAllow:
			log.ForContext(ctx).Debug().
				Fields(fields).
				Msg("authorization granted")
		case authorization.DecisionDeny:
			log.ForContext(ctx).Warn().
				Fields(fields).
				Msg("authorization denied")
		default:
			break
		}
	}()

	if r, ok := target.Request.(interface{ GetRequestId() string }); ok {
		fields = append(fields, "request-id", r.GetRequestId())
	}

	// Some requests allow overriding the namespace via the request payload.
	// If the request has a GetNamespace method, use it to set the target
	// namespace for authorization.
	if nsGetter, ok := target.Request.(interface{ GetNamespace() string }); ok {
		target.Namespace = nsGetter.GetNamespace()
	}

	if target.Namespace != "" {
		fields = append(fields, "namespace", target.Namespace)
	}

	if claims != nil {
		roles := make([]string, 0, len(claims.Namespaces)+1)
		roles = append(roles, "*="+PyckRole(claims.System).String())

		for ns, role := range claims.Namespaces {
			roles = append(roles, ns+"="+PyckRole(role).String())
		}

		fields = append(fields, "roles", roles)
	}

	if ext.User.IsAuthenticated() {
		fields = append(fields, "user-id", ext.User.ID)
		fields = append(fields, "user-tenant-id", ext.User.TenantID)
	}

	override, ok := aclOverride[target.APIName]
	if !ok {
		override, ok = aclOverride[api.ServiceName(target.APIName)]
	}

	if !ok {
		// there are no overrides for this API; fall back to base authorizer
		return a.base.Authorize(ctx, claims, target)
	}

	// deny disabled APIs
	if override.Disabled {
		return authorization.Result{
			Decision: authorization.DecisionDeny,
			Reason:   "API is disabled",
		}, nil
	}

	// allow public APIs
	if override.Public {
		return authorization.Result{
			Decision: authorization.DecisionAllow,
			Reason:   "API is public",
		}, nil
	}

	// deny if user is not authenticated or has no claims
	if claims == nil || !ext.User.IsAuthenticated() {
		return authorization.Result{
			Decision: authorization.DecisionDeny,
			Reason:   "User is not authenticated",
		}, nil
	}

	// allow APIs without role requirement
	if override.RequiredRole == authorization.RoleUndefined {
		return authorization.Result{
			Decision: authorization.DecisionAllow,
			Reason:   "API has no role requirement",
		}, nil
	}

	// allow APIs if user has the required system role
	if claims.System&override.RequiredRole == override.RequiredRole {
		return authorization.Result{
			Decision: authorization.DecisionAllow,
			Reason:   "User has required system role",
		}, nil
	}

	// allow APIs if user has required namespace role
	if target.Namespace != "" {
		if role, ok := claims.Namespaces[target.Namespace]; ok {
			if role&override.RequiredRole == override.RequiredRole {
				return authorization.Result{
					Decision: authorization.DecisionAllow,
					Reason:   "User has required namespace role",
				}, nil
			}
		}
	}

	// Deny by default
	return authorization.Result{
		Decision: authorization.DecisionDeny,
		Reason:   "User does not have required role",
	}, nil
}
