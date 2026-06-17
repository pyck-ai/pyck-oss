package idempotency_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

// mustChecksum is a t.Helper that fails the test if checksumming errors.
// Production code rejects with 400 INVALID_VARIABLES; tests pass clean
// inputs and treat an error as a test bug. It builds the operation the same
// way mutationOpCtx does (see precheck_test.go) so the checksum a test
// expects matches the one PreCheck derives from the same operation.
func mustChecksum(t *testing.T, name string, fields []string, vars map[string]any) [32]byte {
	t.Helper()
	c, err := idempotency.OperationChecksum(mutationOp(name, fields), nil, vars)
	require.NoError(t, err)
	return c
}

func TestOperationChecksum_StableAcrossVarOrder(t *testing.T) {
	t.Parallel()

	fields := []string{"createItem"}
	a := mustChecksum(t, "CreateItem", fields, map[string]any{
		"name":  "Widget",
		"sku":   "W-1",
		"price": 12.50,
	})

	b := mustChecksum(t, "CreateItem", fields, map[string]any{
		"price": 12.50,
		"sku":   "W-1",
		"name":  "Widget",
	})

	assert.Equal(t, a, b, "checksum must be independent of map key order")
}

func TestOperationChecksum_StableForNestedMaps(t *testing.T) {
	t.Parallel()

	fields := []string{"createOrder"}
	a := mustChecksum(t, "CreateOrder", fields, map[string]any{
		"input": map[string]any{
			"customerId": "c-1",
			"lineItems": []any{
				map[string]any{"sku": "A", "qty": 1},
				map[string]any{"qty": 2, "sku": "B"},
			},
		},
	})

	b := mustChecksum(t, "CreateOrder", fields, map[string]any{
		"input": map[string]any{
			"lineItems": []any{
				map[string]any{"qty": 1, "sku": "A"},
				map[string]any{"sku": "B", "qty": 2},
			},
			"customerId": "c-1",
		},
	})

	assert.Equal(t, a, b)
}

func TestOperationChecksum_ChangesWithOperationName(t *testing.T) {
	t.Parallel()

	vars := map[string]any{"id": "x"}
	a := mustChecksum(t, "CreateItem", []string{"createItem"}, vars)
	b := mustChecksum(t, "UpdateItem", []string{"createItem"}, vars)

	assert.NotEqual(t, a, b)
}

func TestOperationChecksum_ChangesWithVarValues(t *testing.T) {
	t.Parallel()

	fields := []string{"createItem"}
	a := mustChecksum(t, "CreateItem", fields, map[string]any{"name": "A"})
	b := mustChecksum(t, "CreateItem", fields, map[string]any{"name": "B"})

	assert.NotEqual(t, a, b)
}

// TestOperationChecksum_ChangesWithSelectionSet covers the PR-review finding
// that the checksum must include the selection set: two requests with the
// same operation name and variables but a different selection set must NOT
// collide, or a replay would return a body shaped for the wrong fields.
func TestOperationChecksum_ChangesWithSelectionSet(t *testing.T) {
	t.Parallel()

	vars := map[string]any{"name": "A"}
	a := mustChecksum(t, "CreateItem", []string{"createItem"}, vars)
	b := mustChecksum(t, "CreateItem", []string{"createItemDifferentField"}, vars)

	assert.NotEqual(t, a, b, "different selection set must change the checksum")
}

// TestOperationChecksum_DefaultMatchesExplicit covers the PR-review finding
// that canonicalization must be semantic for variables: a request that omits
// a variable carrying a schema default and a retry that supplies the same
// value explicitly must produce the same checksum (no false 422).
func TestOperationChecksum_DefaultMatchesExplicit(t *testing.T) {
	t.Parallel()

	op := &ast.OperationDefinition{
		Operation: ast.Mutation,
		Name:      "CreateItem",
		VariableDefinitions: ast.VariableDefinitionList{
			{
				Variable:     "limit",
				Type:         ast.NamedType("Int", nil),
				DefaultValue: &ast.Value{Raw: "10", Kind: ast.IntValue},
			},
		},
		SelectionSet: ast.SelectionSet{&ast.Field{Name: "createItem"}},
	}

	omitted, err := idempotency.OperationChecksum(op, nil, map[string]any{})
	require.NoError(t, err)
	explicit, err := idempotency.OperationChecksum(op, nil, map[string]any{"limit": 10})
	require.NoError(t, err)

	assert.Equal(t, omitted, explicit,
		"omitting a defaulted variable must match supplying it explicitly")
}

func TestOperationChecksum_DistinguishesNameValueBoundary(t *testing.T) {
	t.Parallel()

	// Without the unit-separator byte between name and the rest, these two
	// could hash identically (the bytes "foo" + "bar" appear in both
	// concatenations).
	fields := []string{"f"}
	a := mustChecksum(t, "foo", fields, map[string]any{"x": "bar"})
	b := mustChecksum(t, "foobar", fields, map[string]any{"x": ""})

	assert.NotEqual(t, a, b)
}

func TestOperationChecksum_NilVars(t *testing.T) {
	t.Parallel()

	fields := []string{"createItem"}
	a := mustChecksum(t, "CreateItem", fields, nil)
	b := mustChecksum(t, "CreateItem", fields, nil)
	c := mustChecksum(t, "CreateItem", fields, map[string]any{})

	assert.Equal(t, a, b)
	// Empty map and nil canonicalize differently by design (nil encodes to
	// null, empty map to {}), keeping the checksum predictable.
	assert.NotEqual(t, a, c, "nil and empty map produce different canonical encodings")
}

func TestOperationChecksum_NonMarshalableVariable_Errors(t *testing.T) {
	t.Parallel()

	// A channel value can't be JSON-marshaled, so it must surface as an
	// error rather than silently falling back to a sentinel checksum
	// (which previously made every bad payload look identical).
	_, err := idempotency.OperationChecksum(
		mutationOp("CreateItem", []string{"createItem"}), nil,
		map[string]any{"bad": make(chan int)},
	)
	require.Error(t, err)
}

func TestOperationChecksum_NilOperation_Errors(t *testing.T) {
	t.Parallel()

	_, err := idempotency.OperationChecksum(nil, nil, nil)
	require.Error(t, err)
}
