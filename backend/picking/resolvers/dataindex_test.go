//nolint:testpackage // exercises overlapsPredicate, which is unexported
package resolvers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"entgo.io/ent/dialect/sql"
	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/dataindex"
	common_jsonschema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/picking/model"
)

func TestOverlapsPredicateBindsOneParameter(t *testing.T) {
	t.Parallel()

	values := make([]string, 70000)
	for i := range values {
		values[i] = "SN-" + strings.Repeat("x", 3)
	}
	p, err := overlapsPredicate("data_ix_list1", values)
	require.NoError(t, err)

	selector := sql.Dialect("postgres").Select("id").From(sql.Table("orders"))
	p(selector)
	query, args := selector.Query()

	// One bind parameter regardless of candidate count: a statement cannot carry
	// more than 65535, and callers legitimately name thousands.
	assert.Len(t, args, 1)
	assert.Contains(t, query, "?| ARRAY(SELECT jsonb_array_elements_text(")
}

func TestOverlapsPredicateEncodesEmptyAsArray(t *testing.T) {
	t.Parallel()

	// A nil slice must not marshal to `null`: jsonb_array_elements_text rejects a
	// scalar, so the query would error instead of simply matching nothing.
	for name, values := range map[string][]string{"nil": nil, "empty": {}} {
		p, err := overlapsPredicate("data_ix_list1", values)
		require.NoError(t, err, name)

		selector := sql.Dialect("postgres").Select("id").From(sql.Table("orders"))
		p(selector)
		_, args := selector.Query()
		require.Len(t, args, 1, name)
		assert.Equal(t, "[]", args[0], name)
	}
}

func TestDataIndexPredicateOperatorCount(t *testing.T) {
	t.Parallel()
	ptr := func(s string) *string { return &s }

	t.Run("two operators are rejected, not silently narrowed", func(t *testing.T) {
		t.Parallel()
		_, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_text1"},
			&model.DataIndexWhereInput{Eq: ptr("A"), Gt: ptr("B")})
		require.ErrorIs(t, err, errNoDataIndexPredicate)
	})

	t.Run("zero operators are rejected", func(t *testing.T) {
		t.Parallel()
		_, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_text1"}, &model.DataIndexWhereInput{})
		require.ErrorIs(t, err, errNoDataIndexPredicate)
	})

	t.Run("overlaps plus a scalar operator is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_list1"},
			&model.DataIndexWhereInput{Overlaps: []string{"X"}, Eq: ptr("A")})
		require.ErrorIs(t, err, errNoDataIndexPredicate)
	})

	t.Run("exactly one operator compiles", func(t *testing.T) {
		t.Parallel()
		p, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_text1"}, &model.DataIndexWhereInput{Eq: ptr("A")})
		require.NoError(t, err)
		require.NotNil(t, p)
	})
}

func TestDataIndexPredicateTypesOperands(t *testing.T) {
	t.Parallel()
	ptr := func(s string) *string { return &s }

	// argOf compiles the predicate and returns the single bound argument.
	argOf := func(t *testing.T, b dataindex.Binding, w *model.DataIndexWhereInput) any {
		t.Helper()
		p, err := dataIndexPredicate(b, w)
		require.NoError(t, err)
		sel := sql.Dialect("postgres").Select("id").From(sql.Table("orders"))
		p(sel)
		_, args := sel.Query()
		require.Len(t, args, 1)
		return args[0]
	}

	t.Run("numeric eq binds a float, not a string", func(t *testing.T) {
		t.Parallel()
		// A string operand would send text to a double precision column and error.
		got := argOf(t, dataindex.Binding{Slot: "data_ix_numeric1"}, &model.DataIndexWhereInput{Eq: ptr("5")})
		assert.InDelta(t, float64(5), got, 0)
	})

	t.Run("bool eq compiles to a boolean predicate, not a text compare", func(t *testing.T) {
		t.Parallel()
		// ent renders a typed bool as `col` / `NOT col`; a string operand would have
		// produced `col = 'true'`, which a boolean column has no operator for.
		query := func(v string) string {
			p, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_bool1"}, &model.DataIndexWhereInput{Eq: ptr(v)})
			require.NoError(t, err)
			sel := sql.Dialect("postgres").Select("id").From(sql.Table("orders"))
			p(sel)
			q, args := sel.Query()
			assert.Empty(t, args)
			return q
		}
		assert.Contains(t, query("true"), `"data_ix_bool1"`)
		assert.Contains(t, query("false"), `NOT "orders"."data_ix_bool1"`)
	})

	t.Run("text eq keeps the string", func(t *testing.T) {
		t.Parallel()
		got := argOf(t, dataindex.Binding{Slot: "data_ix_text1"}, &model.DataIndexWhereInput{Eq: ptr("ACME")})
		assert.Equal(t, "ACME", got)
	})

	t.Run("a non-numeric operand on a numeric slot is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_numeric1"},
			&model.DataIndexWhereInput{Eq: ptr("not-a-number")})
		require.ErrorIs(t, err, errBadOperand)
	})

	t.Run("contains on a numeric slot is rejected, not compiled to LIKE", func(t *testing.T) {
		t.Parallel()
		_, err := dataIndexPredicate(dataindex.Binding{Slot: "data_ix_numeric1"},
			&model.DataIndexWhereInput{Contains: ptr("5")})
		require.ErrorIs(t, err, errOperatorSlotMismatch)
	})
}

// stubDataTypeReader serves one datatype schema for the resolver's validator.
type stubDataTypeReader struct{ schema string }

func (s stubDataTypeReader) ReadBySlug(context.Context, string) (*common_jsonschema.DataType, error) {
	return &common_jsonschema.DataType{JsonSchema: s.schema}, nil
}

func (s stubDataTypeReader) ReadByID(context.Context, uuid.UUID) (*common_jsonschema.DataType, error) {
	return &common_jsonschema.DataType{JsonSchema: s.schema}, nil
}

func TestDataIndexPredicatesScopesToDatatype(t *testing.T) {
	t.Parallel()
	// The scope predicate is the fix for the shared-slot read bug: a slot column is
	// shared by every datatype, so the query must also filter by datatype.
	v := validator.NewValidator(stubDataTypeReader{
		schema: `{"x-indices":{"serialNumbers":{"source":"/MainItemSerialNumber","slot":"data_ix_list1"}}}`,
	})
	r := &pickingOrderWhereInputResolver{&Resolver{validator: v}}

	predicates, err := r.dataIndexPredicates(tenantCtx(), &model.DataIndexWhereInput{
		DataType: "hellmann-default-pickingorder",
		Field:    "serialNumbers",
		Overlaps: []string{"SN-1"},
	})
	require.NoError(t, err)
	require.Len(t, predicates, 2, "the slot predicate and the datatype scope")

	sel := sql.Dialect("postgres").Select("id").From(sql.Table("orders"))
	for _, p := range predicates {
		p(sel)
	}
	query, args := sel.Query()
	// Both conditions must be present and ANDed into the same WHERE.
	assert.Contains(t, query, "data_ix_list1")
	assert.Contains(t, query, `"data_type_slug" = $`)
	assert.Contains(t, query, " AND ", "the two predicates are ANDed")
	assert.Contains(t, args, "hellmann-default-pickingorder", "the scope binds the requested slug")
}

// TestDataIndexPredicatesRejectsSlotOutsidePool pins the slot re-check on the read
// path. The slot is spliced into SQL as a column identifier and reaches us from
// another service's datatype row, so it cannot be trusted here: ent writes an
// identifier containing a quote character verbatim, which would make a crafted
// slot an injection point rather than a lookup failure.
func TestDataIndexPredicatesRejectsSlotOutsidePool(t *testing.T) {
	t.Parallel()

	for name, slot := range map[string]string{
		"outside the pool": "data_ix_text9",
		"quote breakout":   `data_ix_text1" = '' OR "1`,
		"not a slot":       "tenant_id",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			schema, err := json.Marshal(map[string]any{
				"x-indices": map[string]any{"serials": map[string]string{"source": "/S", "slot": slot}},
			})
			require.NoError(t, err)

			r := &pickingOrderWhereInputResolver{&Resolver{
				validator: validator.NewValidator(stubDataTypeReader{schema: string(schema)}),
			}}
			operand := "x"
			_, err = r.dataIndexPredicates(tenantCtx(), &model.DataIndexWhereInput{
				DataType: "hellmann-default-pickingorder",
				Field:    "serials",
				Eq:       &operand,
			})
			require.ErrorIs(t, err, dataindex.ErrUnknownSlot)
		})
	}
}

// tenantCtx carries the single tenant resolveBinding needs: the datatype cache
// is keyed per tenant and refuses a request that names several or "all".
func tenantCtx() context.Context {
	tenantID := uuid.New()
	return request.Context(context.Background(), &authn.User{ID: uuid.New(), TenantID: tenantID}, tenantID)
}

// TestDataIndexRejectedUnderNot guards the scope: entgql negates the enclosing
// input's whole predicate set, so NOT(slot AND slug) would widen the query to
// every other datatype's rows instead of narrowing it.
func TestDataIndexRejectedUnderNot(t *testing.T) {
	t.Parallel()

	r := &pickingOrderWhereInputResolver{&Resolver{}}
	ctx := graphql.WithPathContext(tenantCtx(), graphql.NewPathWithField("not"))
	operand := "x"

	_, err := r.dataIndexPredicates(ctx, &model.DataIndexWhereInput{
		DataType: "hellmann-default-pickingorder", Field: "serials", Eq: &operand,
	})
	require.ErrorIs(t, err, errDataIndexNegated)
}

// TestEmptyListOperatorMatchesNothing: an explicitly empty candidate list is a
// legitimate query (nothing scanned yet), and every other empty-list filter
// yields an empty result rather than an error.
func TestEmptyListOperatorMatchesNothing(t *testing.T) {
	t.Parallel()

	binding := dataindex.Binding{Name: "serials", Slot: "data_ix_list1"}
	p, err := dataIndexPredicate(binding, &model.DataIndexWhereInput{
		DataType: "hellmann-default-pickingorder", Field: "serials", Overlaps: []string{},
	})
	require.NoError(t, err)
	require.NotNil(t, p)

	// Omitted stays an error: no operator at all is a malformed filter.
	_, err = dataIndexPredicate(binding, &model.DataIndexWhereInput{
		DataType: "hellmann-default-pickingorder", Field: "serials",
	})
	require.ErrorIs(t, err, errNoDataIndexPredicate)
}
