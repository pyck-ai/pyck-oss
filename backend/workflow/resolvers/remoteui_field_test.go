package resolvers_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
)

// Tests for the WorkflowExecutionInfo.remoteUI field (#1455).

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	executionsWithRemoteUIQuery = resolver.ParseTemplate(`query {
		workflowExecutions(first: 20) {
			edges {
				node {
					execution { workflowId id }
					type { name }
					remoteUI { webURL mobileURL }
				}
			}
		}
	}`)

	executionsWithoutRemoteUIQuery = resolver.ParseTemplate(`query {
		workflowExecutions(first: 20) {
			edges {
				node {
					execution { workflowId id }
					type { name }
				}
			}
		}
	}`)
)

type executionRemoteUINode struct {
	Execution struct {
		WorkflowID string
		ID         string
	}
	Type struct {
		Name string
	}
	RemoteUI *struct {
		WebURL    string
		MobileURL string
	}
}

type executionsRemoteUIData struct {
	WorkflowExecutions struct {
		Edges []struct {
			Node executionRemoteUINode
		}
	}
}

// pathError decodes a GraphQL error whose path contains list indices (the
// shared resolver.GQLError only decodes string paths).
type pathError struct {
	Message string
	Path    []any
}

type executionsRemoteUIResult struct {
	Data   executionsRemoteUIData
	Errors []pathError
}

// execWithErrors runs the listing and returns data plus any path-scoped errors.
func execWithErrors(te *testEnv, ctx context.Context) executionsRemoteUIResult {
	te.t.Helper()
	closeResp, resp, err := te.SendQuery(te.t, ctx, executionsWithRemoteUIQuery, nil)
	defer closeResp()
	require.NoError(te.t, err)
	var result executionsRemoteUIResult
	require.NoError(te.t, te.ReadResponse(te.t, resp, &result))
	return result
}

// execAsync runs the listing on its own goroutine and reports the decoded
// result or the transport error, so a failure surfaces on the test goroutine
// instead of hanging it.
func execAsync(te *testEnv, ctx context.Context) <-chan asyncResult {
	done := make(chan asyncResult, 1)
	go func() {
		closeResp, resp, err := te.SendQuery(te.t, ctx, executionsWithRemoteUIQuery, nil)
		defer closeResp()
		if err != nil {
			done <- asyncResult{err: err}
			return
		}
		var result executionsRemoteUIResult
		err = te.ReadResponse(te.t, resp, &result)
		done <- asyncResult{result: result, err: err}
	}()
	return done
}

type asyncResult struct {
	result executionsRemoteUIResult
	err    error
}

// =============================================================================
// FIXTURES
// =============================================================================

const (
	fieldWebTmpl    = "https://cdn.example.com/web/{{.Slug}}/{{.Version}}/mf-manifest.json"
	fieldMobileTmpl = "https://cdn.example.com/mobile/{{.Slug}}/{{.Version}}/widgets.rfw"

	// Polling bounds for the concurrency test.
	waitFor = 5 * time.Second
	tick    = 5 * time.Millisecond
)

// listedExecution describes one row the fake visibility listing returns.
type listedExecution struct {
	workflowID string
	tenantID   uuid.UUID
	typeName   string
	deployment string // "" = unversioned
	buildID    string
}

// executionProto builds a visibility row with the tenant attribute and, when
// versioned, the Temporal deployment attributes in the server's
// "<deployment>:<build>" form.
func executionProto(t *testing.T, e listedExecution) *workflowpb.WorkflowExecutionInfo {
	t.Helper()
	sa := map[string]string{commonworkflow.PyckTenantIDKey: e.tenantID.String()}
	if e.deployment != "" {
		sa[commonworkflow.TemporalWorkerDeploymentKey] = e.deployment
		sa[commonworkflow.TemporalWorkerDeploymentVersionKey] = e.deployment + ":" + e.buildID
	}
	return &workflowpb.WorkflowExecutionInfo{
		Execution:        &commonpb.WorkflowExecution{WorkflowId: e.workflowID, RunId: "run-" + e.workflowID},
		Type:             &commonpb.WorkflowType{Name: e.typeName},
		SearchAttributes: &commonpb.SearchAttributes{IndexedFields: uiBundleMeta(t, sa)},
	}
}

// listingTemporal returns a fake Temporal client whose visibility listing
// yields the given rows and whose deployment client serves the given handles.
func listingTemporal(t *testing.T, rows []listedExecution, handles map[string]*fakeWDHandle) *fakeTemporalClient {
	t.Helper()
	execs := make([]*workflowpb.WorkflowExecutionInfo, 0, len(rows))
	for _, r := range rows {
		execs = append(execs, executionProto(t, r))
	}
	simple := mocks.NewSimpleMockTemporalClient()
	simple.ListWorkflowFunc = func(context.Context, *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return &workflowservice.ListWorkflowExecutionsResponse{Executions: execs}, nil
	}
	return &fakeTemporalClient{
		SimpleMockTemporalClient: simple,
		wdc:                      &fakeWDC{handles: handles},
	}
}

// stampedHandle is a deployment handle whose build "b1" (the build the listed
// rows pin) is stamped with the version-wide bundle picking/1.2.3.
func stampedHandle(t *testing.T) *fakeWDHandle {
	t.Helper()
	return &fakeWDHandle{versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
		"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
			commonworkflow.UIBundleMetadataKey("", "slug"):    "picking",
			commonworkflow.UIBundleMetadataKey("", "version"): "1.2.3",
		})}},
	}}
}

// templatedMgmt serves the same templates for every tenant.
func templatedMgmt(data map[string]any) *fakeMgmtClient {
	return &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
		return tenantsWithData(data), nil
	}}
}

// namespacedClientFactory caches one workflow client per namespace, each over
// its own fake Temporal, like production's per-tenant clients.
type namespacedClientFactory struct {
	temporals map[string]temporalclient.Client
	mu        sync.Mutex
	clients   map[string]*commonworkflow.Client
}

func (f *namespacedClientFactory) GetClient(_ context.Context, namespace string) (*commonworkflow.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[namespace]; ok {
		return c, nil
	}
	tc, ok := f.temporals[namespace]
	if !ok {
		return nil, errors.New("no temporal for namespace " + namespace)
	}
	c, err := commonworkflow.NewClient(namespace, tc)
	if err != nil {
		return nil, err
	}
	if f.clients == nil {
		f.clients = map[string]*commonworkflow.Client{}
	}
	f.clients[namespace] = c
	return c, nil
}

func (f *namespacedClientFactory) Close() {}

// =============================================================================
// TESTS
// =============================================================================

func TestWorkflowExecutionsRemoteUIField(t *testing.T) {
	t.Parallel()

	t.Run("renders pinned executions from the listing without describing them", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
			{workflowID: "wf-2", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
			{workflowID: "wf-3", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})

		te := setupRemoteUI(t, templatedMgmt(map[string]any{
			"remoteWebUITemplate":    fieldWebTmpl,
			"remoteMobileUITemplate": fieldMobileTmpl,
		}), &remoteUIClientFactory{temporal: temporal})

		data := execOK[executionsRemoteUIData](te, te.ctx(userA), executionsWithRemoteUIQuery, nil)

		require.Len(t, data.WorkflowExecutions.Edges, 3)
		for _, edge := range data.WorkflowExecutions.Edges {
			require.NotNil(t, edge.Node.RemoteUI, "node %s", edge.Node.Execution.WorkflowID)
			assert.Equal(t, "https://cdn.example.com/web/picking/1.2.3/mf-manifest.json", edge.Node.RemoteUI.WebURL)
			assert.Equal(t, "https://cdn.example.com/mobile/picking/1.2.3/widgets.rfw", edge.Node.RemoteUI.MobileURL)
		}

		// The pinned version comes from the row's search attributes ...
		assert.Zero(t, temporal.describeCalls.Load(), "listing field must not DescribeWorkflowExecution per row")
		// ... and one version resolves once, however many rows share it.
		assert.Equal(t, int64(1), handle.describeVersionCalls.Load(), "concurrent rows on one version must share a single DescribeVersion")
	})

	t.Run("matches the top-level remoteUI query for the same execution", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})
		// Make the describe path agree with the listing row.
		temporal.workflowType = "PickingWorkflow"
		temporal.version = pinnedVersion()

		te := setupRemoteUI(t, templatedMgmt(map[string]any{
			"remoteWebUITemplate":    fieldWebTmpl,
			"remoteMobileUITemplate": fieldMobileTmpl,
		}), &remoteUIClientFactory{temporal: temporal})

		listed := execOK[executionsRemoteUIData](te, te.ctx(userA), executionsWithRemoteUIQuery, nil)
		single := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-wf-1",
		})

		require.Len(t, listed.WorkflowExecutions.Edges, 1)
		require.NotNil(t, listed.WorkflowExecutions.Edges[0].Node.RemoteUI)
		assert.Equal(t, single.RemoteUI.WebURL, listed.WorkflowExecutions.Edges[0].Node.RemoteUI.WebURL)
		assert.Equal(t, single.RemoteUI.MobileURL, listed.WorkflowExecutions.Edges[0].Node.RemoteUI.MobileURL)
	})

	t.Run("serves the default bundle for unversioned executions", func(t *testing.T) {
		t.Parallel()

		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow"},
		}, nil)

		te := setupRemoteUIWithDefaults(t, templatedMgmt(map[string]any{
			"remoteWebUITemplate":    fieldWebTmpl,
			"remoteMobileUITemplate": fieldMobileTmpl,
		}), &remoteUIClientFactory{temporal: temporal}, resolvers.RemoteUIDefaults{
			Bundle: &commonworkflow.UIBundle{Slug: "legacy", Version: "0.9.0"},
		})

		data := execOK[executionsRemoteUIData](te, te.ctx(userA), executionsWithRemoteUIQuery, nil)

		require.Len(t, data.WorkflowExecutions.Edges, 1)
		require.NotNil(t, data.WorkflowExecutions.Edges[0].Node.RemoteUI)
		assert.Equal(t, "https://cdn.example.com/web/legacy/0.9.0/mf-manifest.json", data.WorkflowExecutions.Edges[0].Node.RemoteUI.WebURL)
		assert.Zero(t, temporal.describeCalls.Load())
	})

	t.Run("is null without an error for unversioned executions when no default is configured", func(t *testing.T) {
		t.Parallel()

		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow"},
		}, nil)

		te := setupRemoteUI(t, templatedMgmt(map[string]any{
			"remoteWebUITemplate":    fieldWebTmpl,
			"remoteMobileUITemplate": fieldMobileTmpl,
		}), &remoteUIClientFactory{temporal: temporal})

		data := execOK[executionsRemoteUIData](te, te.ctx(userA), executionsWithRemoteUIQuery, nil)

		require.Len(t, data.WorkflowExecutions.Edges, 1)
		assert.Nil(t, data.WorkflowExecutions.Edges[0].Node.RemoteUI)
		assert.Equal(t, "wf-1", data.WorkflowExecutions.Edges[0].Node.Execution.WorkflowID, "the rest of the node is intact")
	})

	t.Run("is null without an error when the tenant has no UI templates", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})

		var mgmtCalls atomic.Int64
		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			mgmtCalls.Add(1)
			return tenantsWithData(map[string]any{}), nil
		}}
		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		for range 2 {
			data := execOK[executionsRemoteUIData](te, te.ctx(userA), executionsWithRemoteUIQuery, nil)
			require.Len(t, data.WorkflowExecutions.Edges, 1)
			assert.Nil(t, data.WorkflowExecutions.Edges[0].Node.RemoteUI)
		}
		assert.Zero(t, handle.describeVersionCalls.Load(), "no Temporal lookup when there is nothing to render into")
		assert.Equal(t, int64(1), mgmtCalls.Load(), "the negative verdict is cached across listings")
	})

	t.Run("is null without an error when the tenant is unknown to management", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})

		var mgmtCalls atomic.Int64
		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			mgmtCalls.Add(1)
			return noTenants(), nil
		}}
		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		for range 2 {
			result := execWithErrors(te, te.ctx(userA))
			require.Len(t, result.Data.WorkflowExecutions.Edges, 1)
			assert.Nil(t, result.Data.WorkflowExecutions.Edges[0].Node.RemoteUI)
			assert.Empty(t, result.Errors)
		}
		assert.Equal(t, int64(1), mgmtCalls.Load(), "the negative verdict is cached across listings")
	})

	t.Run("is null without an error when the tenant template is malformed", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})

		te := setupRemoteUI(t, templatedMgmt(map[string]any{
			"remoteWebUITemplate": "not-an-absolute-url/{{.Slug}}/{{.Version}}",
		}), &remoteUIClientFactory{temporal: temporal})

		result := execWithErrors(te, te.ctx(userA))

		require.Len(t, result.Data.WorkflowExecutions.Edges, 1)
		assert.Nil(t, result.Data.WorkflowExecutions.Edges[0].Node.RemoteUI)
		assert.Empty(t, result.Errors, "a tenant-configuration problem must not fail the listing")
	})

	t.Run("does not fail the listing when a management lookup errors", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})

		var mgmtCalls atomic.Int64
		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			mgmtCalls.Add(1)
			return nil, errors.New("management unreachable")
		}}
		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		for range 2 {
			result := execWithErrors(te, te.ctx(userA))
			require.Len(t, result.Data.WorkflowExecutions.Edges, 1)
			assert.Nil(t, result.Data.WorkflowExecutions.Edges[0].Node.RemoteUI)
			assert.Empty(t, result.Errors, "an infrastructure failure must not fail the listing")
		}
		assert.Equal(t, int64(2), mgmtCalls.Load(), "a transport failure is not cached")
	})

	t.Run("is null without an error on an infrastructure failure, the healthy sibling still renders", func(t *testing.T) {
		t.Parallel()

		healthy := stampedHandle(t)
		broken := &fakeWDHandle{describeVersionErr: errors.New("deployment service down")}
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-broken", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "broken", buildID: "b9"},
			{workflowID: "wf-ok", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": healthy, "broken": broken})

		te := setupRemoteUI(t, templatedMgmt(map[string]any{
			"remoteWebUITemplate":    fieldWebTmpl,
			"remoteMobileUITemplate": fieldMobileTmpl,
		}), &remoteUIClientFactory{temporal: temporal})

		result := execWithErrors(te, te.ctx(userA))

		edges := result.Data.WorkflowExecutions.Edges
		require.Len(t, edges, 2)
		byID := map[string]executionRemoteUINode{}
		for _, e := range edges {
			byID[e.Node.Execution.WorkflowID] = e.Node
		}
		assert.Nil(t, byID["wf-broken"].RemoteUI)
		require.NotNil(t, byID["wf-ok"].RemoteUI, "the healthy sibling is still rendered")
		assert.Equal(t, "https://cdn.example.com/web/picking/1.2.3/mf-manifest.json", byID["wf-ok"].RemoteUI.WebURL)

		assert.Empty(t, result.Errors, "a per-row failure must not fail the listing for gqlgenc clients")
	})

	t.Run("renders each row with the templates of its own tenant", func(t *testing.T) {
		t.Parallel()

		handleA := stampedHandle(t)
		handleB := stampedHandle(t)
		temporalA := listingTemporal(t, []listedExecution{
			{workflowID: "wf-a", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handleA})
		temporalB := listingTemporal(t, []listedExecution{
			{workflowID: "wf-b", tenantID: tenantB, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handleB})

		var mgmtCalls atomic.Int64
		mgmt := &fakeMgmtClient{getTenants: func(_ context.Context, args managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			mgmtCalls.Add(1)
			require.NotNil(t, args.Where)
			require.NotNil(t, args.Where.ID)
			host := map[string]string{
				tenantA.String(): "a.example.com",
				tenantB.String(): "b.example.com",
			}[*args.Where.ID]
			require.NotEmpty(t, host, "unexpected tenant lookup %s", *args.Where.ID)
			return tenantsWithData(map[string]any{
				"remoteWebUITemplate":    "https://" + host + "/web/{{.Slug}}/{{.Version}}/mf-manifest.json",
				"remoteMobileUITemplate": "https://" + host + "/mobile/{{.Slug}}/{{.Version}}/widgets.rfw",
			}), nil
		}}

		te := setupRemoteUI(t, mgmt, &namespacedClientFactory{temporals: map[string]temporalclient.Client{
			tenantA.String(): temporalA,
			tenantB.String(): temporalB,
		}})

		// The request names both tenants; the header tenant must not leak into
		// the other tenant's rows.
		data := execOK[executionsRemoteUIData](te, te.ctxWithTenants(userAB, tenantA, tenantB), executionsWithRemoteUIQuery, nil)

		require.Len(t, data.WorkflowExecutions.Edges, 2)
		byID := map[string]executionRemoteUINode{}
		for _, e := range data.WorkflowExecutions.Edges {
			byID[e.Node.Execution.WorkflowID] = e.Node
		}
		require.NotNil(t, byID["wf-a"].RemoteUI)
		require.NotNil(t, byID["wf-b"].RemoteUI)
		assert.Equal(t, "https://a.example.com/web/picking/1.2.3/mf-manifest.json", byID["wf-a"].RemoteUI.WebURL)
		assert.Equal(t, "https://b.example.com/web/picking/1.2.3/mf-manifest.json", byID["wf-b"].RemoteUI.WebURL)
		assert.Equal(t, "https://b.example.com/mobile/picking/1.2.3/widgets.rfw", byID["wf-b"].RemoteUI.MobileURL)
		assert.Equal(t, int64(2), mgmtCalls.Load(), "one template lookup per tenant")
	})

	t.Run("is not resolved when not selected", func(t *testing.T) {
		t.Parallel()

		handle := stampedHandle(t)
		temporal := listingTemporal(t, []listedExecution{
			{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
		}, map[string]*fakeWDHandle{"wf": handle})

		var mgmtCalls atomic.Int64
		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			mgmtCalls.Add(1)
			return tenantsWithData(map[string]any{"remoteWebUITemplate": fieldWebTmpl}), nil
		}}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		data := execOK[executionsRemoteUIData](te, te.ctx(userA), executionsWithoutRemoteUIQuery, nil)

		require.Len(t, data.WorkflowExecutions.Edges, 1)
		assert.Nil(t, data.WorkflowExecutions.Edges[0].Node.RemoteUI)
		assert.Zero(t, handle.describeVersionCalls.Load())
		assert.Zero(t, mgmtCalls.Load())
	})

	t.Run("is null without an error when a row carries no tenant", func(t *testing.T) {
		t.Parallel()

		simple := mocks.NewSimpleMockTemporalClient()
		simple.ListWorkflowFunc = func(context.Context, *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			return &workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-orphan", RunId: "run-1"},
				Type:      &commonpb.WorkflowType{Name: "PickingWorkflow"},
			}}}, nil
		}
		temporal := &fakeTemporalClient{SimpleMockTemporalClient: simple, wdc: &fakeWDC{}}

		te := setupRemoteUI(t, templatedMgmt(map[string]any{"remoteWebUITemplate": fieldWebTmpl}), &remoteUIClientFactory{temporal: temporal})

		result := execWithErrors(te, te.ctx(userA))

		require.Len(t, result.Data.WorkflowExecutions.Edges, 1)
		assert.Nil(t, result.Data.WorkflowExecutions.Edges[0].Node.RemoteUI)
		assert.Empty(t, result.Errors)
	})

	// One request owns each shared lookup (management templates, DescribeVersion)
	// and is cancelled mid-flight; a second request that joined it must still
	// render, from the one call.
	for _, blocked := range []string{"management", "describe"} {
		t.Run("a cancelled caller does not fail the shared "+blocked+" lookup of another request", func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			var calls atomic.Int64
			block := func(ctx context.Context) error {
				calls.Add(1)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			templates := map[string]any{
				"remoteWebUITemplate":    fieldWebTmpl,
				"remoteMobileUITemplate": fieldMobileTmpl,
			}
			mgmt := templatedMgmt(templates)
			handle := stampedHandle(t)
			switch blocked {
			case "management":
				mgmt = &fakeMgmtClient{getTenants: func(ctx context.Context, _ managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
					if err := block(ctx); err != nil {
						return nil, err
					}
					return tenantsWithData(templates), nil
				}}
			case "describe":
				handle.describeVersion = block
			}
			temporal := listingTemporal(t, []listedExecution{
				{workflowID: "wf-1", tenantID: tenantA, typeName: "PickingWorkflow", deployment: "wf", buildID: "b1"},
			}, map[string]*fakeWDHandle{"wf": handle})
			te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

			// The owner goes through a real HTTP round trip, so cancelling its
			// context is a client disconnect that cancels the server-side context.
			ownerCtx, cancelOwner := context.WithCancel(te.ctx(userA))
			owner := execAsync(te, ownerCtx)
			require.Eventually(t, func() bool { return calls.Load() == 1 }, waitFor, tick, "owner is inside the lookup")

			waiter := execAsync(te, te.ctx(userA))
			require.Never(t, func() bool { return calls.Load() > 1 }, 100*time.Millisecond, tick, "waiter joins the in-flight call")

			cancelOwner()
			require.ErrorIs(t, (<-owner).err, context.Canceled)

			close(release)
			got := <-waiter
			require.NoError(t, got.err)
			require.Len(t, got.result.Data.WorkflowExecutions.Edges, 1)
			require.NotNil(t, got.result.Data.WorkflowExecutions.Edges[0].Node.RemoteUI, "the shared call outlives the caller that started it")
			assert.Equal(t, "https://cdn.example.com/web/picking/1.2.3/mf-manifest.json", got.result.Data.WorkflowExecutions.Edges[0].Node.RemoteUI.WebURL)
			assert.Empty(t, got.result.Errors)
			assert.Equal(t, int64(1), calls.Load())
		})
	}
}

// Test helpers that main carries in single_tenant_test.go; that file is not
// part of this backport, so the two definitions the remoteUI tests need live
// here on the v0.25.x lineage.
var userAB = &authn.User{
	ID:       uuid.MustParse("5c2c6a2e-6f6d-4a5e-9b0e-3f1c1a4d7b01"),
	TenantID: resolver.TenantA,
	Roles: map[uuid.UUID]authn.Role{
		resolver.TenantA: authn.ROLE_ADMIN,
		resolver.TenantB: authn.ROLE_ADMIN,
	},
}

// ctxWithTenants is te.ctx with an explicit, possibly empty, tenant selection.
func (te *testEnv) ctxWithTenants(user *authn.User, tenantIDs ...uuid.UUID) context.Context {
	te.t.Helper()
	return request.Context(te.t.Context(), user, tenantIDs...)
}
