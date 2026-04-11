package config

const (
	// Environment variable names
	EnvNatsWSURL    = "PYCK_TEST_NATS_WS_URL"
	EnvAuthJWTToken = "PYCK_TEST_AUTH_TOKEN"
	EnvTenantID     = "PYCK_TEST_TENANT_ID"

	// Default values
	DefaultStreamName = "pyck"
	DefaultTimeout    = 30 // seconds

	// JWT claim keys
	JWTTenantIDClaim = "urn:zitadel:iam:user:resourceowner:id"

	// Test identifiers
	TestClientName = "NATS Integration Tests"
)
