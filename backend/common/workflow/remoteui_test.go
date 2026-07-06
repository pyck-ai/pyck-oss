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
