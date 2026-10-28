package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
)

// setRequiredEnv provides every credential Load fails fast on, so the
// cadence cases below only exercise the duration handling.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PYCK_SERVICE_TOKEN":              "token",
		"PYCK_ZITADEL_ISSUER":             "http://localhost:8080",
		"PYCK_ZITADEL_GRPC_ADDR":          "localhost:8080",
		"PYCK_ZITADEL_ADMIN_KEYFILE":      "/keys/zitadel-admin-sa.json",
		"PYCK_ZITADEL_PROJECT_ID":         "project",
		"PYCK_TEMPORAL_WEB_CLIENT_ID":     "client",
		"PYCK_TEMPORAL_WEB_CLIENT_SECRET": "secret",
	} {
		t.Setenv(k, v)
	}
}

// The cadence defaults mirror backend/management/core/config.go so a suite
// run without envrc.sh sizes its windows to the service defaults, which
// makes the sweep-driven tests skip loudly instead of timing out.
func TestLoad_SweepCadencesDefaultToServiceDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PYCK_TENANT_EXPIRY_CHECK_INTERVAL", "")
	t.Setenv("PYCK_TENANT_RECONCILE_INTERVAL", "")
	t.Setenv("PYCK_ZITADEL_SYNC_EVERY", "")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, time.Minute, cfg.TenantExpiryCheckInterval)
	assert.Equal(t, 5*time.Minute, cfg.TenantReconcileInterval)
	assert.Equal(t, 10*time.Minute, cfg.ZitadelSyncInterval)
}

// envrc.sh forwards PYCK_ZITADEL_SYNC_EVERY from the stack's .env; Load must
// read that exact name (the service's env var), not an *_INTERVAL alias.
func TestLoad_ReadsZitadelSyncEvery(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PYCK_ZITADEL_SYNC_EVERY", "5s")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, 5*time.Second, cfg.ZitadelSyncInterval)
}

// A malformed cadence must fail Load naming the variable, not fall back to
// the default and silently size every window wrong.
func TestLoad_RejectsMalformedZitadelSyncEvery(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PYCK_ZITADEL_SYNC_EVERY", "five-seconds")

	cfg, err := config.Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.ErrorContains(t, err, "PYCK_ZITADEL_SYNC_EVERY")
}
