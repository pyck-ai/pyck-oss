package resolvers

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/std"
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
// flavour, falling back per platform to the system-wide default template.
// Results are cached; a tenant without templates, or not found, is negatively
// cached for a shorter TTL. Concurrent cold lookups of one tenant share a
// single management call (std.SharedCall).
func (r *Resolver) tenantUITemplates(ctx context.Context, tenantID uuid.UUID) (tenantUI, error) {
	id := tenantID.String()
	if t, ok, err := r.cachedTenantUITemplates(id); ok {
		return t, err
	}
	return std.SharedCall(ctx, &r.tenantFlight, id, tenantTemplateLookupTimeout, func(callCtx context.Context) (tenantUI, error) {
		if t, ok, err := r.cachedTenantUITemplates(id); ok {
			return t, err
		}
		return r.fetchTenantUITemplates(callCtx, tenantID)
	})
}

// cachedTenantUITemplates returns the memoized templates, or the memoized
// negative verdict as err. ok is false on a miss.
func (r *Resolver) cachedTenantUITemplates(id string) (t tenantUI, ok bool, err error) {
	cached, _ := r.tenantTemplates.Get(id)
	switch v := cached.(type) {
	case tenantUI:
		return v, true, nil
	case error:
		return tenantUI{}, true, v
	default:
		return tenantUI{}, false, nil
	}
}

// fetchTenantUITemplates is the uncached body of tenantUITemplates. It stores
// its own outcome; a transport failure is not stored.
func (r *Resolver) fetchTenantUITemplates(ctx context.Context, tenantID uuid.UUID) (tenantUI, error) {
	id := tenantID.String()

	first := 1
	resp, err := r.mgmtClient.GetTenants(ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{ID: &id},
	})
	if err != nil {
		return tenantUI{}, fmt.Errorf("fetch tenant UI templates: %w", err)
	}

	edges := resp.GetTenants().GetEdges()
	if len(edges) == 0 || edges[0].GetNode() == nil {
		err := fmt.Errorf("%w: %s", ErrTenantNotFound, id)
		r.tenantTemplates.Set(id, err, tenantTemplateNegativeCacheTTL)
		return tenantUI{}, err
	}
	node := edges[0].GetNode()

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
		err := fmt.Errorf("%w: tenant %s", ErrTenantUITemplatesNotSet, id)
		r.tenantTemplates.Set(id, err, tenantTemplateNegativeCacheTTL)
		return tenantUI{}, err
	}

	t := tenantUI{
		Templates: commonworkflow.UIBundleTemplate{Web: web, Mobile: mobile},
		Flavour:   commonworkflow.DetectFlavour(node.Data),
	}
	r.tenantTemplates.Set(id, t, tenantTemplateCacheTTL)
	return t, nil
}

// executionTenantID returns the tenant a listed execution belongs to, from its
// pyck_tenant_id search attribute (the listing filters on it, so it is set).
func executionTenantID(exec *model.WorkflowExecutionInfo) (uuid.UUID, error) {
	id, err := uuid.Parse(searchAttributeValue(exec, commonworkflow.PyckTenantIDKey))
	if err != nil {
		workflowID := ""
		if exec.Execution != nil {
			workflowID = exec.Execution.WorkflowID
		}
		return uuid.Nil, fmt.Errorf("%w: execution %q", ErrExecutionTenantUnknown, workflowID)
	}
	return id, nil
}

// executionDeploymentVersion returns the Worker Deployment Version a listed
// execution is pinned to, or nil when it is unversioned.
func executionDeploymentVersion(exec *model.WorkflowExecutionInfo) *commonworkflow.DeploymentVersionRef {
	ref, ok := commonworkflow.ParseDeploymentVersionSA(
		searchAttributeValue(exec, commonworkflow.TemporalWorkerDeploymentVersionKey),
		searchAttributeValue(exec, commonworkflow.TemporalWorkerDeploymentKey),
	)
	if !ok {
		return nil
	}
	return &ref
}

// resolveExecutionRemoteUI is the fallible body of WorkflowExecutionInfo.remoteUI.
func (r *workflowExecutionInfoResolver) resolveExecutionRemoteUI(ctx context.Context, obj *model.WorkflowExecutionInfo) (*commonworkflow.UIBundleURLs, error) {
	tenantID, err := executionTenantID(obj)
	if err != nil {
		return nil, err
	}
	// The listing is tenant-filtered already; never render outside the request
	// scope regardless.
	if !slices.Contains(request.ForContext(ctx).TenantIDs(), tenantID) {
		return nil, fmt.Errorf("%w: %s is outside the request scope", ErrExecutionTenantUnknown, tenantID)
	}

	workflowClient, err := r.workflowRouter.GetClient(ctx, tenantID.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidWorkflowClient, err)
	}
	if workflowClient == nil {
		return nil, ErrInvalidWorkflowClient
	}

	tenant, err := r.tenantUITemplates(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	workflowType := ""
	if obj.Type != nil {
		workflowType = obj.Type.Name
	}
	render := commonworkflow.UITemplateContext{
		TenantID: tenantID.String(),
		Flavour:  tenant.Flavour,
		Env:      r.remoteUIDefaults.Env,
	}
	return workflowClient.RenderVersionRemoteUI(ctx, executionDeploymentVersion(obj), workflowType, tenant.Templates, render, r.remoteUIDefaults.Bundle)
}

// logRemoteUIFailure records why a listed execution's remoteUI is null: debug
// when there is simply no UI to load or the caller went away, warn for a
// tenant-configuration problem, error for an infrastructure failure or a
// broken invariant.
func logRemoteUIFailure(ctx context.Context, obj *model.WorkflowExecutionInfo, err error) {
	workflowID := ""
	if obj.Execution != nil {
		workflowID = obj.Execution.WorkflowID
	}
	level := zerolog.ErrorLevel
	switch {
	case errors.Is(err, commonworkflow.ErrNoDeploymentVersion),
		errors.Is(err, commonworkflow.ErrUIBundleMetadataMissing),
		errors.Is(err, ErrTenantUITemplatesNotSet),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		level = zerolog.DebugLevel
	case errors.Is(err, commonworkflow.ErrRenderedURLInvalid),
		errors.Is(err, commonworkflow.ErrInvalidUIBundleValue),
		errors.Is(err, ErrTenantNotFound):
		level = zerolog.WarnLevel
	}
	log.ForContext(ctx).WithLevel(level).Err(err).Str("workflow_id", workflowID).Msg("remoteUI: listed execution has no remote UI")
}
