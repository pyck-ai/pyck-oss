package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/management/core"
	"github.com/pyck-ai/pyck/backend/management/handlers"
)

func fullFrontendConfig() core.FrontendConfig {
	return core.FrontendConfig{
		AppURL:           "https://app.example.com",
		AuthURL:          "https://auth.example.com",
		Environment:      "production",
		ClientID:         "frontend-id",
		RedirectURI:      "https://app.example.com/callback",
		Debug:            true,
		Features:         `{"beta":true}`,
		Version:          "1.2.3",
		OtelURL:          "https://otel.example.com",
		OtelKey:          "ingest-key",
		FeedbackEndpoint: "https://feedback.example.com",
		BarcodeApiURL:    "https://barcode.example.com",
		UIStoreURL:       "https://ui-store.example.com",
		CLIClientID:      "cli-id",
		MobileClientID:   "mobile-id",
		BrunoClientID:    "bruno-id",
		MCPClientID:      "mcp-id",
	}
}

func get(t *testing.T, h http.Handler, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, handlers.WellKnownConfigurationPath, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWellKnownConfiguration(t *testing.T) {
	t.Parallel()

	const fullWant = `{
		"version": 1,
		"meta": {"environment": "dev"},
		"endpoints": {
			"auth": {"url": "https://auth.example.com"},
			"app": {"url": "https://app.example.com"},
			"graphql": {"url": "https://app.example.com/graphql"},
			"worker-api": {"url": "https://worker.example.com"},
			"ui-store": {"url": "https://ui-store.example.com"},
			"feedback": {"url": "https://feedback.example.com"},
			"barcodes": {"url": "https://barcode.example.com"},
			"otel": {"url": "https://otel.example.com", "key": "ingest-key"}
		},
		"components": {
			"pyck-control": {
				"auth": {"client-id": "frontend-id", "redirect-uri": "https://app.example.com/callback"},
				"extra": {"build": "1.2.3", "debug": true, "features": {"beta": true}}
			},
			"pyck-cli": {"auth": {"client-id": "cli-id"}},
			"pyck-mobile": {"auth": {"client-id": "mobile-id"}},
			"pyck-bruno": {"auth": {"client-id": "bruno-id"}},
			"pyck-mcp": {"auth": {"client-id": "mcp-id"}}
		}
	}`

	tests := []struct {
		name         string
		cfg          func() core.FrontendConfig
		workerAPIURL string
		envName      string
		want         string
	}{
		{
			name:         "full config",
			cfg:          fullFrontendConfig,
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want:         fullWant,
		},
		{
			name: "empty client IDs and optional values are omitted",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.CLIClientID, c.MobileClientID, c.BrunoClientID, c.MCPClientID = "", "", "", ""
				c.ClientID, c.RedirectURI = "", ""
				c.UIStoreURL = ""
				c.OtelURL, c.OtelKey = "", ""
				c.FeedbackEndpoint, c.BarcodeApiURL = "", ""
				c.Version = ""
				return c
			},
			workerAPIURL: "",
			envName:      "dev",
			want: `{
				"version": 1,
				"meta": {"environment": "dev"},
				"endpoints": {
					"auth": {"url": "https://auth.example.com"},
					"app": {"url": "https://app.example.com"},
					"graphql": {"url": "https://app.example.com/graphql"}
				},
				"components": {
					"pyck-control": {"extra": {"debug": true, "features": {"beta": true}}}
				}
			}`,
		},
		{
			name: "otel without a key and control without a redirect URI",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.OtelKey = ""
				c.RedirectURI = ""
				c.CLIClientID, c.MobileClientID, c.BrunoClientID, c.MCPClientID = "", "", "", ""
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want: `{
				"version": 1,
				"meta": {"environment": "dev"},
				"endpoints": {
					"auth": {"url": "https://auth.example.com"},
					"app": {"url": "https://app.example.com"},
					"graphql": {"url": "https://app.example.com/graphql"},
					"worker-api": {"url": "https://worker.example.com"},
					"ui-store": {"url": "https://ui-store.example.com"},
					"feedback": {"url": "https://feedback.example.com"},
					"barcodes": {"url": "https://barcode.example.com"},
					"otel": {"url": "https://otel.example.com"}
				},
				"components": {
					"pyck-control": {
						"auth": {"client-id": "frontend-id"},
						"extra": {"build": "1.2.3", "debug": true, "features": {"beta": true}}
					}
				}
			}`,
		},
		{
			name: "only the CLI client is configured",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.ClientID, c.MobileClientID, c.BrunoClientID, c.MCPClientID = "", "", "", ""
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want: `{
				"version": 1,
				"meta": {"environment": "dev"},
				"endpoints": {
					"auth": {"url": "https://auth.example.com"},
					"app": {"url": "https://app.example.com"},
					"graphql": {"url": "https://app.example.com/graphql"},
					"worker-api": {"url": "https://worker.example.com"},
					"ui-store": {"url": "https://ui-store.example.com"},
					"feedback": {"url": "https://feedback.example.com"},
					"barcodes": {"url": "https://barcode.example.com"},
					"otel": {"url": "https://otel.example.com", "key": "ingest-key"}
				},
				"components": {
					"pyck-control": {"extra": {"build": "1.2.3", "debug": true, "features": {"beta": true}}},
					"pyck-cli": {"auth": {"client-id": "cli-id"}}
				}
			}`,
		},
		{
			name: "meta.environment prefers the discovery override",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.DiscoveryEnvironment = "eu"
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "prod",
			want:         strings.Replace(fullWant, `"environment": "dev"`, `"environment": "eu"`, 1),
		},
		{
			name:         "meta.environment falls back to PYCK_ENV",
			cfg:          fullFrontendConfig,
			workerAPIURL: "https://worker.example.com",
			envName:      "demo",
			want:         strings.Replace(fullWant, `"environment": "dev"`, `"environment": "demo"`, 1),
		},
		{
			name: "feature keys are converted to kebab-case recursively",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.Features = `{"enableDebugMenu":true,"useAPIKey":false,"nested":{"someFlag":1,"deepObj":{"innerKey":"x"}},` +
					`"list":[{"itemName":1}],"already-kebab":null,"version2Beta":1.50}`
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want: strings.Replace(fullWant, `"features": {"beta": true}`,
				`"features": {"enable-debug-menu":true,"use-api-key":false,`+
					`"nested":{"some-flag":1,"deep-obj":{"inner-key":"x"}},`+
					`"list":[{"item-name":1}],"already-kebab":null,"version2-beta":1.50}`, 1),
		},
		{
			name: "invalid features fall back to an empty object",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.Features = `{not json`
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want:         strings.Replace(fullWant, `"features": {"beta": true}`, `"features": {}`, 1),
		},
		{
			name: "non-object features fall back to an empty object",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.Features = `["a","b"]`
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want:         strings.Replace(fullWant, `"features": {"beta": true}`, `"features": {}`, 1),
		},
		{
			name: "empty features fall back to an empty object",
			cfg: func() core.FrontendConfig {
				c := fullFrontendConfig()
				c.Features = ``
				return c
			},
			workerAPIURL: "https://worker.example.com",
			envName:      "dev",
			want:         strings.Replace(fullWant, `"features": {"beta": true}`, `"features": {}`, 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := get(t, handlers.NewWellKnownConfigurationHandler(tt.cfg(), tt.workerAPIURL, tt.envName), "text/html")

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.Equal(t, "public, max-age=300", rec.Header().Get("Cache-Control"))
			assert.JSONEq(t, tt.want, rec.Body.String())
		})
	}
}

func TestWellKnownConfigurationKeyOrder(t *testing.T) {
	t.Parallel()

	rec := get(t, handlers.NewWellKnownConfigurationHandler(fullFrontendConfig(), "https://worker.example.com", "dev"), "")

	// JSONEq ignores key order, so this test compares the order of the
	// object keys as they appear in the body.
	assert.Equal(t, []string{
		"version", "meta", "environment",
		"endpoints",
		"auth", "url", "app", "url", "graphql", "url", "worker-api", "url", "ui-store", "url",
		"feedback", "url", "barcodes", "url", "otel", "url", "key",
		"components",
		"pyck-control", "auth", "client-id", "redirect-uri", "extra", "build", "debug", "features", "beta",
		"pyck-cli", "auth", "client-id",
		"pyck-mobile", "auth", "client-id",
		"pyck-bruno", "auth", "client-id",
		"pyck-mcp", "auth", "client-id",
	}, objectKeys(t, rec.Body.Bytes()))
}

// objectKeys returns every object key of a JSON document in the order it
// appears.
func objectKeys(t *testing.T, body []byte) []string {
	t.Helper()

	type level struct {
		object    bool // an object, not an array
		expectKey bool // the next token in this object is a key
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	var keys []string
	var stack []level
	// valueDone marks the current object as expecting its next key.
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return keys
		}
		require.NoError(t, err)

		// In key position a string is a key; the only other token there is
		// the closing '}', handled by the switch below.
		if key, ok := tok.(string); ok && len(stack) > 0 && stack[len(stack)-1].expectKey {
			keys = append(keys, key)
			stack[len(stack)-1].expectKey = false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, level{object: true, expectKey: true})
		case json.Delim('['):
			stack = append(stack, level{})
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
}

func TestSettingsUnchanged(t *testing.T) {
	t.Parallel()

	rec := get(t, handlers.NewSettingsHandler(fullFrontendConfig()), "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t,
		`{"appUrl":"https://app.example.com","authority":"https://auth.example.com","authorityClientId":"frontend-id",`+
			`"debug":true,"environment":"production","graphqlClientUri":"https://app.example.com/graphql",`+
			`"redirectUri":"https://app.example.com/callback","features":{"beta":true},"version":"1.2.3",`+
			`"otelUrl":"https://otel.example.com","otelIngestKey":"ingest-key",`+
			`"feedbackEndpoint":"https://feedback.example.com","barcodeApiUrl":"https://barcode.example.com"}`,
		rec.Body.String())
}
