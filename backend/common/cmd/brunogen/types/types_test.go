package types_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

// TestAssertionKeysCoverSchema fails when the test schema accepts an
// assertion operator the parser does not recognise: such an assertion passes
// schema validation but is parsed without an operator.
func TestAssertionKeysCoverSchema(t *testing.T) {
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
		assert.True(t, slices.Contains(types.AssertionKeys, op), "schema operator %q missing from assertionKeys", op)
	}
}

func TestAssertionUnmarshalRejectsUnknownOperator(t *testing.T) {
	t.Parallel()

	var a types.Assertion
	err := yaml.Unmarshal([]byte("ref: res.body.data.id\nnotAThing: true\n"), &a)
	assert.ErrorIs(t, err, types.ErrUnknownAssertion)
}

func TestAssertionUnmarshalNegatedPresence(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"notNil", "notEmpty"} {
		var a types.Assertion
		require.NoError(t, yaml.Unmarshal([]byte("ref: res.body.data.id\n"+op+": true\n"), &a))
		assert.Equal(t, op, a.Test)
		assert.Equal(t, true, a.Args)
	}
}
