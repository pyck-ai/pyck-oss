package resolvers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	common "go.temporal.io/api/common/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	"github.com/pyck-ai/pyck/backend/workflow/model"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	workflowExecutions = resolver.ParseTemplate(`query {
		workflowExecutions{{if or .Where .First .After .Last .Before .OrderBy}}(
			{{- if .Where }}
			where: {
				{{- if .Where.TypeName }}
				typeName: "{{.Where.TypeName}}"
				{{- end }}
				{{- if .Where.TypeNameIn }}
				typeNameIn: [{{range $i, $v := .Where.TypeNameIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.TypeNameNotIn }}
				typeNameNotIn: [{{range $i, $v := .Where.TypeNameNotIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.TypeNameNeq }}
				typeNameNEQ: "{{.Where.TypeNameNeq}}"
				{{- end }}
				{{- if .Where.TypeNameContains }}
				typeNameContains: "{{.Where.TypeNameContains}}"
				{{- end }}
				{{- if .Where.WorkflowName }}
				workflowName: "{{.Where.WorkflowName}}"
				{{- end }}
				{{- if .Where.WorkflowNameContains }}
				workflowNameContains: "{{.Where.WorkflowNameContains}}"
				{{- end }}
				{{- if .Where.Status }}
				status: "{{.Where.Status}}"
				{{- end }}
				{{- if .Where.StatusIn }}
				statusIn: [{{range $i, $v := .Where.StatusIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.StatusNotIn }}
				statusNotIn: [{{range $i, $v := .Where.StatusNotIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.StatusNeq }}
				statusNEQ: "{{.Where.StatusNeq}}"
				{{- end }}
				{{- if .Where.StatusContains }}
				statusContains: "{{.Where.StatusContains}}"
				{{- end }}
				{{- if .Where.Assignee }}
				assignee: "{{.Where.Assignee}}"
				{{- end }}
				{{- if .Where.AssigneeIn }}
				assigneeIn: [{{range $i, $v := .Where.AssigneeIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.AssigneeNeq }}
				assigneeNEQ: "{{.Where.AssigneeNeq}}"
				{{- end }}
				{{- if ne .Where.AssigneeIsNil nil }}
				assigneeIsNil: {{.Where.AssigneeIsNil}}
				{{- end }}
				{{- if ne .Where.AssigneeNotNil nil }}
				assigneeNotNil: {{.Where.AssigneeNotNil}}
				{{- end }}
				{{- if ne .Where.IsAssignable nil }}
				isAssignable: {{.Where.IsAssignable}}
				{{- end }}
				{{- if .Where.StartTime }}
				startTime: "{{.Where.StartTime}}"
				{{- end }}
				{{- if .Where.StartTimeGt }}
				startTimeGT: "{{.Where.StartTimeGt}}"
				{{- end }}
				{{- if .Where.StartTimeGte }}
				startTimeGTE: "{{.Where.StartTimeGte}}"
				{{- end }}
				{{- if .Where.StartTimeLt }}
				startTimeLT: "{{.Where.StartTimeLt}}"
				{{- end }}
				{{- if .Where.StartTimeLte }}
				startTimeLTE: "{{.Where.StartTimeLte}}"
				{{- end }}
				{{- if ne .Where.CloseTimeIsNil nil }}
				closeTimeIsNil: {{.Where.CloseTimeIsNil}}
				{{- end }}
				{{- if .Where.Service }}
				service: "{{.Where.Service}}"
				{{- end }}
				{{- if .Where.ServiceIn }}
				serviceIn: [{{range $i, $v := .Where.ServiceIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.ServiceNotIn }}
				serviceNotIn: [{{range $i, $v := .Where.ServiceNotIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.ServiceContains }}
				serviceContains: "{{.Where.ServiceContains}}"
				{{- end }}
				{{- if .Where.ServiceHasPrefix }}
				serviceHasPrefix: "{{.Where.ServiceHasPrefix}}"
				{{- end }}
				{{- if .Where.ServiceHasSuffix }}
				serviceHasSuffix: "{{.Where.ServiceHasSuffix}}"
				{{- end }}
				{{- if ne .Where.ServiceIsNil nil }}
				serviceIsNil: {{.Where.ServiceIsNil}}
				{{- end }}
				{{- if ne .Where.ServiceNotNil nil }}
				serviceNotNil: {{.Where.ServiceNotNil}}
				{{- end }}
				{{- if .Where.DataType }}
				dataType: "{{.Where.DataType}}"
				{{- end }}
				{{- if .Where.DataTypeIn }}
				dataTypeIn: [{{range $i, $v := .Where.DataTypeIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.DataTypeNotIn }}
				dataTypeNotIn: [{{range $i, $v := .Where.DataTypeNotIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.DataTypeContains }}
				dataTypeContains: "{{.Where.DataTypeContains}}"
				{{- end }}
				{{- if .Where.DataTypeHasPrefix }}
				dataTypeHasPrefix: "{{.Where.DataTypeHasPrefix}}"
				{{- end }}
				{{- if .Where.DataTypeHasSuffix }}
				dataTypeHasSuffix: "{{.Where.DataTypeHasSuffix}}"
				{{- end }}
				{{- if ne .Where.DataTypeIsNil nil }}
				dataTypeIsNil: {{.Where.DataTypeIsNil}}
				{{- end }}
				{{- if ne .Where.DataTypeNotNil nil }}
				dataTypeNotNil: {{.Where.DataTypeNotNil}}
				{{- end }}
				{{- if .Where.DataId }}
				dataId: "{{.Where.DataId}}"
				{{- end }}
				{{- if .Where.DataIdIn }}
				dataIdIn: [{{range $i, $v := .Where.DataIdIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.DataIdNotIn }}
				dataIdNotIn: [{{range $i, $v := .Where.DataIdNotIn}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
				{{- end }}
				{{- if .Where.DataIdContains }}
				dataIdContains: "{{.Where.DataIdContains}}"
				{{- end }}
				{{- if .Where.DataIdHasPrefix }}
				dataIdHasPrefix: "{{.Where.DataIdHasPrefix}}"
				{{- end }}
				{{- if .Where.DataIdHasSuffix }}
				dataIdHasSuffix: "{{.Where.DataIdHasSuffix}}"
				{{- end }}
				{{- if ne .Where.DataIdIsNil nil }}
				dataIdIsNil: {{.Where.DataIdIsNil}}
				{{- end }}
				{{- if ne .Where.DataIdNotNil nil }}
				dataIdNotNil: {{.Where.DataIdNotNil}}
				{{- end }}
				{{- if .Where.Targets }}
				targets: [{{range $i, $v := .Where.Targets}}{{if $i}}, {{end}}{{$v}}{{end}}]
				{{- end }}
				{{- if .Where.TargetsNotIn }}
				targetsNotIn: [{{range $i, $v := .Where.TargetsNotIn}}{{if $i}}, {{end}}{{$v}}{{end}}]
				{{- end }}
			}
			{{- end }}
			{{- if .First }}
			first: {{.First}}
			{{- end }}
			{{- if .After }}
			after: "{{.After}}"
			{{- end }}
			{{- if .Last }}
			last: {{.Last}}
			{{- end }}
			{{- if .Before }}
			before: "{{.Before}}"
			{{- end }}
			{{- if .OrderBy }}
			orderBy: { field: {{.OrderBy.Field}}, direction: {{.OrderBy.Direction}} }
			{{- end }}
		){{end}} {
			totalCount
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
			edges {
				cursor
				node {
					execution {
						workflowId
						id
					}
					type {
						name
					}
					status
				}
			}
		}
	}`)

	assignableWorkflowExecutionsQuery = resolver.ParseTemplate(`query {
		assignableWorkflowExecutions{{if or .Where .First .After .OrderBy}}(
			{{- if .First }}
			first: {{.First}}
			{{- end }}
			{{- if .After }}
			after: "{{.After}}"
			{{- end }}
		){{end}} {
			totalCount
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
			edges {
				cursor
				node {
					execution {
						workflowId
						id
					}
					type {
						name
					}
					status
				}
			}
		}
	}`)

	workflowHistory = resolver.ParseTemplate(`query {
		workflowHistory(
			where: {
				{{- if .Where.TypeName }}
				typeName: "{{.Where.TypeName}}"
				{{- end }}
			}
			{{- if .Limit }}
			limit: {{.Limit}}
			{{- end }}
		) {
			totalCount
			edges {
				node {
					execution {
						workflowId
						id
					}
					typeName
					history {
						eventId
						eventType
					}
				}
			}
		}
	}`)
)

// =============================================================================
// INPUT TYPES
// =============================================================================

type workflowExecutionsWhereInput struct {
	TypeName             *string
	TypeNameIn           []string
	TypeNameNotIn        []string
	TypeNameNeq          *string
	TypeNameContains     *string
	WorkflowName         *string
	WorkflowNameContains *string
	Status               *string
	StatusIn             []string
	StatusNotIn          []string
	StatusNeq            *string
	StatusContains       *string
	Assignee             *string
	AssigneeIn           []string
	AssigneeNeq          *string
	AssigneeIsNil        *bool
	AssigneeNotNil       *bool
	IsAssignable         *bool
	StartTime            *string
	StartTimeGt          *string
	StartTimeGte         *string
	StartTimeLt          *string
	StartTimeLte         *string
	CloseTimeIsNil       *bool
	Service              *string
	ServiceIn            []string
	ServiceNotIn         []string
	ServiceContains      *string
	ServiceHasPrefix     *string
	ServiceHasSuffix     *string
	ServiceIsNil         *bool
	ServiceNotNil        *bool
	DataType             *string
	DataTypeIn           []string
	DataTypeNotIn        []string
	DataTypeContains     *string
	DataTypeHasPrefix    *string
	DataTypeHasSuffix    *string
	DataTypeIsNil        *bool
	DataTypeNotNil       *bool
	DataId               *string
	DataIdIn             []string
	DataIdNotIn          []string
	DataIdContains       *string
	DataIdHasPrefix      *string
	DataIdHasSuffix      *string
	DataIdIsNil          *bool
	DataIdNotNil         *bool
	Targets              []string
	TargetsNotIn         []string
}

type workflowExecutionOrderInput struct {
	Field     string
	Direction string
}

type workflowHistoryWhereInput struct {
	TypeName *string
}

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type workflowExecutionsData struct {
	WorkflowExecutions *model.WorkflowExecutionInfoConnection
}

type assignableWorkflowExecutionsData struct {
	AssignableWorkflowExecutions *model.WorkflowExecutionInfoConnection
}

type workflowHistoryData struct {
	WorkflowHistory *model.WorkflowExecutionHistoryConnection
}

// =============================================================================
// NIL WHERE TESTS
// =============================================================================

func TestWorkflowExecutions_NilWhere(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedQuery string
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedQuery = request.GetQuery()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	data := execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"Where": nil,
	})

	require.NotNil(t, data.WorkflowExecutions)
	assert.Equal(t, fmt.Sprintf(`pyck_tenant_id IN (%q)`, userA.TenantID.String()), capturedQuery)
}

// =============================================================================
// TYPE NAME FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_TypeNameFilters(t *testing.T) {
	t.Parallel()

	t.Run("single type name", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				TypeName: stringPtr("MyWorkflow"),
			},
		})

		assert.Equal(t, fmt.Sprintf(`pyck_tenant_id IN (%q) AND WorkflowType = "MyWorkflow"`, userA.TenantID.String()), capturedQuery)
	})

	t.Run("type name IN", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				TypeNameIn: []string{"Workflow1", "Workflow2"},
			},
		})

		assert.Equal(t, fmt.Sprintf(`pyck_tenant_id IN (%q) AND WorkflowType IN ("Workflow1", "Workflow2")`, userA.TenantID.String()), capturedQuery)
	})

	t.Run("type name CONTAINS", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				TypeNameContains: stringPtr("Payment"),
			},
		})

		assert.Equal(t, fmt.Sprintf(`pyck_tenant_id IN (%q) AND WorkflowType CONTAINS "Payment"`, userA.TenantID.String()), capturedQuery)
	})

	t.Run("type name NEQ", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				TypeNameNeq: stringPtr("ExcludedWorkflow"),
				StatusNeq:   stringPtr("FAILED"),
			},
		})

		assert.Contains(t, capturedQuery, `WorkflowType != "ExcludedWorkflow"`)
		assert.Contains(t, capturedQuery, `ExecutionStatus != "FAILED"`)
	})

	t.Run("type name NOT IN", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				TypeNameNotIn: []string{"ExcludedType"},
				StatusNotIn:   []string{"COMPLETED", "FAILED"},
			},
		})

		assert.Contains(t, capturedQuery, `WorkflowType NOT IN ("ExcludedType")`)
		assert.Contains(t, capturedQuery, `ExecutionStatus NOT IN ("COMPLETED", "FAILED")`)
	})
}

// =============================================================================
// STATUS FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_StatusFilters(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedQuery string
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedQuery = request.GetQuery()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"Where": &workflowExecutionsWhereInput{
			StatusIn: []string{"RUNNING", "PENDING"},
		},
	})

	assert.Equal(t, fmt.Sprintf(`pyck_tenant_id IN (%q) AND ExecutionStatus IN ("RUNNING", "PENDING")`, userA.TenantID.String()), capturedQuery)
}

// =============================================================================
// WORKFLOW TARGETS FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_TargetsFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		where          *workflowExecutionsWhereInput
		expectContains []string
	}{
		{
			name:           "single target → IN",
			where:          &workflowExecutionsWhereInput{Targets: []string{"WEB"}},
			expectContains: []string{`pyck_workflow_targets IN ("WEB")`},
		},
		{
			name:           "multiple targets → IN with all",
			where:          &workflowExecutionsWhereInput{Targets: []string{"WEB", "MOBILE"}},
			expectContains: []string{`pyck_workflow_targets IN ("WEB", "MOBILE")`},
		},
		{
			name:           "targetsNotIn → NOT IN",
			where:          &workflowExecutionsWhereInput{TargetsNotIn: []string{"SETUP"}},
			expectContains: []string{`pyck_workflow_targets NOT IN ("SETUP")`},
		},
		{
			name: "targets combined with status",
			where: &workflowExecutionsWhereInput{
				Status:  stringPtr("RUNNING"),
				Targets: []string{"WEB"},
			},
			expectContains: []string{
				`ExecutionStatus = "RUNNING"`,
				`pyck_workflow_targets IN ("WEB")`,
				" AND ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := setupWithMockWorkflow(t)
			defer te.Close(t)
			ctx := te.ctx(userA)

			var capturedQuery string
			te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
				capturedQuery = request.GetQuery()
				return &workflowservice.ListWorkflowExecutionsResponse{
					Executions: []*workflowpb.WorkflowExecutionInfo{},
				}, nil
			}

			execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
				"Where": tt.where,
			})

			for _, expected := range tt.expectContains {
				assert.Contains(t, capturedQuery, expected, "captured query: %s", capturedQuery)
			}
		})
	}
}

// =============================================================================
// TIME RANGE FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_TimeRangeFilters(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedQuery string
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedQuery = request.GetQuery()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"Where": &workflowExecutionsWhereInput{
			StartTimeGt: stringPtr("2024-01-01T00:00:00Z"),
			StartTimeLt: stringPtr("2024-12-31T23:59:59Z"),
		},
	})

	assert.Contains(t, capturedQuery, `StartTime > "2024-01-01T00:00:00Z"`)
	assert.Contains(t, capturedQuery, `StartTime < "2024-12-31T23:59:59Z"`)

	andCount := strings.Count(capturedQuery, " AND ")
	assert.GreaterOrEqual(t, andCount, 2, "Should have at least 2 AND operators")
}

// =============================================================================
// COMPLEX FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_ComplexFilters(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedQuery string
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedQuery = request.GetQuery()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"Where": &workflowExecutionsWhereInput{
			TypeName:    stringPtr("MyWorkflow"),
			StatusIn:    []string{"RUNNING"},
			StartTimeGt: stringPtr("2024-01-01T00:00:00Z"),
			Assignee:    stringPtr("user-123"),
		},
	})

	assert.Contains(t, capturedQuery, `WorkflowType = "MyWorkflow"`)
	assert.Contains(t, capturedQuery, `ExecutionStatus IN ("RUNNING")`)
	assert.Contains(t, capturedQuery, `StartTime > "2024-01-01T00:00:00Z"`)
	assert.Contains(t, capturedQuery, `pyck_workflow_assignee = "user-123"`)

	andCount := strings.Count(capturedQuery, " AND ")
	assert.GreaterOrEqual(t, andCount, 3, "Should have at least 3 AND operators")
}

// =============================================================================
// SERVICE FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_ServiceFilters(t *testing.T) {
	t.Parallel()

	t.Run("service equals", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				Service: stringPtr("picking"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_service = "picking"`)
	})

	t.Run("service IN", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				ServiceIn: []string{"picking", "shipping"},
			},
		})

		assert.Contains(t, capturedQuery, `pyck_service IN ("picking", "shipping")`)
	})

	t.Run("service IS NULL", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				ServiceIsNil: boolPtr(true),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_service IS NULL`)
	})

	t.Run("service CONTAINS", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				ServiceContains: stringPtr("pick"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_service CONTAINS "pick"`)
	})

	t.Run("service STARTS_WITH", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				ServiceHasPrefix: stringPtr("pick"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_service STARTS_WITH "pick"`)
	})
}

// =============================================================================
// DATA TYPE FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_DataTypeFilters(t *testing.T) {
	t.Parallel()

	t.Run("dataType equals", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				DataType: stringPtr("order"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_data_type = "order"`)
	})

	t.Run("dataType IN", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				DataTypeIn: []string{"order", "shipment"},
			},
		})

		assert.Contains(t, capturedQuery, `pyck_data_type IN ("order", "shipment")`)
	})

	t.Run("dataType ENDS_WITH", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				DataTypeHasSuffix: stringPtr("_order"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_data_type ENDS_WITH "_order"`)
	})
}

// =============================================================================
// DATA ID FILTER TESTS
// =============================================================================

func TestWorkflowExecutions_DataIdFilters(t *testing.T) {
	t.Parallel()

	t.Run("dataId equals", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				DataId: stringPtr("order-123"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_data_id = "order-123"`)
	})

	t.Run("dataId IN", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				DataIdIn: []string{"order-123", "order-456"},
			},
		})

		assert.Contains(t, capturedQuery, `pyck_data_id IN ("order-123", "order-456")`)
	})

	t.Run("dataId STARTS_WITH", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedQuery string
		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedQuery = request.GetQuery()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"Where": &workflowExecutionsWhereInput{
				DataIdHasPrefix: stringPtr("order-"),
			},
		})

		assert.Contains(t, capturedQuery, `pyck_data_id STARTS_WITH "order-"`)
	})
}

// =============================================================================
// COMBINED NEW FILTERS TEST
// =============================================================================

func TestWorkflowExecutions_CombinedNewFilters(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedQuery string
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedQuery = request.GetQuery()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"Where": &workflowExecutionsWhereInput{
			Service:  stringPtr("picking"),
			DataType: stringPtr("order"),
			DataId:   stringPtr("order-123"),
		},
	})

	assert.Contains(t, capturedQuery, `pyck_service = "picking"`)
	assert.Contains(t, capturedQuery, `pyck_data_type = "order"`)
	assert.Contains(t, capturedQuery, `pyck_data_id = "order-123"`)

	andCount := strings.Count(capturedQuery, " AND ")
	assert.GreaterOrEqual(t, andCount, 3, "Should have at least 3 AND operators")
}

// =============================================================================
// PAGINATION TESTS
// =============================================================================

func TestWorkflowExecutions_Pagination(t *testing.T) {
	t.Parallel()

	t.Run("with after cursor", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		var capturedPageToken []byte
		var capturedPageSize int32

		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			capturedPageToken = request.GetNextPageToken()
			capturedPageSize = request.GetPageSize()
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions:    []*workflowpb.WorkflowExecutionInfo{},
				NextPageToken: []byte("next-token-123"),
			}, nil
		}

		// First request without cursor
		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"First": 10,
		})

		assert.Empty(t, capturedPageToken, "First request should have no page token")
		assert.Equal(t, int32(10), capturedPageSize)

		// Second request with cursor (base64 encoded "next-page-token")
		afterCursor := "bmV4dC1wYWdlLXRva2Vu" // base64("next-page-token")
		execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"First": 10,
			"After": afterCursor,
		})

		assert.Equal(t, []byte("next-page-token"), capturedPageToken, "Second request should have page token from cursor")
	})

	t.Run("invalid cursor returns error", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{},
			}, nil
		}

		invalidCursor := "!!!not-valid-base64!!!"
		execErr(te, ctx, workflowExecutions, map[string]any{
			"First": 10,
			"After": invalidCursor,
		}, "invalid pagination cursor")
	})

	t.Run("first limit parameter", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name         string
			first        int
			expectedSize int32
		}{
			{"small page", 5, 5},
			{"medium page", 50, 50},
			{"large page within limit", 500, 500},
			{"max page size", 1000, 1000},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)
				ctx := te.ctx(userA)

				var capturedPageSize int32
				te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
					capturedPageSize = request.GetPageSize()
					return &workflowservice.ListWorkflowExecutionsResponse{
						Executions: []*workflowpb.WorkflowExecutionInfo{},
					}, nil
				}

				execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
					"First": tc.first,
				})

				assert.Equal(t, tc.expectedSize, capturedPageSize, "Page size should match requested first parameter")
			})
		}
	})
}

// =============================================================================
// PAGE INFO TESTS
// =============================================================================

func TestWorkflowExecutions_PageInfo(t *testing.T) {
	t.Parallel()

	t.Run("has next page when NextPageToken present", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{
					{
						Execution: &common.WorkflowExecution{WorkflowId: "workflow-1", RunId: "run-1"},
						Type:      &common.WorkflowType{Name: "TestWorkflow"},
					},
				},
				NextPageToken: []byte("has-more-pages"),
			}, nil
		}

		data := execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"First": 10,
		})

		require.NotNil(t, data.WorkflowExecutions)
		assert.True(t, data.WorkflowExecutions.PageInfo.HasNextPage, "Should have next page when NextPageToken is present")
		assert.NotNil(t, data.WorkflowExecutions.PageInfo.EndCursor, "EndCursor should be set when there are more pages")
	})

	t.Run("no next page when NextPageToken empty", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{
					{
						Execution: &common.WorkflowExecution{WorkflowId: "workflow-1", RunId: "run-1"},
						Type:      &common.WorkflowType{Name: "TestWorkflow"},
					},
				},
				NextPageToken: nil,
			}, nil
		}

		data := execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
			"First": 10,
		})

		require.NotNil(t, data.WorkflowExecutions)
		assert.False(t, data.WorkflowExecutions.PageInfo.HasNextPage, "Should not have next page when NextPageToken is empty")
	})
}

// =============================================================================
// TOTAL COUNT TESTS
// =============================================================================

func TestWorkflowExecutions_TotalCountReturnsPageCount(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{
				{Execution: &common.WorkflowExecution{WorkflowId: "wf-1", RunId: "run-1"}, Type: &common.WorkflowType{Name: "TestWorkflow"}},
				{Execution: &common.WorkflowExecution{WorkflowId: "wf-2", RunId: "run-2"}, Type: &common.WorkflowType{Name: "TestWorkflow"}},
				{Execution: &common.WorkflowExecution{WorkflowId: "wf-3", RunId: "run-3"}, Type: &common.WorkflowType{Name: "TestWorkflow"}},
			},
			NextPageToken: []byte("more-pages"),
		}, nil
	}

	data := execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"First": 10,
	})

	require.NotNil(t, data.WorkflowExecutions)
	assert.Equal(t, 3, data.WorkflowExecutions.TotalCount, "TotalCount should return current page count")
}

// =============================================================================
// ERROR HANDLING TESTS
// =============================================================================

func TestWorkflowExecutions_TemporalError(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return nil, fmt.Errorf("temporal connection failed")
	}

	execErr(te, ctx, workflowExecutions, nil, "temporal connection failed")
}

// =============================================================================
// ORDER BY TESTS
// =============================================================================

func TestWorkflowExecutions_OrderBy(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	data := execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
		"OrderBy": &workflowExecutionOrderInput{
			Field:     "START_TIME",
			Direction: "DESC",
		},
	})

	require.NotNil(t, data.WorkflowExecutions)
}

// =============================================================================
// MULTI-TENANT TESTS
// =============================================================================

func TestWorkflowExecutions_MultiTenantAggregation(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)

	callCount := 0
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		callCount++
		if callCount == 1 {
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflowpb.WorkflowExecutionInfo{
					{Execution: &common.WorkflowExecution{WorkflowId: "tenant1-wf-1", RunId: "run-1"}, Type: &common.WorkflowType{Name: "WorkflowA"}},
				},
			}, nil
		}
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{
				{Execution: &common.WorkflowExecution{WorkflowId: "tenant2-wf-1", RunId: "run-2"}, Type: &common.WorkflowType{Name: "WorkflowB"}},
			},
		}, nil
	}

	// Create multi-tenant context
	multiTenantUser := &authn.User{
		ID:       userA.ID,
		TenantID: tenantA,
		Roles: map[uuid.UUID]authn.Role{
			tenantA: authn.ROLE_ADMIN,
			tenantB: authn.ROLE_ADMIN,
		},
	}
	ctx := te.ctx(multiTenantUser)

	data := execOK[workflowExecutionsData](te, ctx, workflowExecutions, nil)

	require.NotNil(t, data.WorkflowExecutions)
	assert.False(t, data.WorkflowExecutions.PageInfo.HasNextPage,
		"Multi-tenant queries don't support cursor-based pagination")
	assert.False(t, data.WorkflowExecutions.PageInfo.HasPreviousPage,
		"Multi-tenant queries don't support cursor-based pagination")
}

// =============================================================================
// ASSIGNABLE WORKFLOW EXECUTIONS TESTS
// =============================================================================

func TestWorkflowExecutions_IsAssignableFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    *bool
		expected string
	}{
		{
			// true matches explicit-true OR absent-attribute.
			name:     "isAssignable=true (null-matches-true) appended to Temporal query",
			value:    boolPtr(true),
			expected: "(pyck_workflow_is_assignable = true OR pyck_workflow_is_assignable IS NULL)",
		},
		{
			// false matches only explicit-false.
			name:     "isAssignable=false appended to Temporal query",
			value:    boolPtr(false),
			expected: "pyck_workflow_is_assignable = false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := setupWithMockWorkflow(t)
			defer te.Close(t)
			ctx := te.ctx(userA)

			var capturedQuery string
			te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
				capturedQuery = request.GetQuery()
				return &workflowservice.ListWorkflowExecutionsResponse{
					Executions: []*workflowpb.WorkflowExecutionInfo{},
				}, nil
			}

			execOK[workflowExecutionsData](te, ctx, workflowExecutions, map[string]any{
				"Where": &workflowExecutionsWhereInput{IsAssignable: tt.value},
			})

			assert.Equal(t,
				fmt.Sprintf(`pyck_tenant_id IN (%q) AND %s`, userA.TenantID.String(), tt.expected),
				capturedQuery)
		})
	}
}

func TestAssignableWorkflowExecutions_InjectsIsAssignableFilter(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedQuery string
	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedQuery = request.GetQuery()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[assignableWorkflowExecutionsData](te, ctx, assignableWorkflowExecutionsQuery, nil)

	assert.Contains(t, capturedQuery, "(pyck_workflow_is_assignable = true OR pyck_workflow_is_assignable IS NULL)",
		"assignableWorkflowExecutions must inject the null-matches-true is_assignable filter into the Temporal query")
	assert.Contains(t, capturedQuery, fmt.Sprintf(`pyck_tenant_id IN (%q)`, userA.TenantID.String()),
		"tenant filter must still be applied")
}

func TestAssignableWorkflowExecutions_DelegatesToWorkflowExecutions(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{
				{Execution: &common.WorkflowExecution{WorkflowId: "wf-1", RunId: "run-1"}, Type: &common.WorkflowType{Name: "TestWorkflow"}},
				{Execution: &common.WorkflowExecution{WorkflowId: "wf-2", RunId: "run-2"}, Type: &common.WorkflowType{Name: "TestWorkflow"}},
			},
		}, nil
	}

	data := execOK[assignableWorkflowExecutionsData](te, ctx, assignableWorkflowExecutionsQuery, map[string]any{
		"First": 10,
	})

	require.NotNil(t, data.AssignableWorkflowExecutions)
	assert.Equal(t, 2, data.AssignableWorkflowExecutions.TotalCount)
	assert.Len(t, data.AssignableWorkflowExecutions.Edges, 2)
	assert.False(t, data.AssignableWorkflowExecutions.PageInfo.HasNextPage)
}

func TestAssignableWorkflowExecutions_EmptyResult(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	data := execOK[assignableWorkflowExecutionsData](te, ctx, assignableWorkflowExecutionsQuery, nil)

	require.NotNil(t, data.AssignableWorkflowExecutions)
	assert.Equal(t, 0, data.AssignableWorkflowExecutions.TotalCount)
	assert.Empty(t, data.AssignableWorkflowExecutions.Edges)
	assert.False(t, data.AssignableWorkflowExecutions.PageInfo.HasNextPage)
}

// =============================================================================
// WORKFLOW HISTORY TESTS
// =============================================================================

func TestWorkflowHistory_DefaultLimit(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedPageSize int32

	execs := make([]*workflowpb.WorkflowExecutionInfo, 15)
	for i := range execs {
		execs[i] = &workflowpb.WorkflowExecutionInfo{
			Execution: &common.WorkflowExecution{
				WorkflowId: fmt.Sprintf("wf-%d", i),
				RunId:      fmt.Sprintf("run-%d", i),
			},
			Type: &common.WorkflowType{Name: "TestWorkflow"},
		}
	}

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedPageSize = request.GetPageSize()
		count := int(request.GetPageSize())
		if count > len(execs) {
			count = len(execs)
		}
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions:    execs[:count],
			NextPageToken: nil,
		}, nil
	}

	execOK[workflowHistoryData](te, ctx, workflowHistory, map[string]any{
		"Where": workflowHistoryWhereInput{
			TypeName: stringPtr("TestWorkflow"),
		},
	})

	assert.LessOrEqual(t, int(capturedPageSize), 10,
		"Default limit should request at most 10 executions")
}

func TestWorkflowHistory_CustomLimit(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedPageSize int32

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedPageSize = request.GetPageSize()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[workflowHistoryData](te, ctx, workflowHistory, map[string]any{
		"Where": workflowHistoryWhereInput{
			TypeName: stringPtr("TestWorkflow"),
		},
		"Limit": 25,
	})

	assert.LessOrEqual(t, int(capturedPageSize), 25,
		"Custom limit should request at most 25 executions")
}

func TestWorkflowHistory_MaxLimitCapped(t *testing.T) {
	t.Parallel()
	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var capturedPageSize int32

	te.MockTemporalClient.ListWorkflowFunc = func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		capturedPageSize = request.GetPageSize()
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{},
		}, nil
	}

	execOK[workflowHistoryData](te, ctx, workflowHistory, map[string]any{
		"Where": workflowHistoryWhereInput{
			TypeName: stringPtr("TestWorkflow"),
		},
		"Limit": 500,
	})

	assert.LessOrEqual(t, int(capturedPageSize), 100,
		"Limit should be capped at max 100 executions")
}
