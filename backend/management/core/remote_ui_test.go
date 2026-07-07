package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/management/core"
)

func TestDetectFlavour(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{"nil data", nil, ""},
		{"empty map", map[string]any{}, ""},
		{"isPyckGo bool true", map[string]any{"isPyckGo": true}, "pyck-go"},
		{"isPyckGo bool false", map[string]any{"isPyckGo": false}, ""},
		{"isPyckGo string ignored", map[string]any{"isPyckGo": "true"}, ""},
		{"flavour pyck-go", map[string]any{"flavour": "pyck-go"}, "pyck-go"},
		{"flavour custom-wms", map[string]any{"flavour": "custom-wms"}, "custom-wms"},
		{"flavour empty string", map[string]any{"flavour": ""}, ""},
		{"flavour wins over isPyckGo", map[string]any{"flavour": "custom-wms", "isPyckGo": true}, "custom-wms"},
		{"unrelated keys", map[string]any{"someKey": "val"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := core.DetectFlavour(tt.data)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectFlavourDoesNotMutate(t *testing.T) {
	t.Parallel()
	data := map[string]any{"isPyckGo": true, "other": "val"}
	core.DetectFlavour(data)
	assert.Equal(t, true, data["isPyckGo"])
	assert.Equal(t, "val", data["other"])
}

func TestIsBooleanField(t *testing.T) {
	t.Parallel()

	assert.True(t, core.IsBooleanField("isPyckGo"))
	assert.True(t, core.IsBooleanField("isSetupPending"))
	assert.False(t, core.IsBooleanField("flavour"))
	assert.False(t, core.IsBooleanField("remoteMobileUI"))
	assert.False(t, core.IsBooleanField(""))
}

func TestEnrichRemoteUIURLs(t *testing.T) {
	t.Parallel()

	const base = "https://frontend.example.com"
	const tenantID = "550e8400-e29b-41d4-a716-446655440000"

	t.Run("flavour tenant gets both URLs", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"isPyckGo": true}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "dev")
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/web/mf-manifest.json", result["remoteWebUI"])
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("normal tenant gets both URLs with tenant ID", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"someKey": "val"}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "dev")
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/web/mf-manifest.json", result["remoteWebUI"])
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("nil data with baseURL returns new map with both URLs", func(t *testing.T) {
		t.Parallel()
		result := core.EnrichRemoteUIURLs(nil, tenantID, base, "dev")
		assert.NotNil(t, result)
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/web/mf-manifest.json", result["remoteWebUI"])
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("nil data without baseURL returns nil", func(t *testing.T) {
		t.Parallel()
		result := core.EnrichRemoteUIURLs(nil, tenantID, "", "dev")
		assert.Nil(t, result)
	})

	t.Run("does not overwrite non-empty remoteMobileUI", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"remoteMobileUI": "https://custom.example.com/widgets.rfw"}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "dev")
		assert.Equal(t, "https://custom.example.com/widgets.rfw", result["remoteMobileUI"])
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/web/mf-manifest.json", result["remoteWebUI"])
	})

	t.Run("does not overwrite non-empty remoteWebUI", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"isPyckGo": true, "remoteWebUI": "https://custom.example.com/mf.json"}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "dev")
		assert.Equal(t, "https://custom.example.com/mf.json", result["remoteWebUI"])
		// Mobile should still be populated since it's not set
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("overwrites empty string values", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"remoteMobileUI": ""}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "dev")
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("trims trailing slash from base URL", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"isPyckGo": true}
		result := core.EnrichRemoteUIURLs(data, tenantID, base+"/", "dev")
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/web/mf-manifest.json", result["remoteWebUI"])
	})

	t.Run("lowercases environment name", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"isPyckGo": true}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "DEV")
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/web/mf-manifest.json", result["remoteWebUI"])
	})

	t.Run("custom flavour via flavour field", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{"flavour": "custom-wms"}
		result := core.EnrichRemoteUIURLs(data, tenantID, base, "prod")
		assert.Equal(t, "https://frontend.example.com/flavours/custom-wms/prod/web/mf-manifest.json", result["remoteWebUI"])
		assert.Equal(t, "https://frontend.example.com/flavours/custom-wms/prod/mobile/widgets.rfw", result["remoteMobileUI"])
	})
}

func TestMergeData(t *testing.T) {
	t.Parallel()

	t.Run("both nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, core.MergeData(nil, nil))
	})

	t.Run("only existing", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "1"}
		result := core.MergeData(existing, nil)
		assert.Equal(t, map[string]any{"a": "1"}, result)
	})

	t.Run("only incoming", func(t *testing.T) {
		t.Parallel()
		incoming := map[string]any{"b": "2"}
		result := core.MergeData(nil, incoming)
		assert.Equal(t, map[string]any{"b": "2"}, result)
	})

	t.Run("overlap incoming wins", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "old", "b": "keep"}
		incoming := map[string]any{"a": "new"}
		result := core.MergeData(existing, incoming)
		assert.Equal(t, "new", result["a"])
		assert.Equal(t, "keep", result["b"])
	})

	t.Run("non-overlapping keys preserved", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "1"}
		incoming := map[string]any{"b": "2"}
		result := core.MergeData(existing, incoming)
		assert.Equal(t, map[string]any{"a": "1", "b": "2"}, result)
	})

	t.Run("computed keys from incoming are skipped", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"remoteMobileUI": "custom-url", "remoteWebUI": "custom-react"}
		incoming := map[string]any{"remoteMobileUI": "stale", "remoteWebUI": "stale", "isPyckGo": "true"}
		result := core.MergeData(existing, incoming)
		assert.Equal(t, "custom-url", result["remoteMobileUI"])
		assert.Equal(t, "custom-react", result["remoteWebUI"])
		assert.Equal(t, "true", result["isPyckGo"])
	})

	t.Run("computed keys from incoming skipped even when not in existing", func(t *testing.T) {
		t.Parallel()
		incoming := map[string]any{"remoteMobileUI": "from-zitadel", "flavour": "pyck-go"}
		result := core.MergeData(nil, incoming)
		assert.NotContains(t, result, "remoteMobileUI")
		assert.Equal(t, "pyck-go", result["flavour"])
	})

	t.Run("originals not mutated", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "1"}
		incoming := map[string]any{"a": "2", "b": "3"}
		core.MergeData(existing, incoming)
		assert.Equal(t, "1", existing["a"])
		assert.NotContains(t, existing, "b")
		assert.Equal(t, "2", incoming["a"])
	})
}

func TestMapsEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    map[string]any
		b    map[string]any
		want bool
	}{
		{"nil vs nil", nil, nil, true},
		{"nil vs empty", nil, map[string]any{}, true},
		{"empty vs nil", map[string]any{}, nil, true},
		{"empty vs empty", map[string]any{}, map[string]any{}, true},
		{"equal maps", map[string]any{"a": "1"}, map[string]any{"a": "1"}, true},
		{"different values", map[string]any{"a": "1"}, map[string]any{"a": "2"}, false},
		{"different keys", map[string]any{"a": "1"}, map[string]any{"b": "1"}, false},
		{"nil vs non-empty", nil, map[string]any{"a": "1"}, false},
		{"non-empty vs nil", map[string]any{"a": "1"}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, core.MapsEqual(tt.a, tt.b))
		})
	}
}

func TestReconcileTenantData(t *testing.T) {
	t.Parallel()

	const base = "https://frontend.example.com"
	const tenantID = "550e8400-e29b-41d4-a716-446655440000"

	t.Run("manual URLs preserved when Zitadel has same keys", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"remoteMobileUI": "custom-url", "remoteWebUI": "custom-react"}
		zitadel := map[string]any{"remoteMobileUI": "stale", "remoteWebUI": "stale"}
		result := core.ReconcileTenantData(existing, zitadel, tenantID, base, "dev")
		assert.Equal(t, "custom-url", result["remoteMobileUI"])
		assert.Equal(t, "custom-react", result["remoteWebUI"])
	})

	t.Run("nil data + nil metadata + baseURL creates both URLs", func(t *testing.T) {
		t.Parallel()
		result := core.ReconcileTenantData(nil, nil, tenantID, base, "dev")
		assert.NotNil(t, result)
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/web/mf-manifest.json", result["remoteWebUI"])
		assert.Equal(t, "https://frontend.example.com/"+tenantID+"/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("nil data + nil metadata + no baseURL stays nil", func(t *testing.T) {
		t.Parallel()
		result := core.ReconcileTenantData(nil, nil, tenantID, "", "dev")
		assert.Nil(t, result)
	})

	t.Run("isPyckGo bool from Zitadel (pre-converted) enriched with URLs", func(t *testing.T) {
		t.Parallel()
		// Zitadel metadata arrives already converted to native bool at ingestion
		zitadel := map[string]any{"isPyckGo": true}
		result := core.ReconcileTenantData(nil, zitadel, tenantID, base, "dev")
		assert.Equal(t, true, result["isPyckGo"])
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/web/mf-manifest.json", result["remoteWebUI"])
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/mobile/widgets.rfw", result["remoteMobileUI"])
	})

	t.Run("manual Mobile URL + isPyckGo metadata keeps manual, adds Web", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"remoteMobileUI": "custom"}
		// Zitadel metadata arrives already converted to native bool at ingestion
		zitadel := map[string]any{"isPyckGo": true}
		result := core.ReconcileTenantData(existing, zitadel, tenantID, base, "dev")
		assert.Equal(t, "custom", result["remoteMobileUI"])
		assert.Equal(t, true, result["isPyckGo"])
		assert.Equal(t, "https://frontend.example.com/flavours/pyck-go/dev/web/mf-manifest.json", result["remoteWebUI"])
	})

	t.Run("existing URLs + no metadata stays unchanged", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"remoteMobileUI": "set", "remoteWebUI": "set"}
		result := core.ReconcileTenantData(existing, nil, tenantID, base, "dev")
		assert.Equal(t, "set", result["remoteMobileUI"])
		assert.Equal(t, "set", result["remoteWebUI"])
	})
}
