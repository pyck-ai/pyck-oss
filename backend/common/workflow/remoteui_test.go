//nolint:testpackage // in-package test required: uiBundleMetaValue is package-private.
package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
)

func mustPayload(t *testing.T, v any) *commonpb.Payload {
	t.Helper()
	p, err := converter.GetDefaultDataConverter().ToPayload(v)
	require.NoError(t, err)
	return p
}

func TestUIBundleMetadataKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ui.bundle.slug", UIBundleMetadataKey("", "slug"))
	assert.Equal(t, "ui.bundle.version", UIBundleMetadataKey("", "version"))
	assert.Equal(t, "ui.bundle.PickingWorkflow.slug", UIBundleMetadataKey("PickingWorkflow", "slug"))

	// Write-side helpers must produce the exact keys the read path looks up.
	assert.Equal(t, "ui.bundle.PickingWorkflow.version", UIBundleVersionKey("PickingWorkflow"))
	assert.Equal(t, "ui.bundle.PickingWorkflow.slug", UIBundleSlugKey("PickingWorkflow"))
}

func TestResolveUIBundle(t *testing.T) {
	t.Parallel()

	t.Run("complete per-type override wins over the default", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			"ui.bundle.slug":                    mustPayload(t, "default-slug"),
			"ui.bundle.version":                 mustPayload(t, "9.9.9"),
			"ui.bundle.PickingWorkflow.slug":    mustPayload(t, "picking-slug"),
			"ui.bundle.PickingWorkflow.version": mustPayload(t, "2.3.0"),
		}
		got, err := resolveUIBundle(meta, "PickingWorkflow")
		require.NoError(t, err)
		assert.Equal(t, UIBundle{Slug: "picking-slug", Version: "2.3.0"}, got)
	})

	t.Run("falls back to the version-wide default", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			"ui.bundle.slug":    mustPayload(t, "default-slug"),
			"ui.bundle.version": mustPayload(t, "1.0.0"),
		}
		got, err := resolveUIBundle(meta, "PickingWorkflow")
		require.NoError(t, err)
		assert.Equal(t, UIBundle{Slug: "default-slug", Version: "1.0.0"}, got)
	})

	t.Run("an incomplete per-type pair falls back to the default (no cross-tier mixing)", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			// per-type has only a slug — must NOT be paired with the default version
			"ui.bundle.PickingWorkflow.slug": mustPayload(t, "picking-slug"),
			"ui.bundle.slug":                 mustPayload(t, "default-slug"),
			"ui.bundle.version":              mustPayload(t, "1.0.0"),
		}
		got, err := resolveUIBundle(meta, "PickingWorkflow")
		require.NoError(t, err)
		assert.Equal(t, UIBundle{Slug: "default-slug", Version: "1.0.0"}, got)
	})

	t.Run("errors when no complete tier is present", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{"ui.bundle.slug": mustPayload(t, "only-slug")}
		_, err := resolveUIBundle(meta, "PickingWorkflow")
		require.ErrorIs(t, err, ErrUIBundleMetadataMissing)
	})

	t.Run("an empty per-type version is no override", func(t *testing.T) {
		t.Parallel()
		for name, empty := range map[string]*commonpb.Payload{
			"empty string": mustPayload(t, ""),
			"nil payload":  nil,
		} {
			meta := map[string]*commonpb.Payload{
				"ui.bundle.slug":                    mustPayload(t, "default"),
				"ui.bundle.version":                 mustPayload(t, "1.0.0"),
				"ui.bundle.PickingWorkflow.version": empty,
			}
			got, err := resolveUIBundle(meta, "PickingWorkflow")
			require.NoError(t, err, name)
			assert.Equal(t, UIBundle{Slug: "default", Version: "1.0.0"}, got, name)
		}
	})

	t.Run("an empty version-wide version stays stamped and fails at render", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			"ui.bundle.slug":    mustPayload(t, "default"),
			"ui.bundle.version": mustPayload(t, ""),
		}
		got, err := resolveUIBundle(meta, "PickingWorkflow")
		require.NoError(t, err)
		assert.Equal(t, UIBundle{Slug: "default", Version: ""}, got)

		_, err = renderUIBundleURLs(got, UIBundleTemplate{Web: "https://cdn/{{.Slug}}/{{.Version}}/mf.json"}, UITemplateContext{})
		require.ErrorIs(t, err, ErrInvalidUIBundleValue)
	})

	t.Run("version without slug resolves (slug optional for per-tenant bundles)", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			"ui.bundle.PickingWorkflow.version": mustPayload(t, "4e5950c5"),
		}
		got, err := resolveUIBundle(meta, "PickingWorkflow")
		require.NoError(t, err)
		assert.Equal(t, UIBundle{Slug: "", Version: "4e5950c5"}, got)
	})
}

func TestUIBundlesFromMetadata(t *testing.T) {
	t.Parallel()

	t.Run("collects version-wide and per-type, sorted, slug optional", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			"ui.bundle.version":                 mustPayload(t, "1.0.0"),
			"ui.bundle.slug":                    mustPayload(t, "default"),
			"ui.bundle.PickingWorkflow.version": mustPayload(t, "9.9.9"),
			"ui.bundle.PickingWorkflow.slug":    mustPayload(t, "picking"),
			// version-only per-type (the per-tenant case) — included, empty slug.
			"ui.bundle.ReceivingWorkflow.version": mustPayload(t, "4e5950c5"),
			// slug-only — skipped (no version).
			"ui.bundle.PackingWorkflow.slug": mustPayload(t, "packing"),
		}

		got, err := uiBundlesFromMetadata(meta)
		require.NoError(t, err)
		assert.Equal(t, []DeploymentVersionUIBundle{
			{WorkflowType: "", Slug: "default", Version: "1.0.0"},
			{WorkflowType: "PickingWorkflow", Slug: "picking", Version: "9.9.9"},
			{WorkflowType: "ReceivingWorkflow", Slug: "", Version: "4e5950c5"},
		}, got)
	})

	t.Run("skips an empty per-type version but lists an empty default", func(t *testing.T) {
		t.Parallel()
		meta := map[string]*commonpb.Payload{
			"ui.bundle.version":                 mustPayload(t, ""),
			"ui.bundle.slug":                    mustPayload(t, "default"),
			"ui.bundle.PickingWorkflow.version": mustPayload(t, ""),
			"ui.bundle.PickingWorkflow.slug":    mustPayload(t, "picking"),
		}

		got, err := uiBundlesFromMetadata(meta)
		require.NoError(t, err)
		assert.Equal(t, []DeploymentVersionUIBundle{
			{WorkflowType: "", Slug: "default", Version: ""},
		}, got)
	})

	t.Run("empty metadata yields no bundles", func(t *testing.T) {
		t.Parallel()
		got, err := uiBundlesFromMetadata(map[string]*commonpb.Payload{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestValidateUIBundleValue(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"picking", "1.2.3", "v2", "a_b-c.d"} {
		require.NoError(t, validateUIBundleValue("slug", ok), ok)
	}

	for _, bad := range []string{"", ".", "..", "../etc", "a/b", "a:b", "a?b", "a b", "https://evil"} {
		assert.ErrorIs(t, validateUIBundleValue("slug", bad), ErrInvalidUIBundleValue, bad)
	}
}

func TestRenderRemoteUIURL(t *testing.T) {
	t.Parallel()
	render := UITemplateContext{Slug: "picking", Version: "1.2.3", TenantID: "t1", Flavour: "pyck-go", Env: "dev"}

	t.Run("renders an absolute URL", func(t *testing.T) {
		t.Parallel()
		got, err := renderRemoteUIURL("https://cdn.example.com/{{.Slug}}/{{.Version}}/mf.json", render)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/picking/1.2.3/mf.json", got)
	})

	t.Run("renders the full context with branching", func(t *testing.T) {
		t.Parallel()
		tmpl := "{{if .Flavour}}https://cdn/flavours/{{.Flavour}}/{{.Env}}/{{.Slug}}/{{.Version}}{{else}}https://cdn/{{.TenantID}}/{{.Slug}}/{{.Version}}{{end}}"
		got, err := renderRemoteUIURL(tmpl, render)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/flavours/pyck-go/dev/picking/1.2.3", got)

		normal := UITemplateContext{Slug: "picking", Version: "1.2.3", TenantID: "t1", Env: "dev"}
		got, err = renderRemoteUIURL(tmpl, normal)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/t1/picking/1.2.3", got)
	})

	t.Run("renders the same template for a flavour and a normal tenant", func(t *testing.T) {
		t.Parallel()
		tmpl := "https://cdn/{{.Owner}}/{{.Version}}/{{.Slug}}/mf.json"

		flavoured := render
		flavoured.Owner = bundleOwner(flavoured.TenantID, flavoured.Flavour)
		got, err := renderRemoteUIURL(tmpl, flavoured)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/extensions/pyck-go/1.2.3/picking/mf.json", got)

		normal := UITemplateContext{Slug: "picking", Version: "1.2.3", TenantID: "t1", Env: "dev"}
		normal.Owner = bundleOwner(normal.TenantID, normal.Flavour)
		got, err = renderRemoteUIURL(tmpl, normal)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/tenants/t1/1.2.3/picking/mf.json", got)
	})

	t.Run("canonicalizes an empty segment from an unfilled optional placeholder", func(t *testing.T) {
		t.Parallel()
		// An optional placeholder ({{.Slug}}/{{.Env}}/{{.Flavour}}) rendering empty
		// is collapsed, not rejected — path traversal is blocked at the value level.
		noSlug := UITemplateContext{Version: "1.2.3", TenantID: "t1"}
		got, err := renderRemoteUIURL("https://cdn/flavours/{{.Slug}}/{{.Version}}/mf.json", noSlug)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/flavours/1.2.3/mf.json", got)
	})

	t.Run("an empty template renders to empty without error", func(t *testing.T) {
		t.Parallel()
		got, err := renderRemoteUIURL("", render)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("rejects a non-absolute or non-http(s) URL", func(t *testing.T) {
		t.Parallel()
		for _, tmpl := range []string{"/{{.Slug}}/{{.Version}}/mf.json", "ftp://cdn/{{.Slug}}/{{.Version}}"} {
			_, err := renderRemoteUIURL(tmpl, render)
			assert.ErrorIs(t, err, ErrRenderedURLInvalid, tmpl)
		}
	})

	t.Run("rejects a malformed template or unknown field", func(t *testing.T) {
		t.Parallel()
		for _, tmpl := range []string{"https://cdn/{{.Slug}/x", "https://cdn/{{.Nope}}/x"} {
			_, err := renderRemoteUIURL(tmpl, render)
			assert.ErrorIs(t, err, ErrRenderedURLInvalid, tmpl)
		}
	})
}

func TestRenderUIBundleURLs(t *testing.T) {
	t.Parallel()

	const (
		tenantA    = "0198a3c1-0000-7000-8000-00000000000a"
		tenantB    = "0198a3c1-0000-7000-8000-00000000000b"
		ownerTmpl  = "https://cdn.example.com/{{.Owner}}/{{.Version}}/{{.Slug}}/mf.json"
		ownerOfA   = "https://cdn.example.com/tenants/" + tenantA + "/1.2.3/picking/mf.json"
		flavourURL = "https://cdn.example.com/extensions/pyck-go/1.2.3/picking/mf.json"
	)
	bundle := UIBundle{Slug: "picking", Version: "1.2.3"}
	web := func(tmpl string) UIBundleTemplate { return UIBundleTemplate{Web: tmpl} }

	t.Run("renders the caller's own owner prefix", func(t *testing.T) {
		t.Parallel()
		got, err := renderUIBundleURLs(bundle, web(ownerTmpl), UITemplateContext{TenantID: tenantA, Env: "dev"})
		require.NoError(t, err)
		assert.Equal(t, ownerOfA, got.Web)

		got, err = renderUIBundleURLs(bundle, web(ownerTmpl), UITemplateContext{TenantID: tenantA, Flavour: "pyck-go"})
		require.NoError(t, err)
		assert.Equal(t, flavourURL, got.Web)
	})

	t.Run("refuses an empty tenant", func(t *testing.T) {
		t.Parallel()
		for _, tmpl := range []string{ownerTmpl, "https://cdn.example.com/tenants/{{.TenantID}}/{{.Version}}/mf.json"} {
			_, err := renderUIBundleURLs(bundle, web(tmpl), UITemplateContext{})
			require.ErrorIs(t, err, ErrInvalidUIBundleValue, tmpl)
		}
	})

	t.Run("rejects a tenant id that is not one path segment", func(t *testing.T) {
		t.Parallel()
		for _, tenantID := range []string{
			"../tenants/" + tenantB,
			"%2e%2e/tenants/" + tenantB,
			"../" + tenantB,
			tenantB + "/x",
			"..",
		} {
			got, err := renderUIBundleURLs(bundle, web(ownerTmpl), UITemplateContext{TenantID: tenantID})
			require.ErrorIs(t, err, ErrInvalidUIBundleValue, tenantID)
			assert.Nil(t, got, tenantID)
		}
	})

	t.Run("rejects an env that is not one path segment", func(t *testing.T) {
		t.Parallel()
		tmpl := "https://cdn.example.com/{{.Env}}/{{.Version}}/{{.Slug}}/mf.json"
		for _, env := range []string{"../../tenants/" + tenantB, "dev/x", "dev?x=1"} {
			_, err := renderUIBundleURLs(bundle, web(tmpl), UITemplateContext{TenantID: tenantA, Env: env})
			require.ErrorIs(t, err, ErrInvalidUIBundleValue, env)
		}
	})

	t.Run("rejects a tenant id that adds a query parameter", func(t *testing.T) {
		t.Parallel()
		tmpl := "https://cdn.example.com/{{.Owner}}/{{.Version}}/mf.json?tenant={{.TenantID}}"
		_, err := renderUIBundleURLs(bundle, web(tmpl), UITemplateContext{TenantID: tenantB + "&admin=true", Flavour: "pyck-go"})
		require.ErrorIs(t, err, ErrInvalidUIBundleValue)
	})

	t.Run("rejects traversal written into the template", func(t *testing.T) {
		t.Parallel()
		for _, tmpl := range []string{
			"https://cdn.example.com/{{.Owner}}/../../tenants/" + tenantB + "/{{.Version}}/mf.json",
			"https://cdn.example.com/{{.Owner}}/%2e%2e/%2e%2e/tenants/" + tenantB + "/{{.Version}}/mf.json",
		} {
			_, err := renderUIBundleURLs(bundle, web(tmpl), UITemplateContext{TenantID: tenantA})
			require.ErrorIs(t, err, ErrRenderedURLInvalid, tmpl)
		}
	})
}
