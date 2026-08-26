// Package config loads the env-derived values shared across all integration
// suites. The set is identical to what `task bootstrap` produces in pyck —
// no per-test inputs live here, those come from internal/fixtures.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config carries every env-derived value the integration suites share:
// endpoints of the running stack, the credentials `task up` bootstraps, and
// the service sweep cadences timing-sensitive suites size their waits to.
type Config struct {
	GatewayURL string
	// InventoryURL, PickingURL, ReceivingURL, FileURL and MainDataURL are the
	// gated services' own HTTP endpoints (not the gateway). Used by the
	// per-service-role gate tests, which call a gated service directly so the
	// gate's HTTP status (403/200) is observable — the gateway would otherwise
	// fold a subgraph 403 into a GraphQL error. Management and workflow are
	// intentionally ungated and so have no entry here.
	InventoryURL     string
	PickingURL       string
	ReceivingURL     string
	FileURL          string
	MainDataURL      string
	TemporalAddress  string
	NatsURL          string
	NatsStream       string
	ServiceToken     string
	ZitadelIssuer    string
	ZitadelGrpcAddr  string
	ZitadelKeyfile   string
	ZitadelProjectID string
	// ZitadelAudience is the audience pyck's NamespaceGetter and the
	// Actions v2 webhook feed into ComputeUUID to derive tenant IDs. In
	// local compose it equals the issuer, so it defaults to ZitadelIssuer
	// when PYCK_ZITADEL_AUDIENCE is unset.
	ZitadelAudience string
	// IntrospectClientID + IntrospectClientSecret are the basic-auth
	// credentials of an OIDC app authorized to call
	// /oauth/v2/introspect. Reuses the temporal-web client that
	// bootstrap already provisions — no extra Zitadel setup needed.
	IntrospectClientID     string
	IntrospectClientSecret string
	// TenantExpiryCheckInterval and TenantReconcileInterval mirror the
	// management service's background sweep cadences so timing-sensitive
	// suites can size their observation windows instead of guessing.
	// Defaults match the service defaults (backend/management/core/config.go:
	// 1m and 5m); scripts/envrc.sh forwards the local .env overrides (5s), so
	// a mismatch only occurs if the stack was started with different values
	// than the shell running the tests — keep the two in sync.
	TenantExpiryCheckInterval time.Duration
	TenantReconcileInterval   time.Duration
}

// Load reads the environment into a Config, defaulting endpoints to the
// local compose stack and failing fast when a bootstrap credential is
// missing (source scripts/envrc.sh, or run via `task test:integration`).
func Load() (*Config, error) {
	cfg := &Config{
		GatewayURL:             envOr("PYCK_GATEWAY_URL", "http://localhost:4000"),
		InventoryURL:           envOr("PYCK_INVENTORY_URL", "http://localhost:8084"),
		PickingURL:             envOr("PYCK_PICKING_URL", "http://localhost:8086"),
		ReceivingURL:           envOr("PYCK_RECEIVING_URL", "http://localhost:8087"),
		FileURL:                envOr("PYCK_FILE_URL", "http://localhost:8088"),
		MainDataURL:            envOr("PYCK_MAIN_DATA_URL", "http://localhost:8081"),
		TemporalAddress:        envOr("PYCK_TEMPORAL_ADDRESS", "localhost:7233"),
		NatsURL:                envOr("PYCK_NATS_URL", "nats://root:root@localhost:4222"),
		NatsStream:             envOr("PYCK_NATS_STREAM_NAME", "pyck"),
		ServiceToken:           os.Getenv("PYCK_SERVICE_TOKEN"),
		ZitadelIssuer:          os.Getenv("PYCK_ZITADEL_ISSUER"),
		ZitadelGrpcAddr:        os.Getenv("PYCK_ZITADEL_GRPC_ADDR"),
		ZitadelKeyfile:         os.Getenv("PYCK_ZITADEL_ADMIN_KEYFILE"),
		ZitadelProjectID:       os.Getenv("PYCK_ZITADEL_PROJECT_ID"),
		IntrospectClientID:     os.Getenv("PYCK_TEMPORAL_WEB_CLIENT_ID"),
		IntrospectClientSecret: os.Getenv("PYCK_TEMPORAL_WEB_CLIENT_SECRET"),
	}

	// Audience defaults to the issuer — matches pyck's compose wiring,
	// where NamespaceGetter is constructed with the issuer as audience.
	cfg.ZitadelAudience = envOr("PYCK_ZITADEL_AUDIENCE", cfg.ZitadelIssuer)

	var err error
	if cfg.TenantExpiryCheckInterval, err = durationOr("PYCK_TENANT_EXPIRY_CHECK_INTERVAL", time.Minute); err != nil {
		return nil, err
	}
	if cfg.TenantReconcileInterval, err = durationOr("PYCK_TENANT_RECONCILE_INTERVAL", 5*time.Minute); err != nil {
		return nil, err
	}

	for k, v := range map[string]string{
		"PYCK_SERVICE_TOKEN":              cfg.ServiceToken,
		"PYCK_ZITADEL_ISSUER":             cfg.ZitadelIssuer,
		"PYCK_ZITADEL_GRPC_ADDR":          cfg.ZitadelGrpcAddr,
		"PYCK_ZITADEL_ADMIN_KEYFILE":      cfg.ZitadelKeyfile,
		"PYCK_ZITADEL_PROJECT_ID":         cfg.ZitadelProjectID,
		"PYCK_TEMPORAL_WEB_CLIENT_ID":     cfg.IntrospectClientID,
		"PYCK_TEMPORAL_WEB_CLIENT_SECRET": cfg.IntrospectClientSecret,
	} {
		if v == "" {
			return nil, fmt.Errorf("%s env not set", k)
		}
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
