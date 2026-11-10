package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/pyck-ai/pyck/backend/common/log"

	"github.com/pyck-ai/pyck/backend/management/core"
)

const (
	// WellKnownConfigurationPath is where the discovery document is served.
	WellKnownConfigurationPath = "/.well-known/pyck-configuration"

	// WellKnownConfigurationVersion is the "version" of the discovery
	// document. It covers the whole document and changes only when a field is
	// removed or renamed; adding a field does not change it.
	//
	// "endpoints.auth.url" is the OIDC issuer: it must equal
	// PYCK_FRONTEND_AUTH_URL verbatim, because clients compare it with the
	// "iss" claim of the tokens they receive.
	WellKnownConfigurationVersion = 1
)

type wellKnownConfiguration struct {
	Version    int                 `json:"version"`
	Meta       wellKnownMeta       `json:"meta"`
	Endpoints  wellKnownEndpoints  `json:"endpoints"`
	Components wellKnownComponents `json:"components"`
}

type wellKnownMeta struct {
	Environment string `json:"environment"`
}

type wellKnownEndpoint struct {
	URL string `json:"url"`
}

type wellKnownEndpoints struct {
	Auth      wellKnownEndpoint  `json:"auth"`
	App       wellKnownEndpoint  `json:"app"`
	GraphQL   wellKnownEndpoint  `json:"graphql"`
	WorkerAPI *wellKnownEndpoint `json:"worker-api,omitempty"`
	UIStore   *wellKnownEndpoint `json:"ui-store,omitempty"`
	Feedback  *wellKnownEndpoint `json:"feedback,omitempty"`
	Barcodes  *wellKnownEndpoint `json:"barcodes,omitempty"`
	Otel      *wellKnownOtel     `json:"otel,omitempty"`
}

type wellKnownOtel struct {
	URL string `json:"url"`
	Key string `json:"key,omitempty"`
}

type wellKnownComponents struct {
	Control *wellKnownControl   `json:"pyck-control,omitempty"`
	CLI     *wellKnownComponent `json:"pyck-cli,omitempty"`
	Mobile  *wellKnownComponent `json:"pyck-mobile,omitempty"`
	Bruno   *wellKnownComponent `json:"pyck-bruno,omitempty"`
	MCP     *wellKnownComponent `json:"pyck-mcp,omitempty"`
}

type wellKnownComponent struct {
	Auth wellKnownAuth `json:"auth"`
}

type wellKnownAuth struct {
	ClientID    string `json:"client-id"`
	RedirectURI string `json:"redirect-uri,omitempty"`
}

type wellKnownControl struct {
	Auth  *wellKnownAuth `json:"auth,omitempty"`
	Extra wellKnownExtra `json:"extra"`
}

type wellKnownExtra struct {
	Build    string          `json:"build,omitempty"`
	Debug    bool            `json:"debug"`
	Features json.RawMessage `json:"features"`
}

// NewWellKnownConfigurationHandler returns an http.Handler that serves the
// unauthenticated discovery document at WellKnownConfigurationPath. The
// response is built once from config and cached for the lifetime of the process.
//
// meta.environment is cfg.DiscoveryEnvironment, or environmentName (PYCK_ENV)
// when that is empty.
func NewWellKnownConfigurationHandler(cfg core.FrontendConfig, workerAPIURL, environmentName string) http.Handler {
	environment := cfg.DiscoveryEnvironment
	if environment == "" {
		environment = environmentName
	}

	control := &wellKnownControl{
		Extra: wellKnownExtra{
			Build:    cfg.Version,
			Debug:    cfg.Debug,
			Features: kebabCaseFeatures(cfg.Features),
		},
	}
	if cfg.ClientID != "" {
		control.Auth = &wellKnownAuth{ClientID: cfg.ClientID, RedirectURI: cfg.RedirectURI}
	}

	var otel *wellKnownOtel
	if cfg.OtelURL != "" {
		otel = &wellKnownOtel{URL: cfg.OtelURL, Key: cfg.OtelKey}
	}

	doc := wellKnownConfiguration{
		Version: WellKnownConfigurationVersion,
		Meta:    wellKnownMeta{Environment: environment},
		Endpoints: wellKnownEndpoints{
			Auth:      wellKnownEndpoint{URL: cfg.AuthURL},
			App:       wellKnownEndpoint{URL: cfg.AppURL},
			GraphQL:   wellKnownEndpoint{URL: cfg.AppURL + "/graphql"},
			WorkerAPI: optionalEndpoint(workerAPIURL),
			UIStore:   optionalEndpoint(cfg.UIStoreURL),
			Feedback:  optionalEndpoint(cfg.FeedbackEndpoint),
			Barcodes:  optionalEndpoint(cfg.BarcodeApiURL),
			Otel:      otel,
		},
		Components: wellKnownComponents{
			Control: control,
			CLI:     optionalComponent(cfg.CLIClientID),
			Mobile:  optionalComponent(cfg.MobileClientID),
			Bruno:   optionalComponent(cfg.BrunoClientID),
			MCP:     optionalComponent(cfg.MCPClientID),
		},
	}

	body, err := json.Marshal(doc)
	if err != nil {
		body = []byte("{}")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := log.ForContext(r.Context())
		logger.Debug().Msg("Serving well-known configuration")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			logger.Debug().Err(err).Msg("Writing well-known configuration failed")
		}
	})
}

// optionalEndpoint returns nil for an empty URL so the endpoint is omitted.
func optionalEndpoint(url string) *wellKnownEndpoint {
	if url == "" {
		return nil
	}
	return &wellKnownEndpoint{URL: url}
}

// optionalComponent returns nil for an empty client ID so the component is
// omitted.
func optionalComponent(clientID string) *wellKnownComponent {
	if clientID == "" {
		return nil
	}
	return &wellKnownComponent{Auth: wellKnownAuth{ClientID: clientID}}
}

// kebabCaseFeatures converts the object keys of the features JSON from
// camelCase to kebab-case, recursively. Values are unchanged. Input that is
// not a valid JSON object yields "{}".
func kebabCaseFeatures(features string) json.RawMessage {
	dec := json.NewDecoder(strings.NewReader(features))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return json.RawMessage("{}")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return json.RawMessage("{}")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return json.RawMessage("{}")
	}

	out, err := json.Marshal(kebabCaseKeys(obj))
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

func kebabCaseKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[kebabCase(k)] = kebabCaseKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = kebabCaseKeys(val)
		}
		return out
	default:
		return v
	}
}

// kebabCase converts camelCase to kebab-case. A run of uppercase letters
// counts as one word: "useAPIKey" becomes "use-api-key".
func kebabCase(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('-')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
