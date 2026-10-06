package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"golang.org/x/sync/errgroup"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"
)

// ErrNoDeploymentVersion is returned when an execution is not pinned to a
// Worker Deployment Version — there is no version to read a UI bundle from.
// This is expected for executions started before worker deployment versioning
// was enabled, or on workers that do not opt into it.
var ErrNoDeploymentVersion = errors.New("execution has no pinned deployment version")

// ErrUIBundleMetadataMissing is returned when a deployment version carries no UI
// bundle metadata for the requested field (neither a per-type override nor the
// version-wide default).
var ErrUIBundleMetadataMissing = errors.New("UI bundle metadata not found on deployment version")

// ErrRemoteUIUnavailable is the client-facing error for infrastructure failures
// in the resolve path (the Temporal describe calls). The underlying error is
// logged server-side; this generic error keeps namespace/infra detail off the
// wire.
var ErrRemoteUIUnavailable = errors.New("remote UI temporarily unavailable")

// ErrInvalidUIBundleValue is returned when a render dimension is missing or not
// a single safe path segment.
var ErrInvalidUIBundleValue = errors.New("invalid UI bundle URL value")

// ErrRenderedURLInvalid is returned when a rendered UI bundle URL is not a
// well-formed absolute http(s) URL.
var ErrRenderedURLInvalid = errors.New("rendered UI bundle URL is invalid")

// UI bundle metadata keys stamped on a Temporal Worker Deployment Version.
// A version may host several workflow types; a per-type key overrides the
// version-wide default, so different workflow types in one deployment can ship
// different bundles (#1317).
const (
	uiBundleMetaPrefix = "ui.bundle."
	uiBundleSlugField  = "slug"
	uiBundleVerField   = "version"

	// RemoteWebUITemplateKey is the tenant.data key for the web UI bundle URL
	// template. Canonical home for the wire contract: management writes it
	// (setTenantUITemplate), the workflow service reads it.
	RemoteWebUITemplateKey = "remoteWebUITemplate"
	// RemoteMobileUITemplateKey is the tenant.data key for the mobile UI bundle
	// URL template.
	RemoteMobileUITemplateKey = "remoteMobileUITemplate"

	// TemporalWorkerDeploymentKey and TemporalWorkerDeploymentVersionKey are the
	// Temporal system search attributes naming the Worker Deployment Version an
	// execution runs on; visibility listings carry them.
	TemporalWorkerDeploymentKey        = "TemporalWorkerDeployment"
	TemporalWorkerDeploymentVersionKey = "TemporalWorkerDeploymentVersion"

	// deploymentVersionsCacheKey is the single cache key for the full listing
	// (one Client serves one namespace); deploymentVersionsCacheTTL bounds its
	// staleness.
	deploymentVersionsCacheKey = "deployment-version-ui-bundles"
	deploymentVersionsCacheTTL = 30 * time.Second

	// deploymentVersionDescribeConcurrency bounds the parallel per-version
	// DescribeVersion calls when (re)building the listing.
	deploymentVersionDescribeConcurrency = 8

	// DefaultUnstampedVersionCacheTTL is how long a version observed without a
	// UI bundle is remembered as such: short enough to pick up CI stamping,
	// long enough that a listing does not re-describe it on every request.
	DefaultUnstampedVersionCacheTTL = 30 * time.Second

	// versionLookupTimeout bounds a DescribeVersion call, which runs detached
	// from the requesting context (see resolveVersionBundle).
	versionLookupTimeout = 10 * time.Second
)

// UIBundleMetadataKey returns the deployment-version metadata key holding the
// given field ("slug"/"version") for a workflow type. Pass an empty
// workflowType for the version-wide default key.
func UIBundleMetadataKey(workflowType, field string) string {
	if workflowType == "" {
		return uiBundleMetaPrefix + field
	}
	return uiBundleMetaPrefix + workflowType + "." + field
}

// UIBundleVersionKey and UIBundleSlugKey are the write-side counterparts workers
// stamp with (#1132); they must agree with what the read path looks up.
func UIBundleVersionKey(workflowType string) string {
	return UIBundleMetadataKey(workflowType, uiBundleVerField)
}

func UIBundleSlugKey(workflowType string) string {
	return UIBundleMetadataKey(workflowType, uiBundleSlugField)
}

// DeploymentVersionRef identifies a Worker Deployment Version.
type DeploymentVersionRef struct {
	DeploymentName string
	BuildID        string
}

// ParseDeploymentVersionSA splits a TemporalWorkerDeploymentVersion search
// attribute value ("<deployment>:<build>"; older servers wrote
// "<deployment>.<build>") into its parts. Either part may contain the other
// delimiter, so a known deployment name (the TemporalWorkerDeployment
// attribute) anchors the split; otherwise the first ':' wins, then the first
// '.'. Returns false for an empty or unparseable value, i.e. unversioned.
func ParseDeploymentVersionSA(value, deploymentName string) (DeploymentVersionRef, bool) {
	if value == "" {
		return DeploymentVersionRef{}, false
	}
	if deploymentName != "" {
		if rest, ok := strings.CutPrefix(value, deploymentName); ok && len(rest) > 1 && (rest[0] == ':' || rest[0] == '.') {
			return DeploymentVersionRef{DeploymentName: deploymentName, BuildID: rest[1:]}, true
		}
	}
	for _, delim := range []string{":", "."} {
		if name, build, ok := strings.Cut(value, delim); ok && name != "" && build != "" {
			return DeploymentVersionRef{DeploymentName: name, BuildID: build}, true
		}
	}
	return DeploymentVersionRef{}, false
}

// UIBundle identifies the UI bundle (slug + version) that a workflow
// execution's pinned deployment version ships.
type UIBundle struct {
	Slug    string
	Version string
}

// UIBundleTemplate holds the tenant-owned web + mobile URL templates (with
// {{.Slug}}/{{.Version}} placeholders) for the UI bundles.
type UIBundleTemplate struct {
	Web    string
	Mobile string
}

// UIBundleURLs holds the fully rendered web and mobile UI bundle URLs.
type UIBundleURLs struct {
	Web    string
	Mobile string
}

// UITemplateContext is the data a UI bundle URL template renders against: the
// bundle slug/version plus the tenant dimensions. Version and TenantID are
// required, and every set dimension must be a single path segment, whatever its
// source. Owner is derived from TenantID/Flavour.
type UITemplateContext struct {
	Slug     string
	Version  string
	TenantID string
	Flavour  string
	Env      string
	Owner    string
}

// DetectFlavour returns the flavour from tenant data, or "" for a normal tenant.
// Precedence: an explicit "flavour" string, else the isPyckGo bool. Boolean
// fields are expected to be native Go bools (converted at ingestion). Canonical
// home so management and the workflow service share one definition.
func DetectFlavour(data map[string]any) string {
	if data == nil {
		return ""
	}
	if f, ok := data["flavour"].(string); ok && f != "" {
		return f
	}
	if v, ok := data["isPyckGo"].(bool); ok && v {
		return "pyck-go"
	}
	return ""
}

// ResolveRemoteUIBundle resolves the UI bundle (slug/version) for an execution
// from the Worker Deployment Version it is pinned to. The bundle is read from
// the version's metadata, preferring a per-workflow-type override and falling
// back to the version-wide default.
//
// When the bundle cannot be read from a pinned version — there is no pinned
// deployment version (pre-versioning execution, worker not opted in, namespace
// without versioning), or the version exists but its UI bundle is not stamped
// yet — and defaultBundle is non-nil, that bundle is returned so remoteUI keeps
// working during the #1132 rollout; with no default it errors.
//
// This is a plain client call (DescribeWorkflowExecution + WorkerDeployment
// describe); it runs off any workflow goroutine and has no determinism
// constraints.
func (c *Client) ResolveRemoteUIBundle(ctx context.Context, workflowID, runID string, defaultBundle *UIBundle) (*UIBundle, error) {
	if err := ValidateWorkflowID(workflowID); err != nil {
		return nil, err
	}

	resp, err := c.temporal.DescribeWorkflowExecution(ctx, workflowID, runID)
	if err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("describe workflow execution")
		return nil, ErrRemoteUIUnavailable
	}

	info := resp.GetWorkflowExecutionInfo()
	// Pinned worker versioning (#1317/#1132): an execution stays on its start
	// version, so GetDeploymentVersion is the version it actually runs. Revisit if
	// we move to AutoUpgrade (which reports the current version, not the replayed
	// one).
	var version *DeploymentVersionRef
	if v := info.GetVersioningInfo().GetDeploymentVersion(); v != nil {
		version = &DeploymentVersionRef{DeploymentName: v.GetDeploymentName(), BuildID: v.GetBuildId()}
	}

	return c.ResolveVersionUIBundle(ctx, version, info.GetType().GetName(), defaultBundle)
}

// ResolveVersionUIBundle is ResolveRemoteUIBundle for an execution whose pinned
// version (nil = unversioned) and workflow type are already known, e.g. from a
// listing's search attributes, so no DescribeWorkflowExecution is paid.
func (c *Client) ResolveVersionUIBundle(ctx context.Context, version *DeploymentVersionRef, workflowType string, defaultBundle *UIBundle) (*UIBundle, error) {
	if version == nil {
		if defaultBundle != nil {
			log.ForContext(ctx).Debug().Str("workflow_type", workflowType).
				Msg("remoteUI: execution has no pinned deployment version, serving default UI bundle")
			return defaultBundle, nil
		}
		return nil, ErrNoDeploymentVersion
	}

	bundle, err := c.resolveVersionBundle(ctx, version.DeploymentName, version.BuildID, workflowType)
	if err != nil {
		// Pinned but this workflow type isn't stamped yet (#1132 rollout): fall
		// back to the default, like an unversioned execution. Debug here; the
		// first observation per version warns in resolveVersionBundle.
		if errors.Is(err, ErrUIBundleMetadataMissing) && defaultBundle != nil {
			log.ForContext(ctx).Debug().
				Str("deployment", version.DeploymentName).Str("build_id", version.BuildID).
				Str("workflow_type", workflowType).
				Msg("remoteUI: pinned version has no stamped UI bundle for this workflow type, serving default")
			return defaultBundle, nil
		}
		return nil, err
	}

	return bundle, nil
}

// resolveVersionBundle resolves the UI bundle for (deployment, buildID,
// workflowType). A complete resolve is cached for good (stamped metadata is
// immutable); an unstamped tier (ErrUIBundleMetadataMissing) only for
// unstampedVersionCacheTTL, so CI stamping is picked up; an infrastructure
// failure is never cached.
//
// Concurrent misses on one key share a single DescribeVersion (std.SharedCall).
func (c *Client) resolveVersionBundle(ctx context.Context, deploymentName, buildID, workflowType string) (*UIBundle, error) {
	key := "version-bundle:" + deploymentName + "/" + buildID + "/" + workflowType
	if bundle, ok, err := c.cachedVersionBundle(key); ok {
		return bundle, err
	}

	bundle, err := std.SharedCall(ctx, &c.versionFlight, key, versionLookupTimeout, func(callCtx context.Context) (UIBundle, error) {
		if bundle, ok, err := c.cachedVersionBundle(key); ok {
			if err != nil {
				return UIBundle{}, err
			}
			return *bundle, nil
		}

		desc, err := c.temporal.WorkerDeploymentClient().
			GetHandle(deploymentName).
			DescribeVersion(callCtx, temporalclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildID})
		if err != nil {
			log.ForContext(callCtx).Error().Err(err).Msg("describe deployment version")
			return UIBundle{}, ErrRemoteUIUnavailable
		}

		bundle, err := resolveUIBundle(desc.Info.Metadata, workflowType)
		if err != nil {
			if errors.Is(err, ErrUIBundleMetadataMissing) {
				log.ForContext(callCtx).Warn().
					Str("deployment", deploymentName).Str("build_id", buildID).Str("workflow_type", workflowType).
					Dur("recheck_after", c.unstampedVersionCacheTTL).
					Msg("remoteUI: pinned version has no stamped UI bundle for this workflow type")
				// A zero TTL never expires in the store, so zero must not be set.
				if c.unstampedVersionCacheTTL > 0 {
					c.remoteUICache.Set(key, err, c.unstampedVersionCacheTTL)
				}
			}
			return UIBundle{}, err
		}

		c.remoteUICache.Set(key, bundle, 0)
		return bundle, nil
	})
	if err != nil {
		return nil, err
	}
	return &bundle, nil
}

// cachedVersionBundle returns the memoized bundle, or the memoized negative
// verdict as err. ok is false on a miss.
func (c *Client) cachedVersionBundle(key string) (bundle *UIBundle, ok bool, err error) {
	cached, _ := c.remoteUICache.Get(key)
	switch v := cached.(type) {
	case UIBundle:
		return &v, true, nil
	case error:
		return nil, true, v
	default:
		return nil, false, nil
	}
}

// RenderRemoteUI resolves the execution's UI bundle and renders the tenant (or
// default) templates into final web + mobile URLs. The caller supplies the
// tenant render dimensions (TenantID/Flavour/Env); the bundle slug/version are
// resolved here and merged in. The substitution lives here (not in the GraphQL
// resolver), so callers forward the result as-is. See ResolveRemoteUIBundle for
// the defaultBundle fallback semantics.
func (c *Client) RenderRemoteUI(ctx context.Context, workflowID, runID string, templates UIBundleTemplate, render UITemplateContext, defaultBundle *UIBundle) (*UIBundleURLs, error) {
	bundle, err := c.ResolveRemoteUIBundle(ctx, workflowID, runID, defaultBundle)
	if err != nil {
		return nil, err
	}
	return renderUIBundleURLs(*bundle, templates, render)
}

// RenderVersionRemoteUI is RenderRemoteUI for an execution whose pinned version
// (nil = unversioned) and workflow type are already known; see
// ResolveVersionUIBundle.
func (c *Client) RenderVersionRemoteUI(ctx context.Context, version *DeploymentVersionRef, workflowType string, templates UIBundleTemplate, render UITemplateContext, defaultBundle *UIBundle) (*UIBundleURLs, error) {
	bundle, err := c.ResolveVersionUIBundle(ctx, version, workflowType, defaultBundle)
	if err != nil {
		return nil, err
	}
	return renderUIBundleURLs(*bundle, templates, render)
}

// renderUIBundleURLs renders the web + mobile templates for a resolved bundle.
// Every dimension is spliced into a URL the frontend loads, so each is validated
// and the result checked (a stray "../" or "://" must never reach the client).
func renderUIBundleURLs(bundle UIBundle, templates UIBundleTemplate, render UITemplateContext) (*UIBundleURLs, error) {
	render.Slug = bundle.Slug
	render.Version = bundle.Version

	type dimension struct{ field, value string }
	// An empty tenant would collapse its path segment and shift the next one
	// into the tenant slot.
	for _, dim := range [...]dimension{{"version", render.Version}, {"tenant_id", render.TenantID}} {
		if err := validateUIBundleValue(dim.field, dim.value); err != nil {
			return nil, err
		}
	}
	for _, dim := range [...]dimension{{"slug", render.Slug}, {"flavour", render.Flavour}, {"env", render.Env}} {
		if dim.value == "" {
			continue
		}
		if err := validateUIBundleValue(dim.field, dim.value); err != nil {
			return nil, err
		}
	}
	render.Owner = bundleOwner(render.TenantID, render.Flavour)

	web, err := renderRemoteUIURL(templates.Web, render)
	if err != nil {
		return nil, err
	}
	mobile, err := renderRemoteUIURL(templates.Mobile, render)
	if err != nil {
		return nil, err
	}

	return &UIBundleURLs{Web: web, Mobile: mobile}, nil
}

// uiBundleValuePattern allows only characters safe to splice into a URL path.
var uiBundleValuePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// validateUIBundleValue rejects a value unsafe to splice into a URL path: empty,
// outside the allow-list, or a "."/".." segment (the allow-list admits dots, so
// ".." would otherwise pass and yield path traversal).
func validateUIBundleValue(field, value string) error {
	if !uiBundleValuePattern.MatchString(value) || value == "." || value == ".." {
		return fmt.Errorf("%w: %s=%q", ErrInvalidUIBundleValue, field, value)
	}
	return nil
}

// bundleOwner mirrors the store's layout: a flavour's bundle is published
// once by the platform and shared, a tenant publishes its own. Composed from
// already-validated values.
func bundleOwner(tenantID, flavour string) string {
	if flavour != "" {
		return "extensions/" + flavour
	}
	return "tenants/" + tenantID
}

// renderRemoteUIURL executes a URL template against the render context and
// verifies the result is a well-formed absolute http(s) URL. The template uses
// Go text/template syntax ({{.Owner}} / {{.Slug}} / {{.Version}} /
// {{.TenantID}} / {{.Flavour}} / {{.Env}}); a malformed template or a reference
// to an unknown field fails loudly rather than silently passing through. An
// empty template (no URL for that platform) yields "" without error.
func renderRemoteUIURL(tmpl string, render UITemplateContext) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	parsed, err := template.New("uiBundleURL").Parse(tmpl)
	if err != nil {
		return "", ErrRenderedURLInvalid
	}
	var buf strings.Builder
	if err := parsed.Execute(&buf, render); err != nil {
		return "", ErrRenderedURLInvalid
	}
	rendered := buf.String()
	u, err := url.Parse(rendered)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%w: %s", ErrRenderedURLInvalid, rendered)
	}
	// path.Clean would resolve a ".." (u.Path is already percent-decoded)
	// instead of refusing it.
	if slices.Contains(strings.Split(u.Path, "/"), "..") {
		return "", fmt.Errorf("%w: %s", ErrRenderedURLInvalid, rendered)
	}
	// Collapse the empty segment an unset optional placeholder (slug/env/flavour)
	// leaves behind, so it yields a clean URL instead of "//". Also drops a
	// trailing slash.
	if u.Path != "" {
		u.Path = path.Clean(u.Path)
		u.RawPath = "" // stale encoding of the pre-Clean path
	}
	return u.String(), nil
}

// DeploymentVersionUI describes a Worker Deployment Version and the UI bundles it
// ships, for drain-visibility: which versions are still draining (have pinned
// in-flight executions) versus safe to retire.
type DeploymentVersionUI struct {
	DeploymentName string
	BuildID        string
	DrainageStatus string
	// Bundles is every UI bundle stamped on the version (one per workflow type,
	// plus the version-wide default with WorkflowType ""). Empty if none.
	Bundles []DeploymentVersionUIBundle
}

// DeploymentVersionUIBundle is one UI bundle stamped on a deployment version.
// WorkflowType is "" for the version-wide default.
type DeploymentVersionUIBundle struct {
	WorkflowType string
	Slug         string
	Version      string
}

// uiBundlesFromMetadata returns every UI bundle stamped on a version (one per
// workflow type, plus the version-wide default), sorted by type. A version is
// required; a slug-only tier is skipped.
func uiBundlesFromMetadata(meta map[string]*commonpb.Payload) ([]DeploymentVersionUIBundle, error) {
	types := map[string]struct{}{}
	for key := range meta {
		rest, ok := strings.CutPrefix(key, uiBundleMetaPrefix)
		if !ok {
			continue
		}
		switch {
		case rest == uiBundleVerField || rest == uiBundleSlugField:
			types[""] = struct{}{}
		case strings.HasSuffix(rest, "."+uiBundleVerField):
			types[strings.TrimSuffix(rest, "."+uiBundleVerField)] = struct{}{}
		case strings.HasSuffix(rest, "."+uiBundleSlugField):
			types[strings.TrimSuffix(rest, "."+uiBundleSlugField)] = struct{}{}
		}
	}

	bundles := make([]DeploymentVersionUIBundle, 0, len(types))
	for t := range types {
		b, ok, err := uiBundleAtTier(meta, t)
		if err != nil {
			return nil, err
		}
		if ok {
			bundles = append(bundles, DeploymentVersionUIBundle{WorkflowType: t, Slug: b.Slug, Version: b.Version})
		}
	}
	slices.SortFunc(bundles, func(a, b DeploymentVersionUIBundle) int {
		return strings.Compare(a.WorkflowType, b.WorkflowType)
	})
	return bundles, nil
}

// cloneDeploymentVersionUIs detaches a result from the shared cache entry.
// DeploymentVersionUIBundle is a value type, so cloning Bundles suffices.
func cloneDeploymentVersionUIs(in []DeploymentVersionUI) []DeploymentVersionUI {
	out := make([]DeploymentVersionUI, len(in))
	for i, v := range in {
		v.Bundles = slices.Clone(v.Bundles)
		out[i] = v
	}
	return out
}

// ListDeploymentVersionUIBundles enumerates every Worker Deployment Version in
// the namespace with its drainage status and the UI bundles stamped on it (one
// per workflow type). Bundles are best-effort: a version carrying no UI metadata
// reports an empty list rather than failing the whole listing.
//
// The result is cached for a short TTL: drainage status and stamped bundles
// change slowly, and this avoids re-running the List -> Describe fan-out on every
// operator query.
func (c *Client) ListDeploymentVersionUIBundles(ctx context.Context) ([]DeploymentVersionUI, error) {
	if cached, ok := c.remoteUICache.Get(deploymentVersionsCacheKey); ok {
		if versions, ok := cached.([]DeploymentVersionUI); ok {
			return cloneDeploymentVersionUIs(versions), nil
		}
	}

	wdc := c.temporal.WorkerDeploymentClient()

	iter, err := wdc.List(ctx, temporalclient.WorkerDeploymentListOptions{})
	if err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("list worker deployments")
		return nil, ErrRemoteUIUnavailable
	}

	// First pass (serial — the List iterator is): collect every version with the
	// handle to describe it.
	type pendingVersion struct {
		handle  temporalclient.WorkerDeploymentHandle
		summary temporalclient.WorkerDeploymentVersionSummary
	}
	var pending []pendingVersion
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			log.ForContext(ctx).Error().Err(err).Msg("iterate worker deployments")
			return nil, ErrRemoteUIUnavailable
		}

		handle := wdc.GetHandle(entry.Name)
		desc, err := handle.Describe(ctx, temporalclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			// Best-effort: drop one unreachable deployment from the listing rather
			// than failing the whole operator drain-visibility view.
			log.ForContext(ctx).Warn().Err(err).Str("deployment", entry.Name).
				Msg("skip deployment in UI bundle listing")
			continue
		}
		for _, summary := range desc.Info.VersionSummaries {
			pending = append(pending, pendingVersion{handle: handle, summary: summary})
		}
	}

	// Second pass: the per-version describes are independent, so fan them out
	// (bounded) instead of paying the round trips serially. Results are written by
	// index to preserve order; bundle lookup is best-effort.
	out := make([]DeploymentVersionUI, len(pending))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(deploymentVersionDescribeConcurrency)
	for i := range pending {
		g.Go(func() error {
			p := pending[i]
			version := DeploymentVersionUI{
				DeploymentName: p.summary.Version.DeploymentName,
				BuildID:        p.summary.Version.BuildID,
				DrainageStatus: drainageStatusString(p.summary.DrainageStatus),
			}
			if vd, err := p.handle.DescribeVersion(gctx, temporalclient.WorkerDeploymentDescribeVersionOptions{
				BuildID: p.summary.Version.BuildID,
			}); err == nil {
				if bundles, err := uiBundlesFromMetadata(vd.Info.Metadata); err == nil {
					version.Bundles = bundles
				}
			}
			out[i] = version
			return nil
		})
	}
	_ = g.Wait() // best-effort: the goroutines never return an error

	c.remoteUICache.Set(deploymentVersionsCacheKey, out, deploymentVersionsCacheTTL)
	return cloneDeploymentVersionUIs(out), nil
}

func drainageStatusString(s temporalclient.WorkerDeploymentVersionDrainageStatus) string {
	switch s {
	case temporalclient.WorkerDeploymentVersionDrainageStatusDraining:
		return "draining"
	case temporalclient.WorkerDeploymentVersionDrainageStatusDrained:
		return "drained"
	default:
		return "unspecified"
	}
}

// resolveUIBundle picks the UI bundle for a workflow type from a deployment
// version's metadata, preferring a complete per-type override and falling back
// to the complete version-wide default. The slug/version pair is always read
// from a single tier, so a per-type slug is never mixed with a version-wide
// version (or vice versa).
func resolveUIBundle(meta map[string]*commonpb.Payload, workflowType string) (UIBundle, error) {
	if workflowType != "" {
		bundle, ok, err := uiBundleAtTier(meta, workflowType)
		if err != nil {
			return UIBundle{}, err
		}
		if ok {
			return bundle, nil
		}
	}

	bundle, ok, err := uiBundleAtTier(meta, "")
	if err != nil {
		return UIBundle{}, err
	}
	if ok {
		return bundle, nil
	}

	return UIBundle{}, fmt.Errorf("%w (workflow type %q)", ErrUIBundleMetadataMissing, workflowType)
}

// uiBundleAtTier reads the bundle for a single tier — a workflow type, or "" for
// the version-wide default. version is required; slug is optional (workers stamp
// it only for shared "flavour" bundles whose URL template uses {{.Slug}};
// per-tenant bundles are keyed by version alone). ok is true when version is
// present, so a slug-only tier is treated as absent. An empty per-type version
// is absent too, so it cannot shadow the default; an empty default has nothing
// to fall back to and fails at render.
func uiBundleAtTier(meta map[string]*commonpb.Payload, workflowType string) (UIBundle, bool, error) {
	version, okVersion, err := payloadString(meta, UIBundleMetadataKey(workflowType, uiBundleVerField))
	if err != nil {
		return UIBundle{}, false, err
	}
	if !okVersion || (version == "" && workflowType != "") {
		return UIBundle{}, false, nil
	}
	slug, _, err := payloadString(meta, UIBundleMetadataKey(workflowType, uiBundleSlugField))
	if err != nil {
		return UIBundle{}, false, err
	}
	return UIBundle{Slug: slug, Version: version}, true, nil
}

// payloadString decodes a single string metadata value. ok is false when the key
// is absent.
func payloadString(meta map[string]*commonpb.Payload, key string) (string, bool, error) {
	payload, ok := meta[key]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := converter.GetDefaultDataConverter().FromPayload(payload, &value); err != nil {
		return "", false, fmt.Errorf("decode UI bundle metadata %q: %w", key, err)
	}
	return value, true, nil
}
