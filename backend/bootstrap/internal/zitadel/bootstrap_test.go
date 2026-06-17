package zitadel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/env"
)

// TestLoadConfiguration checks if the configuration ENV is parsed correctly.
func TestLoadConfiguration(t *testing.T) {
	testCases := []struct {
		name           string
		setupEnv       func()
		cleanupEnv     func()
		expectedError  bool
		validateConfig func(t *testing.T, cfg Configuration)
	}{
		{
			name: "LoadConfigurationWithAllFields",
			setupEnv: func() {
				os.Setenv("PYCK_ZITADEL_ISSUER", "https://auth.prod.pyck.cloud")
				os.Setenv("PYCK_ZITADEL_OAUTH_URL", "http://auth.prod.pyck.cloud:443")
				os.Setenv("PYCK_ZITADEL_GRPC_ADDR", "auth.prod.pyck.cloud:443")
				os.Setenv("PYCK_BOOTSTRAP_ZITADEL_KEY_PATH", "/custom/keys")
				os.Setenv("PYCK_BOOTSTRAP_ZITADEL_ENV_PATH", "/custom/env")
				os.Setenv("PYCK_BOOTSTRAP_ZITADEL_K8S_NAMESPACE", "production")
				os.Setenv("PYCK_BOOTSTRAP_ZITADEL_K8S_SECRET_NAME", "pyck-tenant-secrets")
				os.Setenv("PYCK_BOOTSTRAP_ZITADEL_K8S_IN_CLUSTER", "false")
				os.Setenv("PYCK_BOOTSTRAP_ZITADEL_K8S_CONFIG_PATH", "/home/user/.kube/config")
				os.Setenv("LOG_LEVEL", "debug")
			},
			cleanupEnv: func() {
				os.Unsetenv("PYCK_ZITADEL_ISSUER")
				os.Unsetenv("PYCK_ZITADEL_OAUTH_URL")
				os.Unsetenv("PYCK_ZITADEL_GRPC_ADDR")
				os.Unsetenv("PYCK_BOOTSTRAP_ZITADEL_KEY_PATH")
				os.Unsetenv("PYCK_BOOTSTRAP_ZITADEL_ENV_PATH")
				os.Unsetenv("PYCK_BOOTSTRAP_ZITADEL_K8S_NAMESPACE")
				os.Unsetenv("PYCK_BOOTSTRAP_ZITADEL_K8S_SECRET_NAME")
				os.Unsetenv("PYCK_BOOTSTRAP_ZITADEL_K8S_IN_CLUSTER")
				os.Unsetenv("PYCK_BOOTSTRAP_ZITADEL_K8S_CONFIG_PATH")
				os.Unsetenv("LOG_LEVEL")
			},
			expectedError: false,
			validateConfig: func(t *testing.T, cfg Configuration) {
				assert.Equal(t, "https://auth.prod.pyck.cloud", cfg.Issuer)
				assert.Equal(t, "http://auth.prod.pyck.cloud:443", cfg.OAuthURL)
				assert.Equal(t, "auth.prod.pyck.cloud:443", cfg.GrpcAddr)
				assert.Equal(t, "/custom/keys", cfg.KeyPath)
				assert.Equal(t, "/custom/env", cfg.EnvPath)
				assert.Equal(t, "production", cfg.K8sNamespace)
				assert.Equal(t, "pyck-tenant-secrets", cfg.K8sSecretName)
				assert.Equal(t, false, cfg.K8sInCluster)
				assert.Equal(t, "/home/user/.kube/config", cfg.K8sConfigPath)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setupEnv()
			defer tc.cleanupEnv()

			_, cfg, err := env.Load[Configuration](t.Context())

			if tc.expectedError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if tc.validateConfig != nil {
					tc.validateConfig(t, cfg)
				}
			}
		})
	}
}

// TestNewSeederDefaults verifies that New() applies correct defaults when
// optional Configuration fields are empty.
func TestNewSeederDefaults(t *testing.T) {
	// Create a temporary directory to act as keyPath
	tmpDir := t.TempDir()

	cfg := Configuration{
		KeyPath: tmpDir,
		// All other fields intentionally left empty to test defaults
	}

	seeder, err := New(t.Context(), cfg)
	require.NoError(t, err)

	assert.Equal(t, defaultIssuer, seeder.issuer, "should fall back to default issuer")
	assert.Equal(t, defaultOAuthURL, seeder.oauthURL, "should fall back to default OAuth URL")
	assert.Equal(t, defaultGrpcAddr, seeder.grpcAddr, "should fall back to default gRPC address")
	assert.Equal(t, tmpDir, seeder.keyPath, "should use provided keyPath")
	assert.Equal(t, filepath.Join(tmpDir, zitadelKeyFile), seeder.adminKeyFile, "adminKeyFile should combine keyPath and zitadelKeyFile")
}

// TestNewSeederCustomValues verifies that New() uses provided values over defaults.
func TestNewSeederCustomValues(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Configuration{
		Issuer:   "https://custom.issuer.example.com",
		OAuthURL: "http://custom.api.example.com:443",
		GrpcAddr: "custom.api.example.com:443",
		KeyPath:  tmpDir,
		EnvPath:  "/custom/env",
	}

	seeder, err := New(t.Context(), cfg)
	require.NoError(t, err)

	assert.Equal(t, "https://custom.issuer.example.com", seeder.issuer)
	assert.Equal(t, "http://custom.api.example.com:443", seeder.oauthURL)
	assert.Equal(t, "custom.api.example.com:443", seeder.grpcAddr)
	assert.Equal(t, tmpDir, seeder.keyPath)
}

// TestNewSeederEnvPathDefault verifies that EnvPath falls back to defaultKeyPath
// when not specified.
func TestNewSeederEnvPathDefault(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Configuration{
		KeyPath: tmpDir,
		// EnvPath intentionally left empty
	}

	// New() uses envPath internally for the env exporter;
	// verify it doesn't error with empty EnvPath
	_, err := New(t.Context(), cfg)
	require.NoError(t, err)
}

// TestNewSeederInvalidKeyPath verifies that New() returns an error when the
// key path doesn't exist.
func TestNewSeederInvalidKeyPath(t *testing.T) {
	cfg := Configuration{
		KeyPath: "/nonexistent/path/that/does/not/exist",
	}

	_, err := New(t.Context(), cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
