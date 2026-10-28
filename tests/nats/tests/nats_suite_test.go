package tests

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/joho/godotenv"

	"github.com/pyck-ai/pyck/tests/nats/config"
	"github.com/pyck-ai/pyck/tests/nats/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNatsSuite(t *testing.T) {
	// The repo's .env carries the host-facing websocket URL. godotenv keeps
	// variables that are already set, so the shell and CI still win.
	if err := godotenv.Load("../../../.env"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("load ../../../.env: %v", err)
	}

	for _, key := range []string{config.EnvNatsWSURL, config.EnvAuthJWTToken} {
		if os.Getenv(key) == "" {
			t.Fatalf("%s is not set; provision a tenant and export its values as described in tests/nats/config", key)
		}
	}

	// The specs derive the tenant from the token themselves, so the variable
	// is optional, but deriving it needs a JWT, and provision-tenant hands
	// out an opaque PAT next to the tenant ID. Fail once here rather than in
	// every spec.
	if os.Getenv(config.EnvTenantID) == "" {
		if _, err := util.GetTenantIDFromJWT(os.Getenv(config.EnvAuthJWTToken)); err != nil {
			t.Fatalf("cannot derive the tenant from %s (%v); export %s alongside it as described in tests/nats/config",
				config.EnvAuthJWTToken, err, config.EnvTenantID)
		}
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "NATS Integration Suite")
}
