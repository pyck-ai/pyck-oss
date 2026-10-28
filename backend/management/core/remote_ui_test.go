package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestValidateRemoteUITemplate(t *testing.T) {
	t.Parallel()

	t.Run("accepts well-formed absolute templates", func(t *testing.T) {
		t.Parallel()
		ok := []string{
			"https://cdn.example.com/web/{{.Slug}}/{{.Version}}/mf-manifest.json",
			"https://cdn.example.com/tenants/t1/{{.Version}}/web/mf.json", // slug optional (per-tenant)
			"https://b\u00fccher.example.test/{{.Version}}",               // IDN host
			"https://xn--bcher-kva.example.test/{{.Version}}",             // punycode host
			"https://localhost:5173/{{.Version}}/mf.json",                 // port
			"https://[::1]:8443/{{.Version}}",                             // IPv6 literal
		}
		for _, tmpl := range ok {
			assert.NoError(t, core.ValidateRemoteUITemplate(tmpl), tmpl)
		}
	})

	t.Run("rejects invalid templates", func(t *testing.T) {
		t.Parallel()
		bad := []string{
			"", // empty
			"https://cdn.example.com/web/{{.Slug}}/x.json",                                        // missing {{.Version}}
			"/{{.Version}}/x.json",                                                                // not absolute
			"ftp://cdn.example.com/{{.Version}}",                                                  // wrong scheme
			"https://cdn.example.com/{{.Owner}}/../../tenants/other/{{.Version}}/mf.json",         // traversal
			"https://cdn.example.com/{{.Owner}}/%2e%2e/%2e%2e/tenants/other/{{.Version}}/mf.json", // encoded traversal
		}
		for _, tmpl := range bad {
			assert.ErrorIs(t, core.ValidateRemoteUITemplate(tmpl), core.ErrInvalidUITemplate, tmpl)
		}
	})

	t.Run("rejects invisible characters in the host", func(t *testing.T) {
		t.Parallel()
		bad := map[string]string{
			"bidi override":             "https://ok\u202e.example.test/{{.Version}}",
			"encoded bidi override":     "https://ok%E2%80%AE.example.test/{{.Version}}",
			"zero width space":          "https://ok\u200b.example.test/{{.Version}}",
			"no-break space":            "https://ok\u00a0.example.test/{{.Version}}",
			"line separator":            "https://ok\u2028.example.test/{{.Version}}",
			"C1 control":                "https://ok\u0085.example.test/{{.Version}}",
			"variation selector":        "https://ok\ufe0f.example.test/{{.Version}}",
			"combining grapheme joiner": "https://ok\u034f.example.test/{{.Version}}",
			"hangul filler":             "https://ok\u3164.example.test/{{.Version}}",
			"private use":               "https://ok\ue000.example.test/{{.Version}}",
			"noncharacter":              "https://ok\ufdd0.example.test/{{.Version}}",
			"encoded invalid UTF-8":     "https://ok%FF.example.test/{{.Version}}",
		}
		for name, tmpl := range bad {
			err := core.ValidateRemoteUITemplate(tmpl)
			require.ErrorIs(t, err, core.ErrInvalidUITemplate, name)
			assert.ErrorContains(t, err, "invisible", name)
		}
	})

	t.Run("rejects a percent sign or IPv6 zone in the host", func(t *testing.T) {
		t.Parallel()
		bad := map[string]string{
			"zone with escaped space": "https://[fe80::1%25en%200]/{{.Version}}",
			"zone":                    "https://[fe80::1%25en0]/{{.Version}}",
			"escaped percent":         "https://ok%25.example.test/{{.Version}}",
		}
		for name, tmpl := range bad {
			err := core.ValidateRemoteUITemplate(tmpl)
			require.ErrorIs(t, err, core.ErrInvalidUITemplate, name)
			assert.ErrorContains(t, err, "IPv6 zone", name)
		}
	})
}

func TestValidateRemoteUITemplates(t *testing.T) {
	t.Parallel()

	const (
		ok  = "https://cdn.example.com/{{.Version}}/mf.json"
		bad = "https://ok\u202e.example.test/{{.Version}}"
	)

	for name, data := range map[string]map[string]any{
		"nil data":       nil,
		"no template":    {"flavour": "pyck-go"},
		"valid template": {core.RemoteWebUITemplateKey: ok, core.RemoteMobileUITemplateKey: ok},
	} {
		require.NoError(t, core.ValidateRemoteUITemplates(data), name)
	}

	for name, data := range map[string]map[string]any{
		"invalid web":    {core.RemoteWebUITemplateKey: bad},
		"invalid mobile": {core.RemoteWebUITemplateKey: ok, core.RemoteMobileUITemplateKey: bad},
		"non-string":     {core.RemoteWebUITemplateKey: 42},
		"null":           {core.RemoteMobileUITemplateKey: nil},
	} {
		assert.ErrorIs(t, core.ValidateRemoteUITemplates(data), core.ErrInvalidUITemplate, name)
	}
}
