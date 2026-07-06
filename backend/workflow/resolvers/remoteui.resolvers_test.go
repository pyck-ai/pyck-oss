package resolvers_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	deploymentpb "go.temporal.io/api/deployment/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	temporalworker "go.temporal.io/sdk/worker"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/validator"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	remoteUIQuery = resolver.ParseTemplate(`query {
		remoteUI(input: { workflowID: "{{.WorkflowID}}", workflowExecutionID: "{{.RunID}}" }) {
			web
			mobile
		}
	}`)

	workerDeploymentUIBundlesQuery = resolver.ParseTemplate(`query {
		workerDeploymentUIBundles(first: {{.First}}{{if .After}}, after: "{{.After}}"{{end}}) {
			edges {
				node {
					deploymentName
					buildID
					drainageStatus
					bundle { slug version }
				}
				cursor
			}
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
			totalCount
		}
	}`)
)

type remoteUIData struct {
	RemoteUI struct {
		Web    string
		Mobile string
	}
}

type deploymentVersionUINode struct {
	DeploymentName string
	BuildID        string
	DrainageStatus string
	Bundle         struct {
		Slug    string
		Version string
	}
}

type workerDeploymentUIBundlesData struct {
	WorkerDeploymentUIBundles struct {
		Edges []struct {
			Node   deploymentVersionUINode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
		TotalCount int
	}
}

// =============================================================================
// FAKES
// =============================================================================

// fakeMgmtClient serves only GetTenants; the embedded nil Client panics on any
// other call (the remote-UI resolvers make none).
type fakeMgmtClient struct {
	managementapi.Client
	getTenants func(ctx context.Context, input managementapi.GetTenantsArgs) (*managementapi.GetTenants, error)
}

func (f *fakeMgmtClient) GetTenants(ctx context.Context, input managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
	return f.getTenants(ctx, input)
}

// tenantsWithData returns a single-tenant response carrying the given tenant
// data (where the UI bundle URL templates live).
func tenantsWithData(data map[string]any) *managementapi.GetTenants {
	return &managementapi.GetTenants{
		Tenants: managementapi.GetTenants_Tenants{
			Edges: []*managementapi.GetTenants_Tenants_Edges{
				{Node: &managementapi.GetTenants_Tenants_Edges_Node{
					ID:   "11111111-1111-1111-1111-111111111111",
					Data: data,
				}},
			},
		},
	}
}

// noTenants returns an empty response (tenant not found).
func noTenants() *managementapi.GetTenants {
	return &managementapi.GetTenants{Tenants: managementapi.GetTenants_Tenants{}}
}

// fakeTemporalClient overrides the two calls the remote-UI path makes; the rest
// falls through to SimpleMockTemporalClient.
type fakeTemporalClient struct {
	*mocks.SimpleMockTemporalClient
	workflowType string
	version      *deploymentpb.WorkerDeploymentVersion // pinned version; nil = unversioned execution
	wdc          temporalclient.WorkerDeploymentClient
}

func (f *fakeTemporalClient) DescribeWorkflowExecution(_ context.Context, _, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	info := &workflowpb.WorkflowExecutionInfo{
		Type: &commonpb.WorkflowType{Name: f.workflowType},
	}
	if f.version != nil {
		info.VersioningInfo = &workflowpb.WorkflowExecutionVersioningInfo{DeploymentVersion: f.version}
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info}, nil
}

func (f *fakeTemporalClient) WorkerDeploymentClient() temporalclient.WorkerDeploymentClient {
	return f.wdc
}

// fakeWDC is a fake WorkerDeploymentClient: List yields the configured entries
// and GetHandle returns the configured per-name handle.
type fakeWDC struct {
	listEntries []*temporalclient.WorkerDeploymentListEntry
	handles     map[string]*fakeWDHandle
}

func (f *fakeWDC) List(_ context.Context, _ temporalclient.WorkerDeploymentListOptions) (temporalclient.WorkerDeploymentListIterator, error) {
	return &fakeWDIterator{entries: f.listEntries}, nil
}

func (f *fakeWDC) GetHandle(name string) temporalclient.WorkerDeploymentHandle {
	if h, ok := f.handles[name]; ok {
		return h
	}
	return &fakeWDHandle{}
}

func (f *fakeWDC) Delete(_ context.Context, _ temporalclient.WorkerDeploymentDeleteOptions) (temporalclient.WorkerDeploymentDeleteResponse, error) {
	return temporalclient.WorkerDeploymentDeleteResponse{}, nil
}

type fakeWDIterator struct {
	entries []*temporalclient.WorkerDeploymentListEntry
	i       int
}

func (it *fakeWDIterator) HasNext() bool { return it.i < len(it.entries) }

func (it *fakeWDIterator) Next() (*temporalclient.WorkerDeploymentListEntry, error) {
	e := it.entries[it.i]
	it.i++
	return e, nil
}

// fakeWDHandle is a fake WorkerDeploymentHandle. Only Describe and
// DescribeVersion carry behaviour; the mutating methods are inert stubs.
type fakeWDHandle struct {
	describeResp         temporalclient.WorkerDeploymentDescribeResponse
	versions             map[string]temporalclient.WorkerDeploymentVersionDescription // keyed by BuildID
	describeVersionCalls atomic.Int64                                                 // concurrent in the listing; for cache assertions
}

func (h *fakeWDHandle) Describe(_ context.Context, _ temporalclient.WorkerDeploymentDescribeOptions) (temporalclient.WorkerDeploymentDescribeResponse, error) {
	return h.describeResp, nil
}

func (h *fakeWDHandle) DescribeVersion(_ context.Context, opts temporalclient.WorkerDeploymentDescribeVersionOptions) (temporalclient.WorkerDeploymentVersionDescription, error) {
	h.describeVersionCalls.Add(1)
	return h.versions[opts.BuildID], nil
}

func (h *fakeWDHandle) SetCurrentVersion(_ context.Context, _ temporalclient.WorkerDeploymentSetCurrentVersionOptions) (temporalclient.WorkerDeploymentSetCurrentVersionResponse, error) {
	return temporalclient.WorkerDeploymentSetCurrentVersionResponse{}, nil
}

func (h *fakeWDHandle) SetRampingVersion(_ context.Context, _ temporalclient.WorkerDeploymentSetRampingVersionOptions) (temporalclient.WorkerDeploymentSetRampingVersionResponse, error) {
	return temporalclient.WorkerDeploymentSetRampingVersionResponse{}, nil
}

func (h *fakeWDHandle) SetManagerIdentity(_ context.Context, _ temporalclient.WorkerDeploymentSetManagerIdentityOptions) (temporalclient.WorkerDeploymentSetManagerIdentityResponse, error) {
	return temporalclient.WorkerDeploymentSetManagerIdentityResponse{}, nil
}

func (h *fakeWDHandle) DeleteVersion(_ context.Context, _ temporalclient.WorkerDeploymentDeleteVersionOptions) (temporalclient.WorkerDeploymentDeleteVersionResponse, error) {
	return temporalclient.WorkerDeploymentDeleteVersionResponse{}, nil
}

func (h *fakeWDHandle) UpdateVersionMetadata(_ context.Context, _ temporalclient.WorkerDeploymentUpdateVersionMetadataOptions) (temporalclient.WorkerDeploymentUpdateVersionMetadataResponse, error) {
	return temporalclient.WorkerDeploymentUpdateVersionMetadataResponse{}, nil
}

// remoteUIClientFactory hands the resolver a workflow.Client wrapping our fake
// Temporal client; a non-nil err simulates an unavailable client. The client is
// built once and reused, mirroring production (clients are cached per namespace)
// so the client-level caches persist across queries within a test.
type remoteUIClientFactory struct {
	temporal temporalclient.Client
	err      error
	client   *commonworkflow.Client
}

func (f *remoteUIClientFactory) GetClient(_ context.Context, _ string) (*commonworkflow.Client, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.client == nil {
		c, err := commonworkflow.NewClient("test", f.temporal)
		if err != nil {
			return nil, err
		}
		f.client = c
	}
	return f.client, nil
}

func (f *remoteUIClientFactory) Close() {}

// =============================================================================
// SETUP
// =============================================================================

func setupRemoteUI(t *testing.T, mgmt *fakeMgmtClient, factory *remoteUIClientFactory) *testEnv {
	t.Helper()
	return setupRemoteUIWithDefaults(t, mgmt, factory, resolvers.RemoteUIDefaults{})
}

func setupRemoteUIWithDefaults(t *testing.T, mgmt *fakeMgmtClient, factory *remoteUIClientFactory, defaults resolvers.RemoteUIDefaults) *testEnv {
	t.Helper()

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t),
		t:               t,
	}

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	).Debug()

	workflowRouter := services.NewSignalRouter(client, services.SignalRouterConfig{
		TemporalURL:   "",
		ClientFactory: factory,
	})

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("workflow", client, v, workflowRouter, mgmt, defaults)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "workflow-test", 0))
	})

	return te
}

// uiBundleMeta encodes the given key/value pairs as Temporal metadata payloads.
func uiBundleMeta(t *testing.T, kv map[string]string) map[string]*commonpb.Payload {
	t.Helper()
	out := make(map[string]*commonpb.Payload, len(kv))
	for k, val := range kv {
		p, err := converter.GetDefaultDataConverter().ToPayload(val)
		require.NoError(t, err)
		out[k] = p
	}
	return out
}

// pinnedVersion builds the deployment version an execution is pinned to.
// Deployment "wf" / build "b1" match the fakeWDC handle and version keys used
// across these tests.
func pinnedVersion() *deploymentpb.WorkerDeploymentVersion {
	return &deploymentpb.WorkerDeploymentVersion{DeploymentName: "wf", BuildId: "b1"}
}

// =============================================================================
// remoteUI RESOLVER
// =============================================================================

func TestRemoteUIResolver(t *testing.T) {
	t.Parallel()

	const (
		webTmpl    = "https://cdn.example.com/web/{{.Slug}}/{{.Version}}/mf-manifest.json"
		mobileTmpl = "https://cdn.example.com/mobile/{{.Slug}}/{{.Version}}/widgets.rfw"
	)

	t.Run("renders web and mobile URLs from the version-wide bundle", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{
				"remoteWebUITemplate":    webTmpl,
				"remoteMobileUITemplate": mobileTmpl,
			}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
				"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
					"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
						commonworkflow.UIBundleMetadataKey("", "slug"):    "picking",
						commonworkflow.UIBundleMetadataKey("", "version"): "1.2.3",
					})}},
				}},
			}},
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})

		assert.Equal(t, "https://cdn.example.com/web/picking/1.2.3/mf-manifest.json", data.RemoteUI.Web)
		assert.Equal(t, "https://cdn.example.com/mobile/picking/1.2.3/widgets.rfw", data.RemoteUI.Mobile)
	})

	t.Run("prefers the per-workflow-type bundle over the default", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{
				"remoteWebUITemplate":    webTmpl,
				"remoteMobileUITemplate": mobileTmpl,
			}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
				"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
					"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
						// version-wide default ...
						commonworkflow.UIBundleMetadataKey("", "slug"):    "default",
						commonworkflow.UIBundleMetadataKey("", "version"): "0.0.1",
						// ... overridden for this workflow type
						commonworkflow.UIBundleMetadataKey("PickingWorkflow", "slug"):    "picking-special",
						commonworkflow.UIBundleMetadataKey("PickingWorkflow", "version"): "9.9.9",
					})}},
				}},
			}},
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})

		assert.Equal(t, "https://cdn.example.com/web/picking-special/9.9.9/mf-manifest.json", data.RemoteUI.Web)
		assert.Equal(t, "https://cdn.example.com/mobile/picking-special/9.9.9/widgets.rfw", data.RemoteUI.Mobile)
	})

	t.Run("renders only the web URL when no mobile template is set", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
				"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
					"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
						commonworkflow.UIBundleMetadataKey("", "slug"):    "picking",
						commonworkflow.UIBundleMetadataKey("", "version"): "1.2.3",
					})}},
				}},
			}},
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})

		assert.Equal(t, "https://cdn.example.com/web/picking/1.2.3/mf-manifest.json", data.RemoteUI.Web)
		assert.Empty(t, data.RemoteUI.Mobile)
	})

	t.Run("errors when the execution has no pinned deployment version", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  nil, // unversioned execution
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		execErr(te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		}, "no pinned deployment version")
	})

	t.Run("errors when the tenant has no UI templates set", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{}), nil // tenant exists but unconfigured
		}}

		temporal := &fakeTemporalClient{SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient()}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		execErr(te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		}, "no UI bundle URL templates")
	})

	t.Run("errors when the tenant is not found", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return noTenants(), nil
		}}

		temporal := &fakeTemporalClient{SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient()}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		execErr(te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		}, "tenant not found")
	})

	t.Run("errors when the workflow client is unavailable", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return nil, errors.New("GetTenants must not be reached when the client is unavailable")
		}}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{err: errors.New("temporal down")})

		execErr(te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		}, "invalid workflowClient")
	})

	t.Run("rejects a slug that is unsafe to splice into a URL", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
				"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
					"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
						commonworkflow.UIBundleMetadataKey("", "slug"):    "../../evil",
						commonworkflow.UIBundleMetadataKey("", "version"): "1.2.3",
					})}},
				}},
			}},
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		execErr(te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		}, "invalid UI bundle metadata value")
	})

	t.Run("falls back to the system-wide default template when the tenant has none", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{}), nil // tenant exists, no templates
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
				"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
					"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
						commonworkflow.UIBundleMetadataKey("", "slug"):    "picking",
						commonworkflow.UIBundleMetadataKey("", "version"): "1.2.3",
					})}},
				}},
			}},
		}

		defaults := resolvers.RemoteUIDefaults{Templates: commonworkflow.UIBundleTemplate{Web: webTmpl, Mobile: mobileTmpl}}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})
		assert.Equal(t, "https://cdn.example.com/web/picking/1.2.3/mf-manifest.json", data.RemoteUI.Web)
		assert.Equal(t, "https://cdn.example.com/mobile/picking/1.2.3/widgets.rfw", data.RemoteUI.Mobile)
	})

	t.Run("falls back to the default bundle when the execution is not version-pinned", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  nil, // unversioned execution
		}

		defaults := resolvers.RemoteUIDefaults{Bundle: &commonworkflow.UIBundle{Slug: "fallback", Version: "0.0.0"}}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})
		assert.Equal(t, "https://cdn.example.com/web/fallback/0.0.0/mf-manifest.json", data.RemoteUI.Web)
	})

	t.Run("falls back to the default bundle when the pinned version has no bundle stamped", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
				// Version is pinned but carries no UI bundle metadata yet.
				"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{"b1": {}}},
			}},
		}

		defaults := resolvers.RemoteUIDefaults{Bundle: &commonworkflow.UIBundle{Slug: "fallback", Version: "0.0.0"}}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})
		assert.Equal(t, "https://cdn.example.com/web/fallback/0.0.0/mf-manifest.json", data.RemoteUI.Web)
	})

	t.Run("does not cache a pinned version with no bundle stamped", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}
		// Pinned version with no UI bundle metadata yet (mid-rollout).
		handle := &fakeWDHandle{versions: map[string]temporalclient.WorkerDeploymentVersionDescription{"b1": {}}}
		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc:                      &fakeWDC{handles: map[string]*fakeWDHandle{"wf": handle}},
		}
		defaults := resolvers.RemoteUIDefaults{Bundle: &commonworkflow.UIBundle{Slug: "fallback", Version: "0.0.0"}}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		for range 2 {
			execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
				"WorkflowID": "wf-1", "RunID": "run-1",
			})
		}
		// Unstamped metadata must not be cached, so it is re-described until a
		// bundle is stamped (rather than serving the default forever).
		assert.Equal(t, int64(2), handle.describeVersionCalls.Load())
	})

	t.Run("picks up a workflow type stamped later (incremental per-type stamping)", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}
		// Version hosts TypeA (stamped) but not the queried TypeB and no version-wide
		handle := &fakeWDHandle{versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
			"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
				commonworkflow.UIBundleMetadataKey("TypeA", "slug"):    "type-a",
				commonworkflow.UIBundleMetadataKey("TypeA", "version"): "1.0.0",
			})}},
		}}
		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "TypeB",
			version:                  pinnedVersion(),
			wdc:                      &fakeWDC{handles: map[string]*fakeWDHandle{"wf": handle}},
		}
		defaults := resolvers.RemoteUIDefaults{Bundle: &commonworkflow.UIBundle{Slug: "fallback", Version: "0.0.0"}}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		// TypeB not stamped yet → default bundle.
		first := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{"WorkflowID": "wf-1", "RunID": "run-1"})
		assert.Equal(t, "https://cdn.example.com/web/fallback/0.0.0/mf-manifest.json", first.RemoteUI.Web)

		// CI stamps TypeB; a frozen cache would keep serving the default.
		handle.versions["b1"] = temporalclient.WorkerDeploymentVersionDescription{Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
			commonworkflow.UIBundleMetadataKey("TypeA", "slug"):    "type-a",
			commonworkflow.UIBundleMetadataKey("TypeA", "version"): "1.0.0",
			commonworkflow.UIBundleMetadataKey("TypeB", "slug"):    "type-b",
			commonworkflow.UIBundleMetadataKey("TypeB", "version"): "2.0.0",
		})}}

		second := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{"WorkflowID": "wf-1", "RunID": "run-1"})
		assert.Equal(t, "https://cdn.example.com/web/type-b/2.0.0/mf-manifest.json", second.RemoteUI.Web)
	})

	t.Run("caches tenant templates and version metadata across queries", func(t *testing.T) {
		t.Parallel()

		var getTenantsCalls int
		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			getTenantsCalls++
			return tenantsWithData(map[string]any{"remoteWebUITemplate": webTmpl}), nil
		}}

		handle := &fakeWDHandle{versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
			"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
				commonworkflow.UIBundleMetadataKey("", "slug"):    "picking",
				commonworkflow.UIBundleMetadataKey("", "version"): "1.2.3",
			})}},
		}}
		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			workflowType:             "PickingWorkflow",
			version:                  pinnedVersion(),
			wdc:                      &fakeWDC{handles: map[string]*fakeWDHandle{"wf": handle}},
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		for range 2 {
			execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
				"WorkflowID": "wf-1", "RunID": "run-1",
			})
		}

		assert.Equal(t, 1, getTenantsCalls, "tenant templates should be fetched once and cached")
		assert.Equal(t, int64(1), handle.describeVersionCalls.Load(), "version metadata should be described once and cached")
	})

	t.Run("renders the flavour branch of the default template for a flavour tenant", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			// Flavour tenant, no stored override → default template applies.
			return tenantsWithData(map[string]any{"flavour": "pyck-go"}), nil
		}}
		temporal := stampedTemporal(t, "picking", "1.2.3")

		const branchingWeb = "{{if .Flavour}}https://cdn.example.com/flavours/{{.Flavour}}/{{.Env}}/web/{{.Slug}}/{{.Version}}/mf.json" +
			"{{else}}https://cdn.example.com/{{.TenantID}}/web/{{.Slug}}/{{.Version}}/mf.json{{end}}"
		defaults := resolvers.RemoteUIDefaults{
			Templates: commonworkflow.UIBundleTemplate{Web: branchingWeb},
			Env:       "dev",
		}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		data := execOK[remoteUIData](te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		})
		assert.Equal(t, "https://cdn.example.com/flavours/pyck-go/dev/web/picking/1.2.3/mf.json", data.RemoteUI.Web)
	})

	t.Run("rejects a flavour that is unsafe to splice into a URL", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{getTenants: func(context.Context, managementapi.GetTenantsArgs) (*managementapi.GetTenants, error) {
			return tenantsWithData(map[string]any{"flavour": "../evil"}), nil
		}}
		temporal := stampedTemporal(t, "picking", "1.2.3")

		defaults := resolvers.RemoteUIDefaults{
			Templates: commonworkflow.UIBundleTemplate{Web: "https://cdn.example.com/flavours/{{.Flavour}}/web/{{.Slug}}/{{.Version}}/mf.json"},
			Env:       "dev",
		}
		te := setupRemoteUIWithDefaults(t, mgmt, &remoteUIClientFactory{temporal: temporal}, defaults)

		execErr(te, te.ctx(userA), remoteUIQuery, map[string]any{
			"WorkflowID": "wf-1", "RunID": "run-1",
		}, "invalid UI bundle metadata value")
	})
}

// stampedTemporal builds a fake Temporal client for a pinned execution whose
// version is stamped with the given version-wide bundle.
func stampedTemporal(t *testing.T, slug, version string) *fakeTemporalClient {
	t.Helper()
	return &fakeTemporalClient{
		SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
		workflowType:             "PickingWorkflow",
		version:                  pinnedVersion(),
		wdc: &fakeWDC{handles: map[string]*fakeWDHandle{
			"wf": {versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
				"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
					commonworkflow.UIBundleMetadataKey("", "slug"):    slug,
					commonworkflow.UIBundleMetadataKey("", "version"): version,
				})}},
			}},
		}},
	}
}

// =============================================================================
// workerDeploymentUIBundles RESOLVER
// =============================================================================

func TestWorkerDeploymentUIBundlesResolver(t *testing.T) {
	t.Parallel()

	t.Run("lists versions with drainage status and stamped bundles", func(t *testing.T) {
		t.Parallel()

		mgmt := &fakeMgmtClient{} // not used by this resolver

		handle := &fakeWDHandle{
			describeResp: temporalclient.WorkerDeploymentDescribeResponse{
				Info: temporalclient.WorkerDeploymentInfo{
					Name: "wf",
					VersionSummaries: []temporalclient.WorkerDeploymentVersionSummary{
						{
							Version:        temporalworker.WorkerDeploymentVersion{DeploymentName: "wf", BuildID: "b1"},
							DrainageStatus: temporalclient.WorkerDeploymentVersionDrainageStatusDraining,
						},
						{
							Version:        temporalworker.WorkerDeploymentVersion{DeploymentName: "wf", BuildID: "b2"},
							DrainageStatus: temporalclient.WorkerDeploymentVersionDrainageStatusDrained,
						},
					},
				},
			},
			versions: map[string]temporalclient.WorkerDeploymentVersionDescription{
				// b1 carries a complete version-wide bundle ...
				"b1": {Info: temporalclient.WorkerDeploymentVersionInfo{Metadata: uiBundleMeta(t, map[string]string{
					commonworkflow.UIBundleMetadataKey("", "slug"):    "picking",
					commonworkflow.UIBundleMetadataKey("", "version"): "1.0.0",
				})}},
				// ... b2 carries none (best-effort empty bundle).
				"b2": {},
			},
		}

		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			wdc: &fakeWDC{
				listEntries: []*temporalclient.WorkerDeploymentListEntry{{Name: "wf"}},
				handles:     map[string]*fakeWDHandle{"wf": handle},
			},
		}

		te := setupRemoteUI(t, mgmt, &remoteUIClientFactory{temporal: temporal})

		data := execOK[workerDeploymentUIBundlesData](te, te.ctx(userA), workerDeploymentUIBundlesQuery, map[string]any{"First": 50})

		conn := data.WorkerDeploymentUIBundles
		require.Len(t, conn.Edges, 2)
		assert.Equal(t, 2, conn.TotalCount)
		assert.False(t, conn.PageInfo.HasNextPage)

		b1 := conn.Edges[0].Node
		assert.Equal(t, "wf", b1.DeploymentName)
		assert.Equal(t, "b1", b1.BuildID)
		assert.Equal(t, "draining", b1.DrainageStatus)
		assert.Equal(t, "picking", b1.Bundle.Slug)
		assert.Equal(t, "1.0.0", b1.Bundle.Version)

		b2 := conn.Edges[1].Node
		assert.Equal(t, "b2", b2.BuildID)
		assert.Equal(t, "drained", b2.DrainageStatus)
		assert.Empty(t, b2.Bundle.Slug)
		assert.Empty(t, b2.Bundle.Version)
	})

	t.Run("paginates with first/after", func(t *testing.T) {
		t.Parallel()

		handle := &fakeWDHandle{
			describeResp: temporalclient.WorkerDeploymentDescribeResponse{
				Info: temporalclient.WorkerDeploymentInfo{Name: "wf", VersionSummaries: []temporalclient.WorkerDeploymentVersionSummary{
					{Version: temporalworker.WorkerDeploymentVersion{DeploymentName: "wf", BuildID: "b1"}, DrainageStatus: temporalclient.WorkerDeploymentVersionDrainageStatusDrained},
					{Version: temporalworker.WorkerDeploymentVersion{DeploymentName: "wf", BuildID: "b2"}, DrainageStatus: temporalclient.WorkerDeploymentVersionDrainageStatusDraining},
				}},
			},
			versions: map[string]temporalclient.WorkerDeploymentVersionDescription{"b1": {}, "b2": {}},
		}
		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			wdc:                      &fakeWDC{listEntries: []*temporalclient.WorkerDeploymentListEntry{{Name: "wf"}}, handles: map[string]*fakeWDHandle{"wf": handle}},
		}
		te := setupRemoteUI(t, &fakeMgmtClient{}, &remoteUIClientFactory{temporal: temporal})

		page1 := execOK[workerDeploymentUIBundlesData](te, te.ctx(userA), workerDeploymentUIBundlesQuery, map[string]any{"First": 1}).WorkerDeploymentUIBundles
		require.Len(t, page1.Edges, 1)
		assert.Equal(t, 2, page1.TotalCount)
		assert.True(t, page1.PageInfo.HasNextPage)
		assert.False(t, page1.PageInfo.HasPreviousPage)
		require.NotNil(t, page1.PageInfo.EndCursor)

		page2 := execOK[workerDeploymentUIBundlesData](te, te.ctx(userA), workerDeploymentUIBundlesQuery, map[string]any{"First": 1, "After": *page1.PageInfo.EndCursor}).WorkerDeploymentUIBundles
		require.Len(t, page2.Edges, 1)
		assert.False(t, page2.PageInfo.HasNextPage)
		assert.True(t, page2.PageInfo.HasPreviousPage)
		assert.NotEqual(t, page1.Edges[0].Node.BuildID, page2.Edges[0].Node.BuildID)
	})

	t.Run("an unknown after cursor yields an empty page", func(t *testing.T) {
		t.Parallel()

		handle := &fakeWDHandle{
			describeResp: temporalclient.WorkerDeploymentDescribeResponse{
				Info: temporalclient.WorkerDeploymentInfo{Name: "wf", VersionSummaries: []temporalclient.WorkerDeploymentVersionSummary{
					{Version: temporalworker.WorkerDeploymentVersion{DeploymentName: "wf", BuildID: "b1"}},
				}},
			},
			versions: map[string]temporalclient.WorkerDeploymentVersionDescription{"b1": {}},
		}
		temporal := &fakeTemporalClient{
			SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient(),
			wdc:                      &fakeWDC{listEntries: []*temporalclient.WorkerDeploymentListEntry{{Name: "wf"}}, handles: map[string]*fakeWDHandle{"wf": handle}},
		}
		te := setupRemoteUI(t, &fakeMgmtClient{}, &remoteUIClientFactory{temporal: temporal})

		conn := execOK[workerDeploymentUIBundlesData](te, te.ctx(userA), workerDeploymentUIBundlesQuery, map[string]any{"First": 10, "After": "wf/does-not-exist"}).WorkerDeploymentUIBundles
		assert.Empty(t, conn.Edges, "unknown cursor must not restart from page 1")
		assert.False(t, conn.PageInfo.HasNextPage)
		assert.Equal(t, 1, conn.TotalCount)
	})

	t.Run("errors when the workflow client is unavailable", func(t *testing.T) {
		t.Parallel()

		te := setupRemoteUI(t, &fakeMgmtClient{}, &remoteUIClientFactory{err: errors.New("temporal down")})

		execErr(te, te.ctx(userA), workerDeploymentUIBundlesQuery, map[string]any{"First": 50}, "invalid workflowClient")
	})

	t.Run("rejects a non-admin caller", func(t *testing.T) {
		t.Parallel()

		// A reader can reach the resolver (passes tenant access) but the operator
		// query's admin gate must reject it before any Temporal call.
		reader := &authn.User{
			ID:       uuid.MustParse("7a5b1c2d-0000-4000-8000-000000000001"),
			TenantID: tenantA,
			Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER},
		}
		te := setupRemoteUI(t, &fakeMgmtClient{}, &remoteUIClientFactory{})

		execErr(te, te.ctx(reader), workerDeploymentUIBundlesQuery, map[string]any{"First": 50}, "admin role required")
	})
}
