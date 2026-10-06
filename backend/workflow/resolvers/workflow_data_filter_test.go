package resolvers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
)

var queryWorkflowsWhere = resolver.ParseTemplate(`query {
	workflows(where: {{.Where}}) { totalCount }
}`)

// TestWorkflowDataFilterRefusesUnusableShapes pins that a JSON data filter
// whose list length its operator cannot use, or an empty DataHasKey path, is
// refused. Before, the resolver added no predicate and the caller got every
// workflow of the tenant.
func TestWorkflowDataFilterRefusesUnusableShapes(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })
	ctx := te.ctx(userA)
	for _, name := range []string{"first", "second"} {
		execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
			"Name": "wf_filter_" + name, "TaskQueue": "q", "DataTypeID": itemDataTypeID,
			"DataName": name, "DataWeight": 1,
		})
	}

	wellFormed := map[string]int{
		`{ Data: ["meta.name", "first"] }`:             1,
		`{ DataIn: ["meta.name", "first", "second"] }`: 2,
		`{ DataContains: ["meta.tags", "test"] }`:      2,
		`{ DataHasKey: "meta.name" }`:                  2,
	}
	for where, want := range wellFormed {
		data := execOK[queryWorkflowsData](te, ctx, queryWorkflowsWhere, map[string]any{"Where": where})
		assert.Equal(t, want, data.Workflows.TotalCount, "well-formed %s", where)
	}

	for _, where := range []string{
		`{ Data: ["meta.name"] }`,
		`{ Data: [] }`,
		`{ Data: ["meta.name", "first", "surplus"] }`,
		`{ DataIn: ["meta.name"] }`,
		`{ DataIn: [] }`,
		`{ DataContains: ["meta.tags"] }`,
		`{ DataContains: ["meta.tags", "a", "b"] }`,
		`{ DataHasKey: "" }`,
	} {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			res := resolver.Exec[queryWorkflowsData, *ent.Client](te.TestEnvironment, ctx, queryWorkflowsWhere, map[string]any{"Where": where})
			require.NotEmpty(t, res.Errors, "the filter was dropped: %d workflows returned", res.Data.Workflows.TotalCount)
			assert.Contains(t, res.Errors[0].Message, "invalid JSON data filter")
			assert.Zero(t, res.Data.Workflows.TotalCount)
		})
	}
}
