package resolvers

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/request"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/backend/workflow/model"
)

// versionCursor is the opaque pagination cursor for a deployment version; the
// (deployment, build) pair is unique within a namespace.
func versionCursor(v commonworkflow.DeploymentVersionUI) string {
	return v.DeploymentName + "/" + v.BuildID
}

// paginateDeploymentVersionUIBundles applies forward Relay-style pagination over
// the full (already-fetched, cached) listing in memory. Deployment versions
// number in the tens, so slicing the cached slice beats paging Temporal.
func paginateDeploymentVersionUIBundles(versions []commonworkflow.DeploymentVersionUI, first *int, after *string) *model.DeploymentVersionUIConnection {
	size := deploymentVersionDefaultPageSize
	if first != nil && *first > 0 {
		size = *first
	}
	if size > deploymentVersionMaxPageSize {
		size = deploymentVersionMaxPageSize
	}

	start := 0
	if after != nil && *after != "" {
		// An unknown cursor (the cached listing changed between pages, or the
		// version was retired) yields an empty page rather than silently
		// restarting from page 1.
		start = len(versions)
		for i, v := range versions {
			if versionCursor(v) == *after {
				start = i + 1
				break
			}
		}
	}
	end := start + size
	if end > len(versions) {
		end = len(versions)
	}
	page := versions[start:end]

	edges := make([]*model.DeploymentVersionUIEdge, len(page))
	for i := range page {
		v := page[i]
		edges[i] = &model.DeploymentVersionUIEdge{Node: &v, Cursor: versionCursor(v)}
	}

	pageInfo := &model.DeploymentVersionUIPageInfo{
		HasNextPage:     end < len(versions),
		HasPreviousPage: start > 0,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.DeploymentVersionUIConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: len(versions),
	}
}

// singleTenantID resolves the one tenant a namespace-scoped query targets.
//
// These resolvers must run against a single Temporal namespace. Unlike the
// mutation path's MutationTenantID(), this returns a clean error instead of
// panicking when the request carries zero or multiple tenant IDs.
func singleTenantID(req request.RequestContext) (uuid.UUID, error) {
	ids := req.TenantIDs()
	if len(ids) != 1 {
		return uuid.Nil, ErrSingleTenantRequired
	}
	return ids[0], nil
}

// Tenant.data keys written by management's setTenantUITemplate mutation, holding
// URL templates with {{.Slug}}/{{.Version}} placeholders this service renders into
// final URLs. Defined in common/workflow so both sides share one definition of
// the wire contract.
const (
	tenantWebUITemplateKey    = commonworkflow.RemoteWebUITemplateKey
	tenantMobileUITemplateKey = commonworkflow.RemoteMobileUITemplateKey

	// Page sizing for workerDeploymentUIBundles.
	deploymentVersionDefaultPageSize = 50
	deploymentVersionMaxPageSize     = 200
)

// tenantUI bundles the resolved templates and the tenant's flavour, cached
// together so the remoteUI hot path does not hit the management service per query.
type tenantUI struct {
	Templates commonworkflow.UIBundleTemplate
	Flavour   string
}

// tenantUITemplates resolves the tenant's web/mobile UI bundle URL templates and
// flavour. The per-tenant template is an optional override; when absent it falls
// back per-platform to the system-wide default (a tenant-aware template rendered
// with TenantID/Flavour/Env at query time). Results are cached (short TTL).
func (r *Resolver) tenantUITemplates(ctx context.Context, tenantID uuid.UUID) (tenantUI, error) {
	id := tenantID.String()

	if cached, ok := r.tenantTemplates.Get(id); ok {
		if t, ok := cached.(tenantUI); ok {
			return t, nil
		}
	}

	first := 1
	resp, err := r.mgmtClient.GetTenants(ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{ID: &id},
	})
	if err != nil {
		return tenantUI{}, fmt.Errorf("fetch tenant UI templates: %w", err)
	}

	edges := resp.GetTenants().GetEdges()
	if len(edges) == 0 {
		return tenantUI{}, fmt.Errorf("%w: %s", ErrTenantNotFound, id)
	}
	node := edges[0].GetNode()
	if node == nil {
		return tenantUI{}, fmt.Errorf("%w: %s", ErrTenantNotFound, id)
	}

	web, _ := node.Data[tenantWebUITemplateKey].(string)
	mobile, _ := node.Data[tenantMobileUITemplateKey].(string)

	// Per-tenant override is optional; fall back per-platform to the system-wide
	// default. tenant.Data holds only explicit overrides, so a cleared override
	// durably falls back here (sync no longer re-derives templates — #1317).
	if web == "" {
		web = r.remoteUIDefaults.Templates.Web
	}
	if mobile == "" {
		mobile = r.remoteUIDefaults.Templates.Mobile
	}

	// Neither stored nor defaulted: unconfigured. Fail loudly rather than
	// resolving to blank URLs (and skip the Temporal round-trips downstream).
	if web == "" && mobile == "" {
		return tenantUI{}, fmt.Errorf("%w: tenant %s", ErrTenantUITemplatesNotSet, id)
	}

	t := tenantUI{
		Templates: commonworkflow.UIBundleTemplate{Web: web, Mobile: mobile},
		Flavour:   commonworkflow.DetectFlavour(node.Data),
	}
	r.tenantTemplates.Set(id, t, tenantTemplateCacheTTL)
	return t, nil
}
