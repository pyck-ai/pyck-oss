package authz

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/log"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/common/authorization"
	"google.golang.org/grpc"
	"lab.nexedi.com/kirr/go123/xcontext"
)

// NewNamespaceFilter returns a gRPC interceptor that filters namespaces in
// ListNamespaces responses based on the user's claims. System users see all
// namespaces, authenticated users see only namespaces they have at least
// reader access to.
func NewNamespaceFilter(ctx context.Context) grpc.UnaryServerInterceptor {
	return func(reqCtx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		reqCtx, _ = xcontext.Merge(reqCtx, ctx)

		interceptor := &namespaceFrontendInterceptor{}

		return interceptor.Handle(reqCtx, req, info, next)
	}
}

type namespaceFrontendInterceptor struct {
}

func (i *namespaceFrontendInterceptor) Handle(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (interface{}, error) {
	switch req := req.(type) {
	case *workflowservice.ListNamespacesRequest:
		r, err := next(ctx, req)
		if err != nil {
			return nil, err
		}

		resp, ok := r.(*workflowservice.ListNamespacesResponse)
		if !ok {
			// this should never happen
			panic("expected ListNamespacesResponse")
		}

		// Filter the namespaces in the response
		return i.filterListNamespacesRequest(ctx, resp), nil
	default:
		return next(ctx, req)
	}
}

func (i *namespaceFrontendInterceptor) filterListNamespacesRequest(ctx context.Context, resp *workflowservice.ListNamespacesResponse) *workflowservice.ListNamespacesResponse {
	claims := GetClaims(ctx)
	ext := GetClaimExtensions(claims)

	if ext == nil {
		// GetClaimExtensions returns nil if our custom claim mapper did not
		// run. This usually means we are dealing with a internal-frontend call,
		// so we just return the unmodified response.
		return resp
	}

	if user := GetUser(claims); user.IsSystemUser() {
		// system users see all namespaces
		return resp
	}

	allNamespaces := resp.GetNamespaces()
	filteredNamespaces := make([]*workflowservice.DescribeNamespaceResponse, 0, len(allNamespaces))

	// TODO(michael): This blindly removes entries from the response, which will
	// lead to issues with pagination... A better approach would be to
	// re-implement the ListNamespaces logic here, but that would require access
	// to the persistence layer. For now, we just log a warning if we detect that
	// the response is paginated and entries were filtered out...

	for _, ns := range resp.GetNamespaces() {
		nsinfo := ns.GetNamespaceInfo()
		if nsinfo == nil {
			continue
		}

		nsname := nsinfo.GetName()
		if nsname == "" {
			continue
		}

		role, ok := claims.Namespaces[nsname]
		if !ok || role&authorization.RoleReader != authorization.RoleReader {
			// hide, if user does not have reader role for this namespace
			continue
		}

		filteredNamespaces = append(filteredNamespaces, ns)
	}

	if len(filteredNamespaces) == len(allNamespaces) {
		// nothing was filtered, return original response
		return resp
	}

	if len(filteredNamespaces) == 0 {
		// nothing left, return empty response
		return &workflowservice.ListNamespacesResponse{}
	}

	// return filtered response
	resp.Namespaces = filteredNamespaces

	if resp.NextPageToken != nil && len(filteredNamespaces) < len(allNamespaces) {
		log.ForContext(ctx).Warn().
			Msg("ListNamespacesResponse is paginated, but namespace filtering is enabled. This may lead to incomplete results.")
	}

	return resp
}
