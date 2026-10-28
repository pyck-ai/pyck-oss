package config_test

import (
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"

	"github.com/pyck-ai/pyck/backend/temporal/config"
)

// The shipped defaults must satisfy the relation NewClaimMapper enforces at
// startup: a deployment that sets neither PYCK_TEMPORAL_CLAIMS_TIMEOUT nor
// PYCK_LIVENESS_LOOKUP_TIMEOUT must boot.
func TestClaimsTimeoutDefaultExceedsTheLivenessLookupDefault(t *testing.T) {
	t.Parallel()

	claims, err := env.ParseAs[config.ClaimMapperConfig]()
	require.NoError(t, err)
	live, err := env.ParseAs[envconfig.LivenessConfig]()
	require.NoError(t, err)

	assert.Greater(t, claims.ClaimsTimeout, live.LivenessLookupTimeout)
}
