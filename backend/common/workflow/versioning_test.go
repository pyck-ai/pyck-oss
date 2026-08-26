package workflow_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporalworker "go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/env"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

func TestVersioningConfigDeploymentOptions(t *testing.T) {
	t.Parallel()

	const module = "github.com/pyck-ai/pyck/backend/management"

	t.Run("a versioned build enables pinned versioning", func(t *testing.T) {
		t.Parallel()
		cfg := commonworkflow.VersioningConfig{DeploymentName: "management", RequireBuildID: true}
		opts, on, err := cfg.DeploymentOptions("v1.2.3", module)
		require.NoError(t, err)
		assert.True(t, on)
		assert.True(t, opts.UseVersioning)
		assert.Equal(t, "management", opts.Version.DeploymentName)
		assert.Equal(t, "v1.2.3", opts.Version.BuildID)
		assert.Equal(t, temporalworkflow.VersioningBehaviorPinned, opts.DefaultVersioningBehavior)
	})

	t.Run("the controller's values outrank the module, an explicit override outranks both", func(t *testing.T) {
		t.Parallel()
		// The controller waits for a worker registered under exactly the version
		// it derived, so its values must win over anything inferred locally.
		controller := commonworkflow.VersioningConfig{
			ControllerBuildID: "twc-build", ControllerDeploymentName: "ns.wd",
		}
		opts, _, err := controller.DeploymentOptions("v1.2.3", module)
		require.NoError(t, err)
		assert.Equal(t, "twc-build", opts.Version.BuildID)
		assert.Equal(t, "ns.wd", opts.Version.DeploymentName)

		override := controller
		override.BuildID, override.DeploymentName = "explicit", "explicit-wd"
		opts, _, err = override.DeploymentOptions("v1.2.3", module)
		require.NoError(t, err)
		assert.Equal(t, "explicit", opts.Version.BuildID)
		assert.Equal(t, "explicit-wd", opts.Version.DeploymentName)
	})

	t.Run("falls back to the module version and name", func(t *testing.T) {
		t.Parallel()
		opts, on, err := commonworkflow.VersioningConfig{}.DeploymentOptions("v1.2.3", module)
		require.NoError(t, err)
		require.True(t, on)
		assert.Equal(t, "v1.2.3", opts.Version.BuildID)
		assert.Equal(t, "management", opts.Version.DeploymentName)

		opts, _, err = commonworkflow.VersioningConfig{}.DeploymentOptions("v1.2.3", "")
		require.NoError(t, err)
		assert.Equal(t, "worker", opts.Version.DeploymentName, "last-resort default")
	})

	t.Run("an unversioned build stays off by default", func(t *testing.T) {
		t.Parallel()
		// What the service images resolve: they build without module version
		// stamping, so management and file stay unversioned until a deploy opts in.
		opts, on, err := commonworkflow.VersioningConfig{}.DeploymentOptions("(devel)", "mod")
		require.NoError(t, err)
		assert.False(t, on)
		assert.False(t, opts.UseVersioning)
	})

	t.Run("an unversioned build errors when versioning is required", func(t *testing.T) {
		t.Parallel()
		for _, version := range []string{"(devel)", ""} {
			_, _, err := commonworkflow.VersioningConfig{RequireBuildID: true}.DeploymentOptions(version, "mod")
			require.ErrorIs(t, err, commonworkflow.ErrUnversionedBuild, version)
		}
	})
}

// TestStartPromotionGuard: promotion would override the
// temporal-worker-controller mid-rollout, so it must take both an explicit
// opt-in and a version to promote. A nil client proves neither case reaches
// Temporal.
func TestStartPromotionGuard(t *testing.T) {
	t.Parallel()

	versioned := temporalworker.DeploymentOptions{UseVersioning: true}

	for name, tc := range map[string]struct {
		cfg  commonworkflow.VersioningConfig
		opts temporalworker.DeploymentOptions
	}{
		"asked but unversioned": {commonworkflow.VersioningConfig{PromoteOnStart: true}, temporalworker.DeploymentOptions{}},
		"versioned but unasked": {commonworkflow.VersioningConfig{}, versioned},
		"neither":               {commonworkflow.VersioningConfig{}, temporalworker.DeploymentOptions{}},
	} {
		assert.NotPanics(t, func() { tc.cfg.StartPromotion(t.Context(), nil, tc.opts) }, name)
	}
}

// TestEmbeddedInAServiceConfig: the services get versioning by embedding this
// struct in their own config. If embedding stopped being parsed, versioning
// would simply never switch on — silently, in every environment.
func TestEmbeddedInAServiceConfig(t *testing.T) {
	type serviceConfig struct {
		commonworkflow.VersioningConfig
	}

	t.Setenv("PYCK_WORKER_BUILD_ID", "abc123")
	t.Setenv("TEMPORAL_DEPLOYMENT_NAME", "pyck-dev-workers/management")
	t.Setenv("PYCK_WORKER_REQUIRE_BUILD_ID", "true")

	_, cfg, err := env.Load[serviceConfig](context.Background())
	require.NoError(t, err)
	assert.Equal(t, "abc123", cfg.BuildID)
	assert.Equal(t, "pyck-dev-workers/management", cfg.ControllerDeploymentName)
	assert.True(t, cfg.RequireBuildID)
}
