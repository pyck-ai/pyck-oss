package workflow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"runtime/debug"
	"time"

	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// ErrUnversionedBuild is returned when a worker is asked to run versioned but
// the build carries no version.
var ErrUnversionedBuild = errors.New("worker build is unversioned")

const (
	// develBuildID is the module version Go embeds for an unversioned build.
	develBuildID = "(devel)"

	promoteRetryDelay    = 1 * time.Second
	promoteMaxRetryDelay = 30 * time.Second
)

// VersioningConfig configures Temporal Worker Deployment Versioning (#1132):
// a worker registers a deployment version so rolling deploys don't break
// in-flight executions. Lives here rather than in workflowsdk so the workers
// embedded in services share one implementation with the standalone ones.
type VersioningConfig struct {
	// Explicit overrides, outranking everything below. Must stay unset under the
	// temporal-worker-controller: it derives the version itself and then waits
	// for a worker registered under exactly that, so an override strands the
	// rollout. Pin the version on the CRD (unsafeCustomBuildID) instead.
	BuildID        string `env:"PYCK_WORKER_BUILD_ID"`
	DeploymentName string `env:"PYCK_WORKER_DEPLOYMENT_NAME"`

	// Injected into every container by the controller, and authoritative there.
	ControllerBuildID        string `env:"TEMPORAL_WORKER_BUILD_ID"`
	ControllerDeploymentName string `env:"TEMPORAL_DEPLOYMENT_NAME"`

	// Refuse to start on an unversioned ("(devel)") build. False by default so
	// local dev runs out of the box; deploys set it true.
	RequireBuildID bool `env:"PYCK_WORKER_REQUIRE_BUILD_ID" envDefault:"false"`

	// Promote this worker's version on start, for deployments the
	// temporal-worker-controller does not manage. MUST stay false under the
	// controller: promotion is its decision there, gradual rollout included, and
	// a self-promoting worker would override it mid-rollout.
	PromoteOnStart bool `env:"PYCK_WORKER_PROMOTE_ON_START" envDefault:"false"`
}

// resolveBuildID and resolveDeploymentName share one precedence: explicit
// override, then what the controller injected, then the Go module for local runs.
func resolveBuildID(override, controller, moduleVersion string) string {
	return cmp.Or(override, controller, moduleVersion)
}

func resolveDeploymentName(override, controller, modulePath string) string {
	if name := cmp.Or(override, controller); name != "" {
		return name
	}
	if name := path.Base(modulePath); name != "" && name != "." && name != "/" {
		return name
	}
	return "worker"
}

// DeploymentOptions resolves the Temporal deployment-versioning options for a
// build. Behaviour is Pinned: an execution stays on the version it started on.
//
// The bool reports whether versioning is on. An unversioned build turns it off
// (local dev) unless RequireBuildID demands otherwise, in which case it errors:
// a deploy that expected versioning must not silently run without it.
func (c VersioningConfig) DeploymentOptions(moduleVersion, modulePath string) (temporalworker.DeploymentOptions, bool, error) {
	buildID := resolveBuildID(c.BuildID, c.ControllerBuildID, moduleVersion)
	if buildID == develBuildID || buildID == "" {
		if c.RequireBuildID {
			return temporalworker.DeploymentOptions{}, false, fmt.Errorf(
				"%w (%q): build a versioned binary or set PYCK_WORKER_BUILD_ID; PYCK_WORKER_REQUIRE_BUILD_ID=true forbids unversioned workers",
				ErrUnversionedBuild, buildID)
		}
		return temporalworker.DeploymentOptions{}, false, nil
	}
	return temporalworker.DeploymentOptions{
		UseVersioning: true,
		Version: temporalworker.WorkerDeploymentVersion{
			DeploymentName: resolveDeploymentName(c.DeploymentName, c.ControllerDeploymentName, modulePath),
			BuildID:        buildID,
		},
		DefaultVersioningBehavior: temporalworkflow.VersioningBehaviorPinned,
	}, true, nil
}

// DeploymentOptionsFromBuild is DeploymentOptions against this binary's own Go
// build info, for a worker embedded in a service rather than started by
// workflowsdk's RunDefaultWorker. The service images build without module
// version stamping, so they resolve "(devel)" and stay unversioned until a
// build ID is injected — versioning cannot switch itself on unnoticed.
//
// Unreadable build info is treated as no module version rather than an error:
// it only matters when nothing else supplied one, and that case already fails
// or falls back on RequireBuildID.
func (c VersioningConfig) DeploymentOptionsFromBuild() (temporalworker.DeploymentOptions, bool, error) {
	var version, modulePath string
	if info, ok := debug.ReadBuildInfo(); ok {
		version, modulePath = info.Main.Version, info.Main.Path
	}

	return c.DeploymentOptions(version, modulePath)
}

// StartPromotion makes the worker's own version current in the background, so
// Temporal routes new executions to it. This is the API equivalent of
// `temporal worker deployment set-current-version`: a deploy needs no Temporal
// CLI (#1132, Approach B).
//
// No-op unless PromoteOnStart is set and the worker is versioned — under the
// temporal-worker-controller promotion is its decision, gradual rollout
// included.
func (c VersioningConfig) StartPromotion(ctx context.Context, cl temporalclient.Client, opts temporalworker.DeploymentOptions) {
	if !c.PromoteOnStart || !opts.UseVersioning {
		return
	}

	go promoteVersion(ctx, cl, opts.Version)
}

// promoteVersion retries until ctx ends: a version registers a moment after its
// workers start polling, and Temporal refuses to promote until every task queue
// the deployment served is covered by it.
func promoteVersion(ctx context.Context, c temporalclient.Client, version temporalworker.WorkerDeploymentVersion) {
	logger := log.ForContext(ctx)
	handle := c.WorkerDeploymentClient().GetHandle(version.DeploymentName)
	opts := temporalclient.WorkerDeploymentSetCurrentVersionOptions{BuildID: version.BuildID}

	delay := promoteRetryDelay
	for {
		_, err := handle.SetCurrentVersion(ctx, opts)
		if err == nil {
			logger.Info().
				Str("deployment", version.DeploymentName).
				Str("build_id", version.BuildID).
				Msg("promoted worker deployment version to current")

			return
		}
		logger.Warn().Err(err).Dur("retry_in", delay).
			Msg("promoting worker deployment version failed; new executions keep routing to the previous version")

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, promoteMaxRetryDelay)
	}
}
