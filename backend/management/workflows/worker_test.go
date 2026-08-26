//nolint:testpackage // in-package test required: workerOptions is package-private.
package workflows

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

func TestWorkerOptions(t *testing.T) {
	t.Parallel()

	t.Run("both task queues get the same deployment version", func(t *testing.T) {
		t.Parallel()
		// Versioning them apart would let a rollout promote one queue and not
		// the other, so tenant-sync and management would run different code.
		main, sync, err := workerOptions(commonworkflow.VersioningConfig{
			BuildID: "4e5950c5", DeploymentName: "management",
		})
		require.NoError(t, err)
		require.True(t, main.DeploymentOptions.UseVersioning)
		assert.Equal(t, main.DeploymentOptions, sync.DeploymentOptions)
		assert.Equal(t, "4e5950c5", main.DeploymentOptions.Version.BuildID)

		// The tenant-sync tuning survives.
		assert.Equal(t, 200, sync.MaxConcurrentActivityExecutionSize)
	})

	t.Run("unversioned by default", func(t *testing.T) {
		t.Parallel()
		// What a service image resolves today: no build ID injected, so nothing
		// registers a version and routing is unchanged.
		main, sync, err := workerOptions(commonworkflow.VersioningConfig{})
		require.NoError(t, err)
		assert.False(t, main.DeploymentOptions.UseVersioning)
		assert.False(t, sync.DeploymentOptions.UseVersioning)
	})

	t.Run("a required build ID that is missing fails the worker", func(t *testing.T) {
		t.Parallel()
		_, _, err := workerOptions(commonworkflow.VersioningConfig{RequireBuildID: true})
		require.ErrorIs(t, err, commonworkflow.ErrUnversionedBuild)
	})
}
