package nats_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"
)

const natsConfPath = "../../../config/nats/nats.conf"

// Nothing in the build ties the account the callout mints into user JWTs to the
// account the NATS server declares, and a mismatch rejects every
// callout-authenticated connection with "Authorization Violation" (pyck#1357).
// The account must not be the callout's own either: that one holds the
// streams, and a tenant client placed there could have the server store its
// requests' answers in any stream by choosing their reply subject.
func TestTenantAccountIsDeclaredAndNotTheStreamAccount(t *testing.T) {
	t.Setenv("NATS_USER", "user")
	t.Setenv("NATS_PASSWORD", "password")

	parsed := parseNatsConf(t)
	account := defaultTenantAccountName(t)

	authorization, _ := parsed["authorization"].(map[string]any)
	callout, _ := authorization["auth_callout"].(map[string]any)

	calloutAccount, ok := callout["account"].(string)
	if !ok {
		t.Fatalf("no auth_callout account found in %s", natsConfPath)
	}

	if calloutAccount == account {
		t.Errorf("PYCK_NATS_TENANT_ACCOUNT_NAME defaults to the auth_callout account %q, which holds the streams", account)
	}

	// The account must also be declared: that is what the server looks up to
	// place the connection.
	accounts, _ := parsed["accounts"].(map[string]any)
	if _, ok := accounts[account]; !ok {
		t.Errorf("nats.conf declares no account named %q", account)
	}
}

// parseNatsConf parses nats.conf, or skips t outside a repo checkout. The caller
// sets NATS_USER and NATS_PASSWORD, which the file reads.
func parseNatsConf(t *testing.T) map[string]any {
	t.Helper()

	if _, err := os.Stat(natsConfPath); err != nil {
		t.Skipf("cannot read %s: %v", natsConfPath, err)
	}

	parsed, err := conf.ParseFile(natsConfPath)
	if err != nil {
		t.Fatalf("parse %s: %v", natsConfPath, err)
	}

	return parsed
}

// defaultTenantAccountName is the envDefault every service falls back to when
// PYCK_NATS_TENANT_ACCOUNT_NAME is unset.
func defaultTenantAccountName(t *testing.T) string {
	t.Helper()

	field, ok := reflect.TypeOf(config.NatsConfig{}).FieldByName("NatsTenantAccountName")
	if !ok {
		t.Fatal("config.NatsConfig has no NatsTenantAccountName field")
	}

	value, ok := field.Tag.Lookup("envDefault")
	if !ok {
		t.Fatal("config.NatsConfig.NatsTenantAccountName has no envDefault tag")
	}

	return value
}

// The unit tests of the account boundary run on natstest's copy of the
// accounts, so a change to the stream account's exports or the tenant
// account's imports in nats.conf must show up there too.
func TestNatstestMirrorsNatsConfAccounts(t *testing.T) {
	t.Setenv("NATS_USER", "user")
	t.Setenv("NATS_PASSWORD", "password")

	parsed := parseNatsConf(t)

	accounts, ok := parsed["accounts"].(map[string]any)
	require.True(t, ok, "nats.conf has no accounts block")

	streamAccount, ok := accounts[natstest.StreamAccount].(map[string]any)
	require.True(t, ok, "nats.conf declares no account %q", natstest.StreamAccount)
	tenantAccount, ok := accounts[natstest.TenantAccount].(map[string]any)
	require.True(t, ok, "nats.conf declares no account %q", natstest.TenantAccount)

	exports, ok := streamAccount["exports"].([]any)
	require.True(t, ok, "%q has no exports", natstest.StreamAccount)

	got := make([]natstest.Export, 0, len(exports))
	for _, raw := range exports {
		entry, ok := raw.(map[string]any)
		require.True(t, ok, "export %v", raw)
		assert.Equal(t, []any{natstest.TenantAccount}, entry["accounts"], "export %v must go to the tenant account only", entry)

		service, ok := entry["service"].(string)
		require.True(t, ok, "export %v has no service subject", entry)

		export := natstest.Export{Subject: service}
		export.StreamResponse = entry["response_type"] == "stream"
		if threshold, ok := entry["response_threshold"].(string); ok {
			duration, err := time.ParseDuration(threshold)
			require.NoError(t, err)

			export.ResponseThreshold = duration
		}
		got = append(got, export)
	}
	assert.Equal(t, natstest.TenantExports, got, "exports of %q", natstest.StreamAccount)

	wantImports := make([]any, 0, len(natstest.TenantExports))
	for _, export := range natstest.TenantExports {
		wantImports = append(wantImports, map[string]any{
			"service": map[string]any{"account": natstest.StreamAccount, "subject": export.Subject},
		})
	}
	assert.Equal(t, wantImports, tenantAccount["imports"], "imports of %q", natstest.TenantAccount)
	assert.NotContains(t, tenantAccount, "jetstream", "the tenant account must reach JetStream only through its imports")
}
