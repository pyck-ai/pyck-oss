package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/pyck-ai/pyck/backend/common/log"

	"github.com/pyck-ai/pyck/backend/management/core"
)

// frontendSettings is the JSON structure served at /static/settings.json.
type frontendSettings struct {
	AppURL            string          `json:"appUrl"`
	Authority         string          `json:"authority"`
	AuthorityClientID string          `json:"authorityClientId"`
	Debug             bool            `json:"debug"`
	Environment       string          `json:"environment"`
	GraphQLClientURI  string          `json:"graphqlClientUri"`
	RedirectURI       string          `json:"redirectUri"`
	Features          json.RawMessage `json:"features"`
	Version           string          `json:"version"`
	OtelURL           string          `json:"otelUrl,omitempty"`
	OtelIngestKey     string          `json:"otelIngestKey,omitempty"`
	FeedbackEndpoint  string          `json:"feedbackEndpoint,omitempty"`
}

// NewSettingsHandler returns an http.Handler that serves the frontend settings JSON.
// The response is built once from config and cached for the lifetime of the process.
func NewSettingsHandler(cfg core.FrontendConfig) http.Handler {
	features := json.RawMessage(cfg.Features)
	if !json.Valid(features) {
		features = json.RawMessage("{}")
	}

	settings := frontendSettings{
		AppURL:            cfg.AppURL,
		Authority:         cfg.AuthURL,
		AuthorityClientID: cfg.ClientID,
		Debug:             cfg.Debug,
		Environment:       cfg.Environment,
		GraphQLClientURI:  cfg.AppURL + "/graphql",
		RedirectURI:       cfg.RedirectURI,
		Features:          features,
		Version:           cfg.Version,
		OtelURL:           cfg.OtelURL,
		OtelIngestKey:     cfg.OtelKey,
		FeedbackEndpoint:  cfg.FeedbackEndpoint,
	}

	body, err := json.Marshal(settings)
	if err != nil {
		body = []byte("{}")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.ForContext(r.Context()).Debug().Msg("Serving frontend settings")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}
