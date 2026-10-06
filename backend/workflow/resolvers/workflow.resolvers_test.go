package resolvers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
	"github.com/pyck-ai/pyck/backend/workflow/services"
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
			{{- if not .NoWorkerID }}
			workerID: "{{or .WorkerID "test-worker"}}",
			{{- end }}
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

	unregisterWorker = resolver.ParseTemplate(`mutation {
		unregisterWorker(workerID: "{{.WorkerID}}") {
			stopped
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

// invalidSubjectErr is the full message registerWorkflow returns when a
// signal topic is not a valid NATS subscription subject: the
// ErrInvalidSignalTopic text the client sees, then the quoted subject.
func invalidSubjectErr(subject string) string {
	return resolvers.ErrInvalidSignalTopic.Error() + ": " + strconv.Quote(subject) + " is not a valid NATS subscription subject"
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
		}, "jsonschema validation failed")

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

		// Workflow data unchanged -> no workflow event. 1 signal create (new key)
		// + 2 signal deletes (old key + leftover).
		te.assertEventCounts(ctx, map[string]int{
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

		// Workflow data unchanged -> no workflow event. 2 signal deletes (leftovers).
		te.assertEventCounts(ctx, map[string]int{
			"workflowsignal": 2,
		})
	})

	t.Run("changed data still emits a workflow event", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		base := map[string]any{
			"Name":       "wf_data_change",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
		}
		execOK[registerWorkflowData](te, ctx, registerWorkflow, base)
		te.clearEvents(ctx)

		base["DataWeight"] = 5 // real data change
		execOK[registerWorkflowData](te, ctx, registerWorkflow, base)

		te.assertEventCounts(ctx, map[string]int{"workflow": 1})
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

	// Empty tokens pass Parse and Matchable but are not valid subscription
	// subjects, and the signal router reads an empty token as a wildcard: a
	// stored row would fire on events the caller never named. Each row
	// expects the refusal to quote the input topic, before tenant expansion.
	t.Run("refuses topics that are not valid subscription subjects", func(t *testing.T) {
		t.Parallel()

		const entityID = "123e4567-e89b-12d3-a456-426614174000"
		a := tenantA.String()

		cases := []struct {
			name   string
			topics []string
			// wantErr is the sentinel text plus the full quoted subject and
			// the guard's phrase, so a row cannot pass through the Parse,
			// Matchable or duplicate guards, and the guard cannot wrap the
			// wrong sentinel.
			wantErr string
		}{
			{
				name:    "concrete tenant empty service",
				topics:  []string{"request.reply.pyck." + a + ".crud..item." + entityID + ".created"},
				wantErr: invalidSubjectErr(`request.reply.pyck.` + a + `.crud..item.` + entityID + `.created`),
			},
			{
				name:    "concrete tenant empty trailing operation",
				topics:  []string{"request.reply.pyck." + a + ".crud.workflow.item." + entityID + "."},
				wantErr: invalidSubjectErr(`request.reply.pyck.` + a + `.crud.workflow.item.` + entityID + `.`),
			},
			{
				name:    "wildcard tenant empty service",
				topics:  []string{"request.reply.pyck.*.crud..item.*.created"},
				wantErr: invalidSubjectErr(`request.reply.pyck.*.crud..item.*.created`),
			},
			{
				name:    "wildcard tenant empty stream",
				topics:  []string{"request.reply..*.crud.workflow.item.*.created"},
				wantErr: invalidSubjectErr(`request.reply..*.crud.workflow.item.*.created`),
			},
			{
				name:    "temporal concrete tenant empty task queue",
				topics:  []string{"pyck." + a + ".temporal..t.w.r.s"},
				wantErr: invalidSubjectErr(`pyck.` + a + `.temporal..t.w.r.s`),
			},
			{
				name:    "temporal wildcard tenant empty task queue",
				topics:  []string{"pyck.*.temporal..t.w.r.s"},
				wantErr: invalidSubjectErr(`pyck.*.temporal..t.w.r.s`),
			},
			{
				// Regression row: an empty namespace would read as the
				// wildcard tenant and expansion would fill it in. Parse
				// already refuses it as a namespace that is not a UUID.
				name:    "temporal empty namespace",
				topics:  []string{"pyck..temporal.q.t.w.r.s"},
				wantErr: "invalid namespace UUID",
			},
			{
				// The valid first signal must not be stored either: the
				// refusal rolls back the whole registration.
				name: "valid signal in the same batch is not stored",
				topics: []string{
					natsSignalTopicAttrOp(t, &tenantA, "created"),
					"request.reply.pyck." + a + ".crud..item." + entityID + ".created",
				},
				wantErr: invalidSubjectErr(`request.reply.pyck.` + a + `.crud..item.` + entityID + `.created`),
			},
			{
				// Regression row: a repeated invalid topic is reported as
				// invalid, not as a duplicate. Two inputs share a dedup key
				// only when they expand to the same subject, so the first
				// copy always meets the syntax check before the second
				// reaches the duplicate check, wherever the check sits in
				// the loop body. The row pins the reported error, not the
				// order of the two checks.
				name: "invalid topic sent twice is reported as invalid",
				topics: []string{
					"request.reply.pyck." + a + ".crud..item." + entityID + ".created",
					"request.reply.pyck." + a + ".crud..item." + entityID + ".created",
				},
				wantErr: invalidSubjectErr(`request.reply.pyck.` + a + `.crud..item.` + entityID + `.created`),
			},
			{
				// Regression row: whitespace inside a token is already
				// refused by Matchable before the expansion loop.
				name:    "space inside the service token",
				topics:  []string{"request.reply.pyck." + a + ".crud.work flow.item." + entityID + ".created"},
				wantErr: "can never match a published event",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)
				ctx := te.ctx(userWriter)

				signals := make([]SignalInput, 0, len(tc.topics))
				for _, topic := range tc.topics {
					signals = append(signals, SignalInput{NATSTopic: topic, TemporalSignal: "S", TemporalSignalType: "intermediate", FilterRule: "true"})
				}

				execErr(te, ctx, registerWorkflow, map[string]any{
					"Name":       "wf_invalid_subject",
					"TaskQueue":  "test-queue",
					"DataTypeID": itemDataTypeID,
					"DataName":   "wf_invalid_subject",
					"DataWeight": 0,
					"Signals":    signals,
				}, tc.wantErr)

				assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
				assert.Empty(t, te.Ent.Workflow.Query().AllX(ctx))
				te.assertNoEvents(ctx)
			})
		}
	})

	// A START signal starts a workflow on every matching event, so an empty
	// token read as a wildcard would start workflows for events the caller
	// never named. The guard must cover START, not only intermediate.
	t.Run("start signal with empty service token is refused", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userWriter)

		topic := "request.reply.pyck." + tenantA.String() + ".crud..item.*.created"
		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_invalid_subject_start",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_invalid_subject_start",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: topic, TemporalSignal: "", TemporalSignalType: "start", FilterRule: "true"},
			},
		}, invalidSubjectErr(topic))

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
		assert.Empty(t, te.Ent.Workflow.Query().AllX(ctx))
		te.assertNoEvents(ctx)
	})

	t.Run("refused invalid subject leaves earlier signals untouched", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userWriter)

		valid := natsSignalTopicAttrOp(t, &tenantA, "created")
		vars := func(topic string) map[string]any {
			return map[string]any{
				"Name":       "wf_invalid_subject_rereg",
				"TaskQueue":  "test-queue",
				"DataTypeID": itemDataTypeID,
				"DataName":   "wf_invalid_subject_rereg",
				"DataWeight": 0,
				"Signals": []SignalInput{
					{NATSTopic: topic, TemporalSignal: "S", TemporalSignalType: "intermediate", FilterRule: "true"},
				},
			}
		}

		execOK[registerWorkflowData](te, ctx, registerWorkflow, vars(valid))
		before := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, before, 1)
		te.clearEvents(ctx)

		invalid := "request.reply.pyck." + tenantA.String() + ".crud..item.*.created"
		execErr(te, ctx, registerWorkflow, vars(invalid),
			invalidSubjectErr(invalid))

		after := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, after, 1)
		assert.Equal(t, before[0].ID, after[0].ID)
		// The valid request.reply.* signal is stored in the normalised form.
		assert.Equal(t, strings.TrimPrefix(valid, "request.reply."), after[0].NatsTopic)
		assert.True(t, after[0].DeletedAt.IsZero(), "the refused re-registration must not soft-delete the stored signal")
		te.assertNoEvents(ctx)
	})

	// A ">" tail on a mutation topic is no longer a valid signal: once the
	// request.reply prefix is dropped it also parses as an update event, so
	// the topic is ambiguous. The Temporal state-change tail is still valid.
	t.Run("full wildcard tail is still accepted", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userWriter)

		execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_full_wildcard_tail",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_full_wildcard_tail",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "pyck." + tenantA.String() + ".temporal.q.>", TemporalSignal: "B", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		got := te.Ent.WorkflowSignal.Query().Select(entworkflowsignal.FieldNatsTopic).StringsX(ctx)
		assert.ElementsMatch(t, []string{
			"pyck." + tenantA.String() + ".temporal.q.>",
		}, got)
	})

	t.Run("full wildcard tail on a mutation topic is refused as ambiguous", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_full_wildcard_tail_ambiguous",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_full_wildcard_tail_ambiguous",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "request.reply.pyck.*.crud.workflow.>", TemporalSignal: "A", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "invalid nats topic")

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
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

	t.Run("wildcard tenant expansion keeps other wildcards", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_wildcard_tenant_expansion",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_wildcard_tenant_expansion",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "request.reply.pyck.*.crud.inventory.*.*.created", TemporalSignal: "A", TemporalSignalType: "intermediate", FilterRule: "true"},
				{NATSTopic: "pyck.*.temporal.*.childworkflow.*.*.completed", TemporalSignal: "B", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		})

		got := te.Ent.WorkflowSignal.Query().Select(entworkflowsignal.FieldNatsTopic).StringsX(ctx)
		assert.ElementsMatch(t, []string{
			// The legacy request.reply.* form is stored fire-and-forget, with
			// its wildcard tokens intact.
			"pyck." + tenantA.String() + ".crud.inventory.*.*.created",
			"pyck." + tenantA.String() + ".temporal.*.childworkflow.*.*.completed",
		}, got)
	})

	t.Run("topic that can never match is refused", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_unmatchable_topic",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_unmatchable_topic",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "pyck.*.temporal.*.ChildWorkflow.*.*.completed", TemporalSignal: "A", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "can never match a published event")

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
	})

	t.Run("topic that parses as the legacy request reply form is refused", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Wildcards match the literal request, reply and crud tokens, so the
		// prefix strip does not apply and the stored subject would stay in
		// the legacy form that nothing publishes to.
		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_legacy_form_wildcards",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_legacy_form_wildcards",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "*.*.*.*.*.*.*.*.*", TemporalSignal: "A", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "can never match a published event")

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
	})

	t.Run("temporal topic with a non-UUID namespace is refused", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_non_uuid_namespace",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_non_uuid_namespace",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "pyck.default.temporal.q.t.w.r.s", TemporalSignal: "A", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "invalid nats topic")

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
	})

	t.Run("temporal topic with a dashless namespace UUID is refused", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dashless := strings.ReplaceAll(tenantA.String(), "-", "")
		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_dashless_namespace",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "wf_dashless_namespace",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: "pyck." + dashless + ".temporal.q.t.w.r.s", TemporalSignal: "A", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "can never match a published event")

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
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

// TestWorkflowRegister_SignalWithStartAndByIDKinds: the two routed-by-ID kinds
// round-trip through registerWorkflow, and a kind plus signal name is one
// subscription identity (same topic, same name, two kinds are two rows; same
// kind and name twice is a duplicate).
func TestWorkflowRegister_SignalWithStartAndByIDKinds(t *testing.T) {
	t.Parallel()

	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	topic := natsSignalTopicAttrOp(t, &tenantA, "updated")

	data := execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
		"Name":       "wf_sws_kinds",
		"TaskQueue":  "test-queue",
		"DataTypeID": itemDataTypeID,
		"DataName":   "wf_sws_kinds",
		"DataWeight": 0,
		"Signals": []SignalInput{
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "signal_with_start", FilterRule: "true"},
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "signal_by_id", FilterRule: "true"},
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "intermediate", FilterRule: "true"},
		},
	})

	rows, err := te.Ent.WorkflowSignal.Query().
		Where(entworkflowsignal.WorkflowIDEQ(data.RegisterWorkflow.ID), entworkflowsignal.DeletedAtIsNil()).
		AllPages(te.ctx(userA), mixin.Limit)
	require.NoError(t, err)

	got := map[entworkflowsignal.TemporalSignalType]string{}
	for _, r := range rows {
		got[r.TemporalSignalType] = r.TemporalSignal
	}

	assert.Equal(t, map[entworkflowsignal.TemporalSignalType]string{
		entworkflowsignal.TemporalSignalTypeSignalWithStart: "Updated",
		entworkflowsignal.TemporalSignalTypeSignalByID:      "Updated",
		entworkflowsignal.TemporalSignalTypeIntermediate:    "Updated",
	}, got)

	// Re-registering the same three is a heartbeat, not a change.
	execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
		"Name":       "wf_sws_kinds",
		"TaskQueue":  "test-queue",
		"DataTypeID": itemDataTypeID,
		"DataName":   "wf_sws_kinds",
		"DataWeight": 0,
		"Signals": []SignalInput{
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "signal_with_start", FilterRule: "true"},
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "signal_by_id", FilterRule: "true"},
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "intermediate", FilterRule: "true"},
		},
	})

	n, err := te.Ent.WorkflowSignal.Query().
		Where(entworkflowsignal.WorkflowIDEQ(data.RegisterWorkflow.ID), entworkflowsignal.DeletedAtIsNil()).
		Count(te.ctx(userA))
	require.NoError(t, err)
	assert.Equal(t, 3, n, "no rows were added or removed")

	// The same kind, topic and name twice in one registration is a duplicate.
	execErr(te, ctx, registerWorkflow, map[string]any{
		"Name":       "wf_sws_kinds_dup",
		"TaskQueue":  "test-queue",
		"DataTypeID": itemDataTypeID,
		"DataName":   "wf_sws_kinds_dup",
		"DataWeight": 0,
		"Signals": []SignalInput{
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "signal_by_id", FilterRule: "true"},
			{NATSTopic: topic, TemporalSignal: "Updated", TemporalSignalType: "signal_by_id", FilterRule: "true"},
		},
	}, "duplicate signal subscription")

	// The same kind and topic with two names are two subscriptions.
	execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
		"Name":       "wf_sws_kinds_names",
		"TaskQueue":  "test-queue",
		"DataTypeID": itemDataTypeID,
		"DataName":   "wf_sws_kinds_names",
		"DataWeight": 0,
		"Signals": []SignalInput{
			{NATSTopic: topic, TemporalSignal: "A", TemporalSignalType: "signal_with_start", FilterRule: "true"},
			{NATSTopic: topic, TemporalSignal: "B", TemporalSignalType: "signal_with_start", FilterRule: "true"},
		},
	})
}

// A Signal-With-Start or signal-by-ID subscription without a signal name can
// never be delivered, so registration refuses it instead of letting every
// matching event fail.
func TestWorkflowRegister_SignalWithStartAndByIDNeedASignalName(t *testing.T) {
	t.Parallel()

	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	topic := natsSignalTopicAttrOp(t, &tenantA, "updated")

	for _, typ := range []string{"signal_with_start", "signal_by_id"} {
		for _, name := range []string{"", "   "} {
			execErr(te, ctx, registerWorkflow, map[string]any{
				"Name":       "wf_no_signal_name_" + typ,
				"TaskQueue":  "test-queue",
				"DataTypeID": itemDataTypeID,
				"DataName":   "wf_no_signal_name_" + typ,
				"DataWeight": 0,
				"Signals": []SignalInput{
					{NATSTopic: topic, TemporalSignal: name, TemporalSignalType: typ, FilterRule: "true"},
				},
			}, "needs a signal name")
		}
	}
}

// =============================================================================
// WORKER-SCOPED SUBSCRIPTION TESTS
// =============================================================================

func TestWorkflowRegister_WorkerScoped(t *testing.T) {
	t.Parallel()

	register := func(te *testEnv, ctx context.Context, worker string) uuid.UUID {
		vars := map[string]any{
			"Name":       "wf_worker_scoped",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: natsSignalTopicAttrOp(t, &tenantA, "created"), TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}
		vars["WorkerID"] = worker
		return execOK[registerWorkflowData](te, ctx, registerWorkflow, vars).RegisterWorkflow.ID
	}

	t.Run("concurrent workers keep independent subscriptions", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		id1 := register(te, ctx, "worker-a")
		id2 := register(te, ctx, "worker-b")
		assert.Equal(t, id1, id2, "workflow identity is shared across workers")

		byWorker := map[string]int{}
		for _, s := range te.Ent.WorkflowSignal.Query().AllX(ctx) {
			byWorker[s.WorkerID]++
			assert.True(t, s.ExpiresAt.After(time.Now()), "expiry must be in the future")
		}
		// Neither worker deleted the other's row: one subscription each.
		assert.Equal(t, 1, byWorker["worker-a"])
		assert.Equal(t, 1, byWorker["worker-b"])
	})

	t.Run("rejects an over-long worker id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_worker_scoped",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"WorkerID":   strings.Repeat("w", 256),
			"Signals": []SignalInput{
				{NATSTopic: natsSignalTopicAttrOp(t, &tenantA, "created"), TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, "invalid worker id")

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx), "rejected registration must not persist a subscription")
	})

	t.Run("accepts a worker id at the limit", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, strings.Repeat("w", 255))

		sigs := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, sigs, 1)
		assert.Len(t, sigs[0].WorkerID, 255)
	})

	// workerID is String!, so the schema rejects an omitted one before the resolver.
	t.Run("rejects a missing worker id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_worker_scoped",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"NoWorkerID": true,
			"Signals": []SignalInput{
				{NATSTopic: natsSignalTopicAttrOp(t, &tenantA, "created"), TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, `"RegisterWorkflowWithSignalsInput.workerID" of required type "String!" was not provided`)

		assert.Empty(t, te.Ent.WorkflowSignal.Query().AllX(ctx))
		assert.Empty(t, te.Ent.Workflow.Query().AllX(ctx), "rejected registration must not create the workflow")
	})

	t.Run("rejects a blank worker id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_worker_scoped",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"WorkerID":   "   ",
		}, "a worker id is required")
	})

	t.Run("a second worker on the same workflow and queue gets its own rows", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		id1 := register(te, ctx, "worker-a")
		first := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, first, 1)

		id2 := register(te, ctx, "worker-b")
		assert.Equal(t, id1, id2, "same workflow row, no duplicate")
		assert.Len(t, te.Ent.Workflow.Query().AllX(ctx), 1)

		all := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, all, 2)

		byWorker := map[string]*ent.WorkflowSignal{}
		for _, s := range all {
			byWorker[s.WorkerID] = s
		}
		require.Contains(t, byWorker, "worker-a")
		require.Contains(t, byWorker, "worker-b")
		assert.NotEqual(t, byWorker["worker-a"].ID, byWorker["worker-b"].ID)

		// worker-a's row is untouched by worker-b's registration.
		assert.Equal(t, first[0].ID, byWorker["worker-a"].ID)
		assert.True(t, first[0].ExpiresAt.Equal(byWorker["worker-a"].ExpiresAt), "worker-a's expiry not refreshed by worker-b")
		assert.True(t, first[0].UpdatedAt.Equal(byWorker["worker-a"].UpdatedAt), "worker-a's row not modified")
	})

	t.Run("rejects a task queue change", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		id := register(te, ctx, "worker-a")
		before := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, before, 1)

		execErr(te, ctx, registerWorkflow, map[string]any{
			"Name":       "wf_worker_scoped",
			"TaskQueue":  "other-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"WorkerID":   "worker-b",
			"Signals": []SignalInput{
				{NATSTopic: natsSignalTopicAttrOp(t, &tenantA, "created"), TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}, `registered on task queue "test-queue", not "other-queue"`)

		wfs := te.Ent.Workflow.Query().AllX(ctx)
		require.Len(t, wfs, 1, "no second workflow row")
		assert.Equal(t, id, wfs[0].ID)
		assert.Equal(t, "test-queue", wfs[0].TaskQueue)

		after := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, after, 1, "no signals created or removed")
		assert.Equal(t, before[0].ID, after[0].ID)
		assert.Equal(t, before[0].WorkerID, after[0].WorkerID)
	})

	t.Run("re-registration refreshes expiry without duplicating", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, "worker-a")
		first := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, first, 1)

		register(te, ctx, "worker-a")
		second := te.Ent.WorkflowSignal.Query().AllX(ctx)
		require.Len(t, second, 1, "heartbeat must not create a duplicate row")
		assert.False(t, second[0].ExpiresAt.Before(first[0].ExpiresAt), "expiry must be refreshed")
	})
}

func TestSubscriptionJanitor_Sweep(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	topic := natsSignalTopicAttrOp(t, &tenantA, "created")
	reg := func(worker string) {
		vars := map[string]any{
			"Name":       "wf_janitor",
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"Signals": []SignalInput{
				{NATSTopic: topic, TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: "true"},
			},
		}
		vars["WorkerID"] = worker
		execOK[registerWorkflowData](te, ctx, registerWorkflow, vars)
	}
	reg("dead-worker")
	reg("live-worker")

	// Lapse the dead worker's subscription.
	var deadID uuid.UUID
	for _, s := range te.Ent.WorkflowSignal.Query().AllX(ctx) {
		if s.WorkerID == "dead-worker" {
			deadID = s.ID
		}
	}
	require.NotEqual(t, uuid.Nil, deadID, "dead-worker subscription not found")

	supCtx := feature.Context(ctx, feature.FEATURE_SUPPRESS_EVENTS)
	te.Ent.WorkflowSignal.UpdateOneID(deadID).SetExpiresAt(time.Now().UTC().Add(-time.Hour)).ExecX(supCtx)

	n, err := services.NewSubscriptionJanitor(te.Ent, time.Minute).Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the expired subscription is reaped")

	remaining := map[string]bool{}
	for _, s := range te.Ent.WorkflowSignal.Query().AllX(ctx) {
		remaining[s.WorkerID] = true
	}
	assert.False(t, remaining["dead-worker"], "expired subscription must be reaped")
	assert.True(t, remaining["live-worker"], "live subscription must survive")
}

// =============================================================================
// UNREGISTER WORKER TESTS
// =============================================================================

type unregisterWorkerData struct {
	UnregisterWorker struct {
		Stopped int
	}
}

func TestUnregisterWorker(t *testing.T) {
	t.Parallel()

	register := func(te *testEnv, ctx context.Context, name, worker, filter string) uuid.UUID {
		return execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name":       name,
			"TaskQueue":  "test-queue",
			"DataTypeID": itemDataTypeID,
			"DataName":   "testWorkflow",
			"DataWeight": 0,
			"WorkerID":   worker,
			"Signals": []SignalInput{
				{NATSTopic: natsSignalTopicAttrOp(t, &tenantA, "created"), TemporalSignal: "OrderCreated", TemporalSignalType: "intermediate", FilterRule: filter},
			},
		}).RegisterWorkflow.ID
	}

	// stopped maps worker ID to how many of its rows are marked stopped, and
	// total maps it to how many rows it owns: unregister never deletes.
	stopped := func(te *testEnv, ctx context.Context) (stoppedRows, total map[string]int) {
		stoppedRows, total = map[string]int{}, map[string]int{}
		for _, s := range te.Ent.WorkflowSignal.Query().AllX(ctx) {
			total[s.WorkerID]++
			if s.StoppedAt != nil {
				stoppedRows[s.WorkerID]++
			}
		}
		return stoppedRows, total
	}

	t.Run("marks only its own rows stopped and deletes nothing", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, "wf_one", "worker-a", "true")
		register(te, ctx, "wf_one", "worker-b", "true")
		register(te, ctx, "wf_two", "worker-a", "true")
		register(te, ctx, "wf_solo", "worker-c", "true")

		got := execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		assert.Equal(t, 2, got.UnregisterWorker.Stopped)

		stoppedRows, total := stopped(te, ctx)
		assert.Equal(t, map[string]int{"worker-a": 2}, stoppedRows)
		assert.Equal(t, map[string]int{"worker-a": 2, "worker-b": 1, "worker-c": 1}, total, "no row is deleted")
		assert.Len(t, te.Ent.Workflow.Query().AllX(ctx), 3, "workflow rows are never touched")
	})

	t.Run("the last worker's stop is the same single update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, "wf_solo", "worker-a", "true")

		got := execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		assert.Equal(t, 1, got.UnregisterWorker.Stopped)

		stoppedRows, total := stopped(te, ctx)
		assert.Equal(t, map[string]int{"worker-a": 1}, stoppedRows)
		assert.Equal(t, map[string]int{"worker-a": 1}, total, "rows stay and lapse via TTL")
	})

	t.Run("skips rows that already lapsed", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, "wf_one", "worker-a", "true")

		supCtx := feature.Context(ctx, feature.FEATURE_SUPPRESS_EVENTS)
		for _, s := range te.Ent.WorkflowSignal.Query().AllX(ctx) {
			te.Ent.WorkflowSignal.UpdateOneID(s.ID).SetExpiresAt(time.Now().UTC().Add(-time.Hour)).ExecX(supCtx)
		}

		got := execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		assert.Equal(t, 0, got.UnregisterWorker.Stopped)
	})

	t.Run("idempotent and unknown worker", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, "wf_one", "worker-a", "true")
		register(te, ctx, "wf_one", "worker-b", "true")

		first := execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		assert.Equal(t, 1, first.UnregisterWorker.Stopped)

		again := execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		assert.Equal(t, 0, again.UnregisterWorker.Stopped, "a repeat call stops nothing new")

		unknown := execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "nobody"})
		assert.Equal(t, 0, unknown.UnregisterWorker.Stopped)

		stoppedRows, _ := stopped(te, ctx)
		assert.Equal(t, map[string]int{"worker-a": 1}, stoppedRows)
	})

	t.Run("re-registration clears stopped_at", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		register(te, ctx, "wf_one", "worker-a", "true")
		register(te, ctx, "wf_two", "worker-a", "true")
		execOK[unregisterWorkerData](te, ctx, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		stoppedRows, _ := stopped(te, ctx)
		require.Equal(t, map[string]int{"worker-a": 2}, stoppedRows)

		// Unchanged signals (a heartbeat): the TTL-only branch.
		register(te, ctx, "wf_one", "worker-a", "true")
		// Changed filter: the content-update branch.
		register(te, ctx, "wf_two", "worker-a", "false")

		stoppedRows, total := stopped(te, ctx)
		assert.Empty(t, stoppedRows, "a registering worker is running again")
		assert.Equal(t, map[string]int{"worker-a": 2}, total)
	})

	t.Run("is tenant scoped", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		register(te, ctxA, "wf_one", "worker-a", "true")
		register(te, ctxA, "wf_one", "worker-b", "true")

		// Same worker ID in another tenant: nothing to stop there.
		got := execOK[unregisterWorkerData](te, ctxB, unregisterWorker, map[string]any{"WorkerID": "worker-a"})
		assert.Equal(t, 0, got.UnregisterWorker.Stopped)

		stoppedRows, _ := stopped(te, ctxA)
		assert.Empty(t, stoppedRows)
	})

	t.Run("rejects a blank worker id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		execErr(te, te.ctx(userA), unregisterWorker, map[string]any{"WorkerID": "  "}, "a worker id is required")
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

// =============================================================================
// DATATYPE VERSION REPOINT TESTS
// =============================================================================

var (
	registerWorkflowPinOnly = resolver.ParseTemplate(`mutation {
		registerWorkflow(input: {
			name: "{{.Name}}",
			taskQueue: "test-queue",
			workerID: "test-worker",
			dataTypeID: "{{.DataTypeID}}"
		}) {
			id
			dataTypeID
			data
		}
	}`)

	registerWorkflowLabelData = resolver.ParseTemplate(`mutation {
		registerWorkflow(input: {
			name: "{{.Name}}",
			taskQueue: "test-queue",
			workerID: "test-worker",
			dataTypeID: "{{.DataTypeID}}",
			data: { label: "{{.Label}}" }
		}) {
			id
			dataTypeID
			data
		}
	}`)
)

const (
	// labelSchema accepts any object carrying an optional string "label".
	labelSchema = `{
	"$schema": "https://json-schema.org/draft/2019-09/schema",
	"$id": "http://example.com/label.json",
	"type": "object",
	"properties": { "label": { "type": "string" } }
}`

	// labelSchemaRequiringCode is the incompatible successor of labelSchema:
	// data written under labelSchema lacks "code" and therefore violates it.
	labelSchemaRequiringCode = `{
	"$schema": "https://json-schema.org/draft/2019-09/schema",
	"$id": "http://example.com/label-code.json",
	"type": "object",
	"required": ["code"],
	"properties": {
		"label": { "type": "string" },
		"code": { "type": "string" }
	}
}`
)

// addLabelDataType registers a "label"-slugged DataType version with the given
// schema and returns its id.
func addLabelDataType(te *testEnv, jsonSchema string) uuid.UUID {
	te.t.Helper()
	id := uuid.New()
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         id,
		Slug:       "label",
		TenantID:   tenantA,
		JsonSchema: jsonSchema,
	})
	return id
}

func TestWorkflowRegister_DataTypeRepoint(t *testing.T) {
	t.Parallel()

	t.Run("rejects repoint to a version the persisted data violates", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		v1 := addLabelDataType(te, labelSchema)
		v2 := addLabelDataType(te, labelSchemaRequiringCode)

		created := execOK[registerWorkflowData](te, ctx, registerWorkflowLabelData, map[string]any{
			"Name":       "wf_repoint_incompatible",
			"DataTypeID": v1,
			"Label":      "first",
		})
		require.Equal(t, v1, created.RegisterWorkflow.DataTypeID)

		// The registration omits data, so the row's persisted data is what the
		// newly pinned version has to accept — and it does not.
		execErr(te, ctx, registerWorkflowPinOnly, map[string]any{
			"Name":       "wf_repoint_incompatible",
			"DataTypeID": v2,
		}, "jsonschema validation failed")

		stored := te.Ent.Workflow.GetX(ctx, created.RegisterWorkflow.ID)
		assert.Equal(t, v1, stored.DataTypeID, "rejected repoint must leave the pin untouched")
	})

	t.Run("allows repoint to a compatible version", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		v1 := addLabelDataType(te, labelSchema)
		v2 := addLabelDataType(te, labelSchema)

		created := execOK[registerWorkflowData](te, ctx, registerWorkflowLabelData, map[string]any{
			"Name":       "wf_repoint_compatible",
			"DataTypeID": v1,
			"Label":      "first",
		})

		repointed := execOK[registerWorkflowData](te, ctx, registerWorkflowPinOnly, map[string]any{
			"Name":       "wf_repoint_compatible",
			"DataTypeID": v2,
		})

		assert.Equal(t, created.RegisterWorkflow.ID, repointed.RegisterWorkflow.ID)
		assert.Equal(t, v2, repointed.RegisterWorkflow.DataTypeID)
		assert.Equal(t, map[string]any{"label": "first"}, repointed.RegisterWorkflow.Data,
			"repoint without data must leave the row's data untouched")
	})

	t.Run("allows heartbeat under the pinned version with unvalidatable data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		strict := addLabelDataType(te, labelSchemaRequiringCode)

		// A legacy row whose data its own pinned version rejects: re-registering
		// under that same version is a heartbeat, not a repoint, so it must not
		// be forced through validation.
		wf := te.newWorkflow(ctx, userA).
			Name("wf_repoint_heartbeat").
			Data(map[string]any{"label": "legacy"}).
			DataType(strict, "label").
			Create()
		te.clearEvents(ctx)

		got := execOK[registerWorkflowData](te, ctx, registerWorkflowPinOnly, map[string]any{
			"Name":       "wf_repoint_heartbeat",
			"DataTypeID": strict,
		})

		assert.Equal(t, wf.ID, got.RegisterWorkflow.ID)
		te.assertNoEvents(ctx)
	})
}
