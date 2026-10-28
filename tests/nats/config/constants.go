// Package config names the environment the NATS suites read. The stack does
// not hand these out any more: a token is served only while a live tenant
// row backs it, so the suites need a tenant of their own rather than a
// bootstrap account. Provision one and export both values with
//
//	source tests/integration/scripts/envrc.sh
//	eval "$(cd tests/integration && go run -tags=integration ./cmd/provision-tenant)"
package config

import "os"

const (
	// Environment variable names
	EnvNatsWSURL    = "PYCK_TEST_NATS_WS_URL"
	EnvAuthJWTToken = "PYCK_TEST_AUTH_TOKEN"
	EnvTenantID     = "PYCK_TEST_TENANT_ID"

	// EnvNatsStreamName is the stream the deployment under test publishes
	// to; the services read the same variable. This module deliberately
	// imports nothing from backend/, so the name is read rather than
	// shared through backend/common/env/config.
	EnvNatsStreamName = "PYCK_NATS_STREAM_NAME"

	// Default values
	DefaultTimeout = 30 // seconds

	// JWT claim keys
	JWTTenantIDClaim = "urn:zitadel:iam:user:resourceowner:id"

	// Test identifiers
	TestClientName = "NATS Integration Tests"

	// localStreamName is the stream name of the local compose stack, used
	// when EnvNatsStreamName is unset.
	localStreamName = "pyck"
)

// StreamName is the JetStream stream the suites publish to and consume
// from, read from EnvNatsStreamName so a suite run against a deployment
// whose stream is not the local one does not silently subscribe to
// subjects nothing publishes to.
func StreamName() string {
	if name := os.Getenv(EnvNatsStreamName); name != "" {
		return name
	}
	return localStreamName
}

// InboxPrefix mirrors TenantInboxPrefix in backend/common/nats, kept as a copy
// so that this module stays free of backend dependencies: the only inbox
// prefix the callout grants tenantID.
func InboxPrefix(tenantID string) string {
	return "_INBOX." + tenantID
}
