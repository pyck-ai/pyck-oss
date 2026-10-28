package gen_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gen "github.com/pyck-ai/pyck/backend/common/cmd/brunogen/internal"
)

// TestChaiChainCoversSchema fails when the test schema accepts an assertion
// operator that chaiChain cannot render: such an assertion passes schema
// validation but is generated as a comment and checks nothing.
func TestChaiChainCoversSchema(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../api/schemas/test.schema.json")
	require.NoError(t, err)

	var schema struct {
		Defs struct {
			Assertion struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"Assertion"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.NotEmpty(t, schema.Defs.Assertion.Properties)

	for op := range schema.Defs.Assertion.Properties {
		if op == "ref" {
			continue
		}
		assert.NotContains(t, gen.ChaiChain(op, true, ""), "UNSUPPORTED", "operator %q", op)
	}
}

func TestChaiChainNegatedPresenceOperators(t *testing.T) {
	t.Parallel()

	cases := []struct {
		op   string
		args any
		want string
	}{
		{"notNil", true, ".to.exist"},
		{"notNil", false, ".to.not.exist"},
		{"notEmpty", true, ".to.not.be.empty"},
		{"notEmpty", nil, ".to.not.be.empty"},
		{"notEmpty", false, ".to.be.empty"},
	}
	for _, tc := range cases {
		got := gen.ChaiChain(tc.op, tc.args, "")
		assert.Equal(t, tc.want, got, "%s: %v", tc.op, tc.args)
		assert.NotContains(t, got, "UNSUPPORTED")
	}
}
