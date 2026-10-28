package config_test

import (
	"testing"

	"github.com/pyck-ai/pyck/tests/nats/config"
)

// The stream name used to be a constant spelled "pyck" in two modules. A
// suite run against a deployment whose stream is named differently then
// subscribed to subjects nothing publishes to and reported "no messages"
// as a product failure rather than as a misconfiguration.

func TestStreamName_ReadsTheEnvironment(t *testing.T) {
	t.Setenv(config.EnvNatsStreamName, "pyck-staging")

	if got := config.StreamName(); got != "pyck-staging" {
		t.Fatalf("StreamName() = %q, want the configured %q", got, "pyck-staging")
	}
}

// An unset variable must keep the local stack working out of the box: the
// suites are run by hand against compose more often than against anything
// else, and requiring a variable there would only add a footgun.
func TestStreamName_FallsBackToTheLocalStack(t *testing.T) {
	t.Setenv(config.EnvNatsStreamName, "")

	if got := config.StreamName(); got != "pyck" {
		t.Fatalf("StreamName() = %q, want the local default %q", got, "pyck")
	}
}

// The variable is the one the services themselves read. Spelling it
// differently here would look configured and behave as if it were not.
func TestStreamName_UsesTheServiceVariableName(t *testing.T) {
	t.Parallel()

	if config.EnvNatsStreamName != "PYCK_NATS_STREAM_NAME" {
		t.Fatalf("EnvNatsStreamName = %q, want the variable the services read", config.EnvNatsStreamName)
	}
}
