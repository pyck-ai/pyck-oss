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
	assert.False(t, core.IsBooleanField("remoteMobileUITemplate"))
	assert.False(t, core.IsBooleanField(""))
}

func TestMergeData(t *testing.T) {
	t.Parallel()

	t.Run("both nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, core.MergeData(nil, nil))
	})

	t.Run("only existing is preserved verbatim", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "1"}
		result := core.MergeData(existing, nil)
		assert.Equal(t, map[string]any{"a": "1"}, result)
	})

	t.Run("non-flag incoming keys are dropped", func(t *testing.T) {
		t.Parallel()
		incoming := map[string]any{"b": "2"}
		result := core.MergeData(nil, incoming)
		assert.Equal(t, map[string]any{}, result)
	})

	t.Run("stored data wins for non-flag keys", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "old", "b": "keep"}
		incoming := map[string]any{"a": "new"} // "a" is not a synced flag
		result := core.MergeData(existing, incoming)
		assert.Equal(t, "old", result["a"])
		assert.Equal(t, "keep", result["b"])
	})

	t.Run("flag keys from incoming overlay stored data", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"isPyckGo": false, "other": "x"}
		incoming := map[string]any{"isPyckGo": true, "isSetupPending": true, "flavour": "pyck-go"}
		result := core.MergeData(existing, incoming)
		assert.Equal(t, true, result["isPyckGo"])
		assert.Equal(t, true, result["isSetupPending"])
		assert.Equal(t, "pyck-go", result["flavour"])
		assert.Equal(t, "x", result["other"])
	})

	t.Run("template keys from incoming are ignored", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{core.RemoteWebUITemplateKey: "stored"}
		incoming := map[string]any{core.RemoteWebUITemplateKey: "from-zitadel", "isPyckGo": true}
		result := core.MergeData(existing, incoming)
		assert.Equal(t, "stored", result[core.RemoteWebUITemplateKey])
		assert.Equal(t, true, result["isPyckGo"])
	})

	t.Run("originals not mutated", func(t *testing.T) {
		t.Parallel()
		existing := map[string]any{"a": "1"}
		incoming := map[string]any{"isPyckGo": true}
		core.MergeData(existing, incoming)
		assert.Equal(t, "1", existing["a"])
		assert.NotContains(t, existing, "isPyckGo")
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

// TestMergeDataClearStaysCleared guards the clear-vs-sync fix (#1317): a cleared
// UI template override must NOT be resurrected by the next Zitadel sync. Sync is
// MergeData only — it no longer re-derives templates — so an absent key stays
// absent across a sync.
func TestMergeDataClearStaysCleared(t *testing.T) {
	t.Parallel()
	const (
		webKey    = core.RemoteWebUITemplateKey
		mobileKey = core.RemoteMobileUITemplateKey
	)

	// Tenant cleared its web override (key absent); a manual mobile override and a
	// flavour flag remain. Zitadel sync brings the flag plus a stale template key.
	existing := map[string]any{mobileKey: "custom-mobile", "isPyckGo": true}
	zitadel := map[string]any{webKey: "stale-from-zitadel", "isPyckGo": true}

	result := core.MergeData(existing, zitadel)

	_, webPresent := result[webKey]
	assert.False(t, webPresent, "cleared web override must stay cleared after sync")
	assert.Equal(t, "custom-mobile", result[mobileKey], "manual override preserved")
	assert.Equal(t, true, result["isPyckGo"], "flag synced from Zitadel")
}

func TestValidateRemoteUITemplate(t *testing.T) {
	t.Parallel()

	t.Run("accepts a well-formed absolute template", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, core.ValidateRemoteUITemplate(
			"https://cdn.example.com/web/{{.Slug}}/{{.Version}}/mf-manifest.json"))
	})

	t.Run("rejects invalid templates", func(t *testing.T) {
		t.Parallel()
		bad := []string{
			"", // empty
			"https://cdn.example.com/web/{{.Slug}}/x.json", // missing {{.Version}}
			"https://cdn.example.com/{{.Version}}/x.json",  // missing {{.Slug}}
			"/web/{{.Slug}}/{{.Version}}/x.json",           // not absolute
			"ftp://cdn.example.com/{{.Slug}}/{{.Version}}", // wrong scheme
		}
		for _, tmpl := range bad {
			assert.ErrorIs(t, core.ValidateRemoteUITemplate(tmpl), core.ErrInvalidUITemplate, tmpl)
		}
	})
}
