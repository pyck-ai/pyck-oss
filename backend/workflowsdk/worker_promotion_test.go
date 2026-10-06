package workflowsdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

// fakePromotionClient records the deployment and build ID the SDK promotes. The
// embedded nil interfaces panic on anything else.
type fakePromotionClient struct {
	temporalclient.Client

	deployments chan string
	builds      chan string
}

func newFakePromotionClient() *fakePromotionClient {
	return &fakePromotionClient{deployments: make(chan string, 4), builds: make(chan string, 4)}
}

func (c *fakePromotionClient) WorkerDeploymentClient() temporalclient.WorkerDeploymentClient {
	return fakeDeploymentClient{c: c}
}

type fakeDeploymentClient struct {
	temporalclient.WorkerDeploymentClient

	c *fakePromotionClient
}

func (d fakeDeploymentClient) GetHandle(name string) temporalclient.WorkerDeploymentHandle {
	return fakePromotionHandle{c: d.c, deployment: name}
}

type fakePromotionHandle struct {
	temporalclient.WorkerDeploymentHandle

	c          *fakePromotionClient
	deployment string
}

func (h fakePromotionHandle) SetCurrentVersion(_ context.Context, opts temporalclient.WorkerDeploymentSetCurrentVersionOptions) (temporalclient.WorkerDeploymentSetCurrentVersionResponse, error) {
	h.c.deployments <- h.deployment
	h.c.builds <- opts.BuildID
	return temporalclient.WorkerDeploymentSetCurrentVersionResponse{}, nil
}

func versionedDeployment() temporalworker.DeploymentOptions {
	return temporalworker.DeploymentOptions{
		UseVersioning: true,
		Version:       temporalworker.WorkerDeploymentVersion{DeploymentName: "orders", BuildID: "v1.2.3"},
	}
}

// setPromoteOnStart sets the process-wide flag for one test. Callers must not
// run in parallel.
func setPromoteOnStart(t *testing.T, on bool) {
	t.Helper()

	prev := workflowsdk.Config.PromoteOnStart
	t.Cleanup(func() { workflowsdk.Config.PromoteOnStart = prev })
	workflowsdk.Config.PromoteOnStart = on
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartPromotion_PromotesWhenFlagSet(t *testing.T) {
	setPromoteOnStart(t, true)

	cl := newFakePromotionClient()
	workflowsdk.StartPromotion(t.Context(), cl, versionedDeployment())

	select {
	case got := <-cl.deployments:
		assert.Equal(t, "orders", got)
	case <-time.After(5 * time.Second):
		t.Fatal("the worker's version was never promoted although PYCK_WORKER_PROMOTE_ON_START is set")
	}
	assert.Equal(t, "v1.2.3", <-cl.builds)
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartPromotion_NoOpWithoutFlagOrVersioning(t *testing.T) {
	t.Run("flag unset", func(t *testing.T) {
		setPromoteOnStart(t, false)

		cl := newFakePromotionClient()
		workflowsdk.StartPromotion(t.Context(), cl, versionedDeployment())

		require.Never(t, func() bool { return len(cl.deployments) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
			"a worker that is not told to self-promote must leave promotion to its controller")
	})

	t.Run("unversioned build", func(t *testing.T) {
		setPromoteOnStart(t, true)

		cl := newFakePromotionClient()
		workflowsdk.StartPromotion(t.Context(), cl, temporalworker.DeploymentOptions{})

		require.Never(t, func() bool { return len(cl.deployments) > 0 }, 200*time.Millisecond, 10*time.Millisecond)
	})
}
