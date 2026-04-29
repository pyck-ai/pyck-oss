package resolvers

import (
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/workflow/model"
)

// graphqlTargetsToCommon converts the GraphQL enum slice to the common workflow
// enum slice the SDK expects. The two enums share string values so this is just
// a typed reinterpretation; an unknown value (which the GraphQL layer should
// have rejected) is reported as an error rather than silently dropped.
//
// Kept in a non-resolvers file so gqlgen's regeneration does not move it into
// a trailing warning block — gqlgen only rewrites *.resolvers.go.
func graphqlTargetsToCommon(in []model.WorkflowTarget) ([]common_workflow.WorkflowTarget, error) {
	out := make([]common_workflow.WorkflowTarget, 0, len(in))
	for _, t := range in {
		v, err := common_workflow.WorkflowTargetString(t.String())
		if err != nil {
			return nil, fmt.Errorf("invalid workflow target %q: %w", t.String(), err)
		}
		out = append(out, v)
	}
	return out, nil
}

// decodeTargetsFromSearchAttributes pulls the pyck_workflow_targets KeywordList
// out of an execution's IndexedFields and converts it to the GraphQL enum.
// Returns an empty slice (not nil) so the response always has a stable shape.
func decodeTargetsFromSearchAttributes(fields map[string]*commonpb.Payload) []model.WorkflowTarget {
	payload, ok := fields[common_workflow.PyckWorkflowTargetsKey]
	if !ok || payload == nil {
		return []model.WorkflowTarget{}
	}

	var values []string
	if err := converter.GetDefaultDataConverter().FromPayload(payload, &values); err != nil {
		return []model.WorkflowTarget{}
	}

	out := make([]model.WorkflowTarget, 0, len(values))
	for _, v := range values {
		t := model.WorkflowTarget(v)
		if t.IsValid() {
			out = append(out, t)
		}
	}
	return out
}
