package resolvers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// mockEncodedValue implements converter.EncodedValue for testing QueryWorkflow responses.
type mockEncodedValue struct {
	data any
}

func (m *mockEncodedValue) HasValue() bool { return m.data != nil }

func (m *mockEncodedValue) Get(valuePtr interface{}) error {
	b, err := json.Marshal(m.data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, valuePtr)
}

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	registerWorkflow = resolver.ParseTemplate(`mutation {
		registerWorkflow(input: {
			name: "{{.Name}}",
			taskQueue: "{{.TaskQueue}}",
			{{- if .DataTypeID }}
			dataTypeID: "{{.DataTypeID}}",
			{{- end }}
			{{- if .DataTypeSlug }}
			dataTypeSlug: "{{.DataTypeSlug}}",
			{{- end }}
			data: {
				type: "custom",
				sum: 15,
				meta: {
					name: "{{.DataName}}",
					weight: {{.DataWeight}},
					tags: ["test", "foobar"]
				}
			}
			{{- if .Signals }}
			,
			signals: [
				{{- range $i, $s := .Signals }}
				{{- if $i}},{{end}}
				{
					natsTopic: "{{$s.NATSTopic}}",
					temporalSignal: "{{$s.TemporalSignal}}",
					temporalSignalType: {{$s.TemporalSignalType}},
					filterRule: "{{$s.FilterRule}}"
				}
				{{- end }}
			]
			{{- end }}
		}) {
			id
			tenantID
			name
			dataTypeID
			data
			createdAt
			createdBy
			updatedAt
			updatedBy
		}
	}`)

	deleteWorkflow = resolver.ParseTemplate(`mutation {
		deleteWorkflow(id: "{{.ID}}") {
			deletedID
		}
	}`)

	cancelWorkflow = resolver.ParseTemplate(`mutation {
		cancelWorkflow(input: { workflowID: "{{.WorkflowID}}", workflowRunID: "{{.WorkflowRunID}}" }) {
			workflowID
			workflowRunID
		}
	}`)

	queryWorkflows = resolver.ParseTemplate(`query {
		workflows {
			totalCount
			edges {
				node {
					id
					tenantID
					name
					dataTypeID
					data
				}
				cursor
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	queryWorkflowsWithFilter = resolver.ParseTemplate(`query {
		workflows(first: 20,
			after: null,
			orderBy: { direction: ASC, field: CREATED_AT },
			where: {{or .Where "null"}}
		) {
			totalCount
			edges {
				node {
					id
					tenantID
					name
					dataTypeID
					data
					createdAt
				}
			}
			pageInfo {
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	workflowAssignee = resolver.ParseTemplate(`query {
		workflowAssignee(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}) {
			assignee
		}
	}`)

	setAssignee = resolver.ParseTemplate(`mutation {
		setWorkflowAssignee(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}",
			assigneeID: "{{.AssigneeID}}"
		}) {
			assignee
		}
	}`)

	unsetAssignee = resolver.ParseTemplate(`mutation {
		setWorkflowAssignee(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}) {
			assignee
		}
	}`)

	getWorkflowActions = resolver.ParseTemplate(`query {
		workflowActions(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}) {
			queries { name enabled }
			updates { name enabled }
		}
	}`)

	getWorkflowActionsFiltered = resolver.ParseTemplate(`query {
		workflowActions(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}, where: { enabled: true }) {
			queries { name enabled }
			updates { name enabled }
		}
	}`)

	getWorkflowActionsWhere = resolver.ParseTemplate(`query {
		workflowActions(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}, where: { {{.Where}} }) {
			queries { name enabled }
			updates { name enabled }
		}
	}`)

	queryWorkflowsJSONOrder = resolver.ParseTemplate(`query {
		workflows(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .JSONType}}, jsonType: {{.JSONType}}{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
		) {
			totalCount
			edges { node { id tenantID name dataTypeID data } }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type workflowNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Name       string
	DataTypeID uuid.UUID
	Data       map[string]any
	CreatedAt  string
}

type registerWorkflowData struct {
	RegisterWorkflow workflowNode
}

type deleteWorkflowData struct {
	DeleteWorkflow struct{ DeletedID uuid.UUID }
}

type cancelWorkflowData struct {
	CancelWorkflow struct {
		WorkflowID    string
		WorkflowRunID string
	}
}

type queryWorkflowsData struct {
	Workflows struct {
		TotalCount int
		Edges      []struct{ Node workflowNode }
		PageInfo   struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

type setAssigneeData struct {
	SetWorkflowAssignee struct{ Assignee *string }
}

type workflowActionsData struct {
	WorkflowActions struct {
		Queries []struct {
			Name    string
			Enabled bool
		}
		Updates []struct {
			Name    string
			Enabled bool
		}
	}
}

// =============================================================================
// INPUT TYPES
// =============================================================================

type SignalInput struct {
	NATSTopic          string
	TemporalSignal     string
	TemporalSignalType string
	FilterRule         string
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func natsSignalTopic(t *testing.T, tenantID *uuid.UUID, op string) string {
	t.Helper()
	tid := "*"
	if tenantID != nil {
		tid = tenantID.String()
	}
	return fmt.Sprintf("request.reply.pyck.%s.crud.workflow.workflowsignal.*.%s", tid, op)
}

func natsSignalTopicAttrOp(t *testing.T, tenantID *uuid.UUID, op string) string {
	t.Helper()
	tid := "*"
	if tenantID != nil {
		tid = tenantID.String()
	}
	// Use a valid UUID for the entity field
	entityID := "123e4567-e89b-12d3-a456-426614174000"
	return fmt.Sprintf("request.reply.pyck.%s.crud.workflow.workflowsignal.%s.%s", tid, entityID, op)
}

// =============================================================================
// REGISTER WORKFLOW TESTS
// =============================================================================

func TestWorkflowRegister(t *testing.T) {
	t.Parallel()

	t.Run("creates workflow without signals", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "testWorkflow",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
		})

		assert.Equal(t, userA.TenantID, data.RegisterWorkflow.TenantID)
		assert.Equal(t, "testWorkflow", data.RegisterWorkflow.Name)
		assert.Equal(t, itemDataTypeID, data.RegisterWorkflow.DataTypeID)
		assert.NotEqual(t, uuid.Nil, data.RegisterWorkflow.ID)

		// Verify GraphQL API returns UTC timestamps (suffix "Z", not local offset)
		assert.True(t, strings.HasSuffix(data.RegisterWorkflow.CreatedAt, "Z"),
			"GraphQL createdAt should be in UTC (got %s)", data.RegisterWorkflow.CreatedAt)

		te.assertEvents(ctx, Create("workflow", data.RegisterWorkflow.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_register_missing_dt",
			"TaskQueue":  "test-queue",
			"DataName":   "testWorkflow",
			"DataWeight": 0,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid data against schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_register_invalid_payload",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow2",
			"DataWeight": -50,
		}, "jsonschema:")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// SIGNAL MANAGEMENT TESTS
// =============================================================================

func TestWorkflowRegister_Signals(t *testing.T) {
	t.Parallel()

	t.Run("creates updates and deletes leftover signals", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Call 1: create with 2 signals (orders.created / orders.updated)
		n1 := natsSignalTopicAttrOp(t, &tenantA, "created")
		n2 := natsSignalTopicAttrOp(t, &tenantA, "updated")

		data1 := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_register_signals_diff",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: n1, TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
				{NATSTopic: n2, TemporalSignal: "OrderUpdated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		wfID := data1.RegisterWorkflow.ID
		assert.NotEqual(t, uuid.Nil, wfID)

		te.clearEvents(ctx)

		// Call 2: change A.temporalSignal and drop B (leftover -> delete)
		n1v2 := natsSignalTopicAttrOp(t, &tenantA, "created")
		data2 := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_register_signals_diff", // same name -> update
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: n1v2, TemporalSignal: "OrderCreatedV2", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		assert.Equal(t, wfID, data2.RegisterWorkflow.ID)

		// 1 workflow update + 1 signal create (new key) + 2 signal deletes (old key + leftover)
		te.assertEventCounts(ctx, map[string]int{
			"workflow":       1,
			"workflowsignal": 3,
		})
	})

	t.Run("deletes all signals when omitted", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create with 2 signals
		n1 := natsSignalTopicAttrOp(t, &tenantA, "a")
		n2 := natsSignalTopicAttrOp(t, &tenantA, "b")

		execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_register_signals_delete_all",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: n1, TemporalSignal: "S1", TemporalSignalType: "intermediate", FilterRule: "true"},
				{NATSTopic: n2, TemporalSignal: "S2", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		te.clearEvents(ctx)

		// Register again without signals -> leftovers must be deleted
		execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_register_signals_delete_all",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
		})

		// 1 workflow update + 2 signal deletes (leftovers)
		te.assertEventCounts(ctx, map[string]int{
			"workflow":       1,
			"workflowsignal": 2,
		})
	})

	t.Run("tenant match success with CRUD pattern", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		n1 := natsSignalTopicAttrOp(t, &tenantA, "a")

		data := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_tenant_match_crud",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_tenant_match_crud",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: n1, TemporalSignal: "OrderTotalUpdated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		assert.NotEqual(t, uuid.Nil, data.RegisterWorkflow.ID)

		te.assertEventCounts(ctx, map[string]int{
			"workflow":       1,
			"workflowsignal": 1,
		})
	})

	t.Run("tenant mismatch fails with CRUD pattern", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		n1 := natsSignalTopicAttrOp(t, &tenantB, "a")

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_tenant_mismatch_crud",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_tenant_mismatch_crud",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: n1, TemporalSignal: "OrderTotalUpdated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "invalid nats topic")

		te.assertNoEvents(ctx)
	})

	t.Run("invalid pattern fails", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Obvious invalid topic string
		n1 := "totally.invalid.topic"

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_bad_pattern",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_bad_pattern",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: n1, TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "unknown topic type")

		te.assertNoEvents(ctx)
	})

	t.Run("wildcard single segment allowed", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		topic := natsSignalTopic(t, &tenantA, "update")

		data := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_wildcard_single",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_wildcard_single",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: topic, TemporalSignal: "OrderTotalUpdated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		assert.NotEqual(t, uuid.Nil, data.RegisterWorkflow.ID)

		te.assertEventCounts(ctx, map[string]int{
			"workflow":       1,
			"workflowsignal": 1,
		})
	})

	t.Run("wildcard multi segment allowed", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		topic := natsSignalTopic(t, &tenantA, "*")

		data := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_wildcard_asterisk_tail",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_wildcard_asterisk_tail",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: topic, TemporalSignal: "RRSignal", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		assert.NotEqual(t, uuid.Nil, data.RegisterWorkflow.ID)

		te.assertEventCounts(ctx, map[string]int{
			"workflow":       1,
			"workflowsignal": 1,
		})
	})

	t.Run("wildcard no tenant access fails", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Use a user with no roles/tenant access but set a valid MutationTenantID
		ctx := request.Context(t.Context(), userNoRole, tenantA)

		topic := natsSignalTopic(t, nil, "*") // tenant = "*"

		closeResp, resp, err := te.SendQuery(t, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_rr_wildcard_no_access",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_rr_wildcard_no_access",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: topic, TemporalSignal: "AnySignal", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})
		defer closeResp()
		require.NoError(t, err)

		defer resp.Body.Close()
		raw, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr, "failed to read response body")

		text := strings.ToLower(string(raw))
		require.Contains(t, text, "no access to tenant id")
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

// =============================================================================
// DELETE WORKFLOW TESTS
// =============================================================================

func TestWorkflowDelete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes workflow", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create one to delete
		createData := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "test_workflow_to_delete",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "test_workflow",
			"DataWeight": 0,
		})

		te.clearEvents(ctx)

		deleteData := execOK[deleteWorkflowData](te, ctx, deleteWorkflow, map[string]any{
			"ID": createData.RegisterWorkflow.ID,
		})

		assert.Equal(t, createData.RegisterWorkflow.ID, deleteData.DeleteWorkflow.DeletedID)

		// Verify deleted_at is set and in UTC
		ctxWithDeleted := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		deleted, err := te.Ent.Workflow.Get(ctxWithDeleted, createData.RegisterWorkflow.ID)
		require.NoError(t, err)
		assert.False(t, deleted.DeletedAt.IsZero(), "deleted_at should be set")
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("workflow", createData.RegisterWorkflow.ID))
	})
}

func TestCancelWorkflow(t *testing.T) {
	t.Parallel()

	t.Run("invalid workflowID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, cancelWorkflow, map[string]any{
			"WorkflowID":    "",
			"WorkflowRunID": "test-run-id",
		}, "invalid WorkflowID")
	})

	t.Run("invalid workflowRunID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, cancelWorkflow, map[string]any{
			"WorkflowID":    "test-workflow-id",
			"WorkflowRunID": "",
		}, "invalid WorkflowRunID")
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		const (
			workflowID    = "test-workflow"
			workflowRunID = "test-run"
		)

		var (
			called        bool
			capturedID    string
			capturedRunID string
		)
		te.MockTemporalClient.CancelWorkflowFunc = func(_ context.Context, id, runID string) error {
			called = true
			capturedID = id
			capturedRunID = runID
			return nil
		}

		data := execOK[cancelWorkflowData](te, ctx, cancelWorkflow, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
		})

		assert.True(t, called, "CancelWorkflow should be invoked on the Temporal client")
		assert.Equal(t, workflowID, capturedID)
		assert.Equal(t, workflowRunID, capturedRunID)
		assert.Equal(t, workflowID, data.CancelWorkflow.WorkflowID)
		assert.Equal(t, workflowRunID, data.CancelWorkflow.WorkflowRunID)
	})

	t.Run("temporal error propagates", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.MockTemporalClient.CancelWorkflowFunc = func(_ context.Context, _, _ string) error {
			return fmt.Errorf("cancel failed")
		}

		execErr(te, ctx, cancelWorkflow, map[string]any{
			"WorkflowID":    "test-workflow",
			"WorkflowRunID": "test-run",
		}, "cancel failed")
	})
}

// =============================================================================
// QUERY WORKFLOW TESTS
// =============================================================================

func TestWorkflowQuery(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryWorkflowsData](te, ctx, queryWorkflows, nil)

		assert.Zero(t, data.Workflows.TotalCount)
		assert.Empty(t, data.Workflows.Edges)
		assert.False(t, data.Workflows.PageInfo.HasNextPage)
		assert.False(t, data.Workflows.PageInfo.HasPreviousPage)
		assert.Nil(t, data.Workflows.PageInfo.StartCursor)
		assert.Nil(t, data.Workflows.PageInfo.EndCursor)
	})

	t.Run("returns workflows after creation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create one
		createData := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "testWorkflow",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "test_workflow",
			"DataWeight": 0,
		})

		// Query
		queryData := execOK[queryWorkflowsData](te, ctx, queryWorkflows, nil)

		require.Equal(t, 1, queryData.Workflows.TotalCount)
		got := queryData.Workflows.Edges[0].Node
		assert.Equal(t, createData.RegisterWorkflow.ID, got.ID)
		assert.Equal(t, userA.TenantID, got.TenantID)
		assert.Equal(t, "testWorkflow", got.Name)
		assert.Equal(t, createData.RegisterWorkflow.DataTypeID, got.DataTypeID)
	})

	t.Run("query with filters smoke test", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_with_data",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "test_workflow",
			"DataWeight": 0,
		})

		queryData := execOK[queryWorkflowsData](te, ctx, queryWorkflowsWithFilter, nil)

		assert.Equal(t, 1, queryData.Workflows.TotalCount)
	})
}

// =============================================================================
// WORKFLOW ASSIGNEE TESTS
// =============================================================================

func TestWorkflowAssignee(t *testing.T) {
	t.Parallel()

	t.Run("invalid workflowID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, workflowAssignee, map[string]any{
			"WorkflowID":    "",
			"WorkflowRunID": "test-run-id",
		}, "invalid WorkflowID")
	})

	t.Run("invalid workflowRunID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, workflowAssignee, map[string]any{
			"WorkflowID":    "test-workflow-id",
			"WorkflowRunID": "",
		}, "invalid WorkflowRunID")
	})
}

func TestSetAssignee(t *testing.T) {
	t.Parallel()

	t.Run("invalid workflowID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, setAssignee, map[string]any{
			"WorkflowID":    "",
			"WorkflowRunID": "test-run-id",
			"AssigneeID":    uuid.New(),
		}, "invalid WorkflowID")
	})

	t.Run("invalid workflowRunID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, setAssignee, map[string]any{
			"WorkflowID":    "test-workflow-id",
			"WorkflowRunID": "",
			"AssigneeID":    uuid.New(),
		}, "invalid WorkflowRunID")
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		workflowID := "test-workflow"
		workflowRunID := "test-run"
		assigneeID := uuid.New()

		data := execOK[setAssigneeData](te, ctx, setAssignee, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
			"AssigneeID":    assigneeID,
		})

		require.NotNil(t, data.SetWorkflowAssignee.Assignee,
			"Response assignee should not be nil")
		assert.Equal(t, assigneeID.String(), *data.SetWorkflowAssignee.Assignee,
			"Response should contain the assignee ID that was set")
	})

	t.Run("update existing", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		workflowID := "test-workflow-update"
		workflowRunID := "test-run-update"

		// Set first assignee
		firstAssigneeID := uuid.New()
		data1 := execOK[setAssigneeData](te, ctx, setAssignee, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
			"AssigneeID":    firstAssigneeID,
		})

		require.NotNil(t, data1.SetWorkflowAssignee.Assignee)
		assert.Equal(t, firstAssigneeID.String(), *data1.SetWorkflowAssignee.Assignee)

		// Update to second assignee
		secondAssigneeID := uuid.New()
		data2 := execOK[setAssigneeData](te, ctx, setAssignee, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
			"AssigneeID":    secondAssigneeID,
		})

		require.NotNil(t, data2.SetWorkflowAssignee.Assignee)
		assert.Equal(t, secondAssigneeID.String(), *data2.SetWorkflowAssignee.Assignee,
			"Second update should return the new assignee ID")
	})

	t.Run("unassign with nil assigneeID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		workflowID := "test-workflow-unassign"
		workflowRunID := "test-run-unassign"

		data := execOK[setAssigneeData](te, ctx, unsetAssignee, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
		})

		assert.Nil(t, data.SetWorkflowAssignee.Assignee,
			"Response assignee should be nil when unassigning")
	})

	t.Run("concurrency", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		workflowID := "test-concurrent-workflow"
		workflowRunID := "test-concurrent-run"

		// Test with 10 concurrent requests
		numConcurrent := 10
		type result struct {
			assigneeID string
			response   resolver.GQLResult[setAssigneeData]
			err        error
		}
		results := make(chan result, numConcurrent)
		expectedIDs := make([]uuid.UUID, numConcurrent)

		// Launch concurrent SetAssignee requests
		for i := range numConcurrent {
			expectedIDs[i] = uuid.New()
			go func(assigneeID uuid.UUID) {
				closeResp, resp, err := te.SendQuery(t, ctx, setAssignee, map[string]any{
					"WorkflowID":    workflowID,
					"WorkflowRunID": workflowRunID,
					"AssigneeID":    assigneeID,
				})
				defer closeResp()

				res := result{assigneeID: assigneeID.String(), err: err}
				if err == nil {
					err := te.ReadResponse(t, resp, &res.response)
					if err != nil {
						return
					}
				}
				results <- res
			}(expectedIDs[i])
		}

		// Verify each response matches its request
		receivedIDs := make(map[string]bool)
		for range numConcurrent {
			res := <-results
			require.NoError(t, res.err, "SetAssignee should not return HTTP error")
			require.Empty(t, res.response.Errors, "SetAssignee should succeed: %v", res.response.Errors)
			require.NotNil(t, res.response.Data.SetWorkflowAssignee.Assignee,
				"Response assignee should not be nil")
			assert.Equal(t, res.assigneeID, *res.response.Data.SetWorkflowAssignee.Assignee,
				"Response assignee must match the request's assigneeID (no race condition)")

			receivedIDs[res.assigneeID] = true
		}

		// Verify all requests got their own unique response (no mixing)
		assert.Len(t, receivedIDs, numConcurrent,
			"Each request should get its own assignee back (no cross-contamination)")
	})
}

// =============================================================================
// WORKFLOW ACTIONS TESTS
// =============================================================================

func TestWorkflowActions(t *testing.T) {
	t.Parallel()

	t.Run("invalid workflowID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, getWorkflowActions, map[string]any{
			"WorkflowID":    "",
			"WorkflowRunID": "test-run-id",
		}, "invalid WorkflowID")
	})

	t.Run("invalid workflowRunID", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, getWorkflowActions, map[string]any{
			"WorkflowID":    "test-workflow-id",
			"WorkflowRunID": "",
		}, "invalid WorkflowRunID")
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		workflowID := "test-actions-workflow"
		workflowRunID := "test-actions-run"

		// Mock QueryWorkflow to return AvailableActions
		te.MockTemporalClient.QueryWorkflowFunc = func(ctx context.Context, wfID, runID, queryType string, args ...interface{}) (converter.EncodedValue, error) {
			return &mockEncodedValue{data: map[string]any{
				"queries": []any{
					map[string]any{"name": "GetState", "enabled": true},
					map[string]any{"name": "SetAssignee", "enabled": false},
				},
				"updates": []any{
					map[string]any{"name": "AwaitUserDataInput", "enabled": true},
				},
			}}, nil
		}

		data := execOK[workflowActionsData](te, ctx, getWorkflowActions, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
		})

		assert.Len(t, data.WorkflowActions.Queries, 2)
		assert.Equal(t, "GetState", data.WorkflowActions.Queries[0].Name)
		assert.True(t, data.WorkflowActions.Queries[0].Enabled)
		assert.Equal(t, "SetAssignee", data.WorkflowActions.Queries[1].Name)
		assert.False(t, data.WorkflowActions.Queries[1].Enabled)

		assert.Len(t, data.WorkflowActions.Updates, 1)
		assert.Equal(t, "AwaitUserDataInput", data.WorkflowActions.Updates[0].Name)
		assert.True(t, data.WorkflowActions.Updates[0].Enabled)
	})

	t.Run("where filter enabled only", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		workflowID := "test-actions-filter"
		workflowRunID := "test-actions-filter-run"

		te.MockTemporalClient.QueryWorkflowFunc = func(ctx context.Context, wfID, runID, queryType string, args ...interface{}) (converter.EncodedValue, error) {
			return &mockEncodedValue{data: map[string]any{
				"queries": []any{
					map[string]any{"name": "GetState", "enabled": true},
					map[string]any{"name": "SetAssignee", "enabled": false},
				},
				"updates": []any{
					map[string]any{"name": "AwaitUserDataInput", "enabled": true},
				},
			}}, nil
		}

		data := execOK[workflowActionsData](te, ctx, getWorkflowActionsFiltered, map[string]any{
			"WorkflowID":    workflowID,
			"WorkflowRunID": workflowRunID,
		})

		// Only enabled actions should be returned
		assert.Len(t, data.WorkflowActions.Queries, 1)
		assert.Equal(t, "GetState", data.WorkflowActions.Queries[0].Name)
		assert.True(t, data.WorkflowActions.Queries[0].Enabled)

		assert.Len(t, data.WorkflowActions.Updates, 1)
	})

	// Exercises every name predicate through the GraphQL layer, so schema,
	// gqlgen unmarshaling, and resolver filtering are covered together.
	// Predicate semantics themselves are covered in detail by
	// TestMatchesFilter.
	t.Run("where filter name predicates", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name        string
			where       string
			wantQueries []string
			wantUpdates []string
		}{
			{
				name:        "nameHasPrefix",
				where:       `nameHasPrefix: "Get"`,
				wantQueries: []string{"GetState", "GetDeliveryState"},
				wantUpdates: []string{},
			},
			{
				name:        "nameHasSuffix",
				where:       `nameHasSuffix: "State"`,
				wantQueries: []string{"GetState", "GetDeliveryState"},
				wantUpdates: []string{},
			},
			{
				name:        "nameContains",
				where:       `nameContains: "User"`,
				wantQueries: []string{},
				wantUpdates: []string{"AwaitUserDataInput"},
			},
			{
				name:        "nameNEQ",
				where:       `nameNEQ: "GetState"`,
				wantQueries: []string{"GetDeliveryState", "SetAssignee"},
				wantUpdates: []string{"AwaitUserDataInput", "AwaitShipmentInput"},
			},
			{
				name:        "nameIn",
				where:       `nameIn: ["GetState", "AwaitUserDataInput"]`,
				wantQueries: []string{"GetState"},
				wantUpdates: []string{"AwaitUserDataInput"},
			},
			{
				name:        "nameNotIn",
				where:       `nameNotIn: ["GetState", "SetAssignee"]`,
				wantQueries: []string{"GetDeliveryState"},
				wantUpdates: []string{"AwaitUserDataInput", "AwaitShipmentInput"},
			},
			{
				name:        "nameEqualFold",
				where:       `nameEqualFold: "getstate"`,
				wantQueries: []string{"GetState"},
				wantUpdates: []string{},
			},
			{
				name:        "nameContainsFold",
				where:       `nameContainsFold: "ASSIGNEE"`,
				wantQueries: []string{"SetAssignee"},
				wantUpdates: []string{},
			},
			{
				name:        "name predicate combined with enabled",
				where:       `nameHasPrefix: "Await", enabled: true`,
				wantQueries: []string{},
				wantUpdates: []string{"AwaitUserDataInput"},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)
				ctx := te.ctx(userA)

				te.MockTemporalClient.QueryWorkflowFunc = func(ctx context.Context, wfID, runID, queryType string, args ...interface{}) (converter.EncodedValue, error) {
					return &mockEncodedValue{data: map[string]any{
						"queries": []any{
							map[string]any{"name": "GetState", "enabled": true},
							map[string]any{"name": "GetDeliveryState", "enabled": true},
							map[string]any{"name": "SetAssignee", "enabled": false},
						},
						"updates": []any{
							map[string]any{"name": "AwaitUserDataInput", "enabled": true},
							map[string]any{"name": "AwaitShipmentInput", "enabled": false},
						},
					}}, nil
				}

				data := execOK[workflowActionsData](te, ctx, getWorkflowActionsWhere, map[string]any{
					"WorkflowID":    "test-actions-where",
					"WorkflowRunID": "test-actions-where-run",
					"Where":         tt.where,
				})

				names := func(actions []struct {
					Name    string
					Enabled bool
				},
				) []string {
					out := make([]string, 0, len(actions))
					for _, a := range actions {
						out = append(out, a.Name)
					}
					return out
				}

				assert.Equal(t, tt.wantQueries, names(data.WorkflowActions.Queries))
				assert.Equal(t, tt.wantUpdates, names(data.WorkflowActions.Updates))
			})
		}
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestWorkflow_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		target := te.newWorkflow(ctx, userA).Data(map[string]any{
			"type": "custom",
			"meta": map[string]any{
				"name": "TestItem",
				"tags": []any{"foo", "bar"},
			},
		}).Create()
		te.newWorkflow(ctx, userA).Create() // no data

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc:   "Data filter",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
			},
			{
				desc:   "DataHasKey filter",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
			},
			{
				desc:   "DataIn filter",
				filter: `{ DataIn: ["meta.name", "TestItem", "foo"] }`,
				count:  1,
			},
			{
				desc:   "DataContains filter",
				filter: `{ DataContains: ["meta.tags", "foo"] }`,
				count:  1,
			},
			{
				desc:   "Data null filter",
				filter: `{ Data: null }`,
				count:  2,
			},
			{
				desc:   "DataHasKey null filter",
				filter: `{ DataHasKey: null }`,
				count:  2,
			},
			{
				desc:   "DataIn null filter",
				filter: `{ DataIn: null }`,
				count:  2,
			},
			{
				desc:   "DataContains null filter",
				filter: `{ DataContains: null }`,
				count:  2,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) { //nolint:paralleltest // Subtests share test environment
				data := execOK[queryWorkflowsData](te, ctx, queryWorkflowsWithFilter, map[string]any{
					"Where": tc.filter,
				})

				assert.Equal(t, tc.count, data.Workflows.TotalCount)
				require.Len(t, data.Workflows.Edges, tc.count)

				if tc.count == 1 {
					assert.Equal(t, target.ID, data.Workflows.Edges[0].Node.ID)
				}
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestWorkflow_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		w1 := te.newWorkflow(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		w2 := te.newWorkflow(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		w3 := te.newWorkflow(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryWorkflowsData](te, ctx, queryWorkflowsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.Workflows.TotalCount)
		assert.Equal(t, w2.ID, data.Workflows.Edges[0].Node.ID)
		assert.Equal(t, w3.ID, data.Workflows.Edges[1].Node.ID)
		assert.Equal(t, w1.ID, data.Workflows.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		w1 := te.newWorkflow(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		w2 := te.newWorkflow(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()
		w3 := te.newWorkflow(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(20)},
		}).Create()

		data := execOK[queryWorkflowsData](te, ctx, queryWorkflowsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 3, data.Workflows.TotalCount)
		assert.Equal(t, w2.ID, data.Workflows.Edges[0].Node.ID)
		assert.Equal(t, w3.ID, data.Workflows.Edges[1].Node.ID)
		assert.Equal(t, w1.ID, data.Workflows.Edges[2].Node.ID)
	})

	t.Run("orders by JSON data with pagination", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newWorkflow(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		w2 := te.newWorkflow(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		w3 := te.newWorkflow(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryWorkflowsData](te, ctx, queryWorkflowsJSONOrder, map[string]any{
			"JSONPath": "sum",
			"First":    2,
		})

		require.Len(t, data.Workflows.Edges, 2)
		assert.True(t, data.Workflows.PageInfo.HasNextPage)
		assert.Equal(t, w2.ID, data.Workflows.Edges[0].Node.ID)
		assert.Equal(t, w3.ID, data.Workflows.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		w1 := te.newWorkflow(ctx, userA).Create()
		w2 := te.newWorkflow(ctx, userA).Create()

		data := execOK[queryWorkflowsData](te, ctx, queryWorkflowsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Workflows.TotalCount)
		assert.Equal(t, w2.ID, data.Workflows.Edges[0].Node.ID)
		assert.Equal(t, w1.ID, data.Workflows.Edges[1].Node.ID)
	})
}
