package workflowsdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporalworkflow "go.temporal.io/sdk/workflow"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

// The resolution is covered in common/workflow; what matters here is that the
// embedded config is the one the SDK reads.
func TestWorkerVersioningConfigIsEmbedded(t *testing.T) {
	t.Parallel()

	cfg := workflowsdk.WorkerVersioningConfig{
		VersioningConfig: commonworkflow.VersioningConfig{DeploymentName: "workflow", RequireBuildID: true},
	}
	opts, enabled, err := cfg.DeploymentOptions("v1.2.3", "github.com/pyck-ai/pyck/backend/management")
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "workflow", opts.Version.DeploymentName)
	assert.Equal(t, temporalworkflow.VersioningBehaviorPinned, opts.DefaultVersioningBehavior)
}

func TestUIBundleMetadataEntries(t *testing.T) {
	t.Parallel()

	t.Run("per-tenant stamps version only, no slug", func(t *testing.T) {
		t.Parallel()
		got := workflowsdk.UIBundleMetadataEntries(
			[]string{"PickingWorkflow", "ReceivingWorkflow"},
			commonworkflow.UIBundle{Version: "4e5950c5"},
		)
		assert.Equal(t, map[string]interface{}{
			"ui.bundle.PickingWorkflow.version":   "4e5950c5",
			"ui.bundle.ReceivingWorkflow.version": "4e5950c5",
		}, got)
	})

	t.Run("flavour stamps version and slug", func(t *testing.T) {
		t.Parallel()
		got := workflowsdk.UIBundleMetadataEntries(
			[]string{"PickingWorkflow"},
			commonworkflow.UIBundle{Version: "4e5950c5", Slug: "pyck-go"},
		)
		assert.Equal(t, map[string]interface{}{
			"ui.bundle.PickingWorkflow.version": "4e5950c5",
			"ui.bundle.PickingWorkflow.slug":    "pyck-go",
		}, got)
	})

	t.Run("no workflow types yields no entries", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, workflowsdk.UIBundleMetadataEntries(nil, commonworkflow.UIBundle{Version: "4e5950c5"}))
	})

	// A worker whose deploy never named a bundle — a shared flavour image, a
	// fly-hosted worker — must stay silent so remoteUI falls back to the
	// configured default instead of resolving a URL nothing was uploaded to.
	t.Run("no bundle version stamps nothing", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, workflowsdk.UIBundleMetadataEntries(
			[]string{"PickingWorkflow"}, commonworkflow.UIBundle{Slug: "pyck-go"}))
	})
}
