package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"golang.org/x/sync/errgroup"

	"github.com/pyck-ai/pyck/backend/common/log"
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

// ErrInvalidUIBundleValue is returned when a slug/version from deployment-version
// metadata contains characters unsafe to splice into a URL.
var ErrInvalidUIBundleValue = errors.New("invalid UI bundle metadata value")

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

	// deploymentVersionsCacheKey is the single cache key for the full listing
	// (one Client serves one namespace); deploymentVersionsCacheTTL bounds its
	// staleness.
	deploymentVersionsCacheKey = "deployment-version-ui-bundles"
	deploymentVersionsCacheTTL = 30 * time.Second

	// deploymentVersionDescribeConcurrency bounds the parallel per-version
	// DescribeVersion calls when (re)building the listing.
	deploymentVersionDescribeConcurrency = 8
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
// bundle slug/version plus the tenant dimensions a default template branches on
// (e.g. {{if .Flavour}}...{{else}}...{{end}}). Slug/Version come from
// deployment-version metadata and Flavour from Zitadel; all three are sanitized
// before rendering since they are spliced into a URL path. TenantID is a
// server-derived UUID and Env is operator config, both trusted.
type UITemplateContext struct {
	Slug     string
	Version  string
	TenantID string
	Flavour  string
	Env      string
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
	if workflowID == "" {
		return nil, ErrInvalidWorkflowID
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
	version := info.GetVersioningInfo().GetDeploymentVersion()
	if version == nil {
		if defaultBundle != nil {
			log.ForContext(ctx).Warn().Str("workflow_id", workflowID).
				Msg("remoteUI: execution has no pinned deployment version, serving default UI bundle")
			return defaultBundle, nil
		}
		return nil, ErrNoDeploymentVersion
	}
	workflowType := info.GetType().GetName()

	bundle, err := c.resolveVersionBundle(ctx, version.GetDeploymentName(), version.GetBuildId(), workflowType)
	if err != nil {
		// Pinned but this workflow type isn't stamped yet (#1132 rollout): fall
		// back to the default, like an unversioned execution.
		if errors.Is(err, ErrUIBundleMetadataMissing) && defaultBundle != nil {
			log.ForContext(ctx).Warn().
				Str("deployment", version.GetDeploymentName()).Str("build_id", version.GetBuildId()).
				Msg("remoteUI: pinned version has no stamped UI bundle for this workflow type, serving default")
			return defaultBundle, nil
		}
		return nil, err
	}

	return bundle, nil
}

// resolveVersionBundle resolves the UI bundle for (deployment, buildID,
// workflowType), caching only a complete resolve (no TTL). An unstamped tier
// resolves to ErrUIBundleMetadataMissing and is not cached, so a later read
// picks it up once CI stamps it — covering incremental per-type stamping during
// the #1132 rollout.
func (c *Client) resolveVersionBundle(ctx context.Context, deploymentName, buildID, workflowType string) (*UIBundle, error) {
	key := "version-bundle:" + deploymentName + "/" + buildID + "/" + workflowType
	if cached, ok := c.remoteUICache.Get(key); ok {
		if b, ok := cached.(UIBundle); ok {
			return &b, nil
		}
	}

	desc, err := c.temporal.WorkerDeploymentClient().
		GetHandle(deploymentName).
		DescribeVersion(ctx, temporalclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildID})
	if err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("describe deployment version")
		return nil, ErrRemoteUIUnavailable
	}

	bundle, err := resolveUIBundle(desc.Info.Metadata, workflowType)
	if err != nil {
		return nil, err
	}

	c.remoteUICache.Set(key, bundle, 0)
	return &bundle, nil
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
	render.Slug = bundle.Slug
	render.Version = bundle.Version

	// slug/version (deployment metadata) and flavour (Zitadel metadata) are
	// spliced into a URL the frontend loads as a microfrontend manifest. Sanitize
	// them and validate the rendered URL before handing it out (defense in depth:
	// a stray "../" or "://" must never reach the client).
	if err := validateUIBundleValue("slug", render.Slug); err != nil {
		return nil, err
	}
	if err := validateUIBundleValue("version", render.Version); err != nil {
		return nil, err
	}
	if render.Flavour != "" {
		if err := validateUIBundleValue("flavour", render.Flavour); err != nil {
			return nil, err
		}
	}

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

// renderRemoteUIURL executes a URL template against the render context and
// verifies the result is a well-formed absolute http(s) URL. The template uses
// Go text/template syntax ({{.Slug}} / {{.Version}} / {{.TenantID}} /
// {{.Flavour}} / {{.Env}}); a malformed template or a reference to an unknown
// field fails loudly rather than silently passing through. An empty template (no
// URL for that platform) yields "" without error.
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
	return rendered, nil
}

// DeploymentVersionUI describes a Worker Deployment Version and the UI bundle it
// ships, for drain-visibility: which versions are still draining (have pinned
// in-flight executions) versus safe to retire.
type DeploymentVersionUI struct {
	DeploymentName string
	BuildID        string
	DrainageStatus string
	Bundle         UIBundle
}

// ListDeploymentVersionUIBundles enumerates every Worker Deployment Version in
// the namespace with its drainage status and the version-wide UI bundle stamped
// on it. Bundle fields are best-effort: a version carrying no UI metadata
// reports an empty slug/version rather than failing the whole listing.
//
// The result is cached for a short TTL: drainage status and stamped bundles
// change slowly, and this avoids re-running the List -> Describe fan-out on every
// operator query.
func (c *Client) ListDeploymentVersionUIBundles(ctx context.Context) ([]DeploymentVersionUI, error) {
	if cached, ok := c.remoteUICache.Get(deploymentVersionsCacheKey); ok {
		if versions, ok := cached.([]DeploymentVersionUI); ok {
			return versions, nil
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
				if b, ok, err := uiBundleAtTier(vd.Info.Metadata, ""); err == nil && ok {
					version.Bundle = b
				}
			}
			out[i] = version
			return nil
		})
	}
	_ = g.Wait() // best-effort: the goroutines never return an error

	c.remoteUICache.Set(deploymentVersionsCacheKey, out, deploymentVersionsCacheTTL)
	return out, nil
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

// uiBundleAtTier reads the slug+version pair for a single tier — a workflow type,
// or "" for the version-wide default. ok is true only when BOTH fields are
// present, so an incomplete tier (e.g. slug without version) is treated as
// absent rather than yielding a half-resolved pair.
func uiBundleAtTier(meta map[string]*commonpb.Payload, workflowType string) (UIBundle, bool, error) {
	slug, okSlug, err := payloadString(meta, UIBundleMetadataKey(workflowType, uiBundleSlugField))
	if err != nil {
		return UIBundle{}, false, err
	}
	version, okVersion, err := payloadString(meta, UIBundleMetadataKey(workflowType, uiBundleVerField))
	if err != nil {
		return UIBundle{}, false, err
	}
	if okSlug && okVersion {
		return UIBundle{Slug: slug, Version: version}, true, nil
	}
	return UIBundle{}, false, nil
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
