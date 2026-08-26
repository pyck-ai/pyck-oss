package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"entgo.io/ent/dialect/sql"
	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
	commonmixin "github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/request"

	entorder "github.com/pyck-ai/pyck/backend/picking/ent/gen/order"
	entpredicate "github.com/pyck-ai/pyck/backend/picking/ent/gen/predicate"
	"github.com/pyck-ai/pyck/backend/picking/model"
)

var (
	errNoDataIndexPredicate = errors.New("dataIndex needs exactly one operator")
	errOperatorSlotMismatch = errors.New("operator does not apply to this index")
	errBadOperand           = errors.New("operand is not valid for this index")
	errDataIndexMultiTenant = errors.New("dataIndex needs exactly one tenant; it resolves the datatype per tenant")
	errDataIndexNegated     = errors.New("dataIndex cannot be used inside not; negate the operand instead (neq, notIn)")
)

// dataIndexPredicates compiles a dataIndex filter into the operator against the
// slot AND the datatype scope. The scope is not optional: a slot is one physical
// column shared by every datatype of the entity, so the slot predicate alone
// would also match rows of another datatype binding it to a different key. It
// scopes by slug -- version-stable, unlike data_type_id, and what the backfill
// uses.
func (r *pickingOrderWhereInputResolver) dataIndexPredicates(ctx context.Context, data *model.DataIndexWhereInput) ([]entpredicate.Order, error) {
	// Under not: entgql negates the enclosing input's whole predicate set, so
	// NOT(slot AND slug = X) widens to (NOT slot) OR slug <> X -- every other
	// datatype's rows. The scope cannot stay conjunctive from in here.
	if negatedPath(ctx) {
		return nil, errDataIndexNegated
	}
	binding, err := r.resolveBinding(ctx, data.DataType, data.Field)
	if err != nil {
		return nil, err
	}
	slotPredicate, err := dataIndexPredicate(binding, data)
	if err != nil {
		return nil, err
	}
	return []entpredicate.Order{slotPredicate, entorder.DataTypeSlugEQ(data.DataType)}, nil
}

// resolveBinding maps a caller-facing index name to its binding, using the
// datatype that declares it. The binding carries the slot and its kind, which the
// predicate needs to coerce the operand to the column's type.
//
// The slot is re-checked against the pool because it ends up spliced into SQL as
// a column identifier and arrives from another service's datatype row: ent writes
// an identifier containing a quote character verbatim. The backfill re-checks for
// the same reason (quoteIdent).
func (r *pickingOrderWhereInputResolver) resolveBinding(ctx context.Context, dataTypeSlug, field string) (dataindex.Binding, error) {
	// ReadBySlug keys the datatype cache by a single tenant and panics otherwise.
	// Queries may carry several tenant IDs or "all", which every other filter
	// serves, so refuse explicitly instead of taking the process down.
	if !request.ForContext(ctx).HasMutationTenantID() {
		return dataindex.Binding{}, errDataIndexMultiTenant
	}
	dt, err := r.validator.ReadBySlug(ctx, dataTypeSlug)
	if err != nil {
		return dataindex.Binding{}, fmt.Errorf("datatype %q: %w", dataTypeSlug, err)
	}
	bindings, err := dataindex.ParseCached(dt.JsonSchema)
	if err != nil {
		return dataindex.Binding{}, err
	}
	binding, ok := bindings[field]
	if !ok {
		return dataindex.Binding{}, fmt.Errorf("index %q: %w", field, dataindex.ErrUnknownIndex)
	}
	binding.Name = field
	pool, hasPool := dataindex.PoolFor(dataindex.EntityPickingOrder)
	if !hasPool || !pool.Has(binding.Slot) {
		return dataindex.Binding{}, fmt.Errorf("index %q: %w %q", field, dataindex.ErrUnknownSlot, binding.Slot)
	}
	return binding, nil
}

// dataIndexPredicate compiles the requested operator against the slot. The
// operator is checked against the slot's kind, so an overlap on a scalar (or an
// equality on a list) fails at the query instead of quietly matching nothing.
//
// Exactly one operator must be set: taking the first of several would silently
// drop the rest, answering a query more constrained than the one that ran.
func dataIndexPredicate(b dataindex.Binding, w *model.DataIndexWhereInput) (entpredicate.Order, error) {
	if operatorCount(w) != 1 {
		return nil, errNoDataIndexPredicate
	}
	slot, kind := b.Slot, b.Kind()

	if w.Overlaps != nil {
		if kind != commonmixin.SlotKindList {
			return nil, fmt.Errorf("%w: overlaps needs a list index, %q is not", errOperatorSlotMismatch, w.Field)
		}
		return overlapsPredicate(slot, w.Overlaps)
	}
	if kind == commonmixin.SlotKindList {
		return nil, fmt.Errorf("%w: %q is a list index, use overlaps", errOperatorSlotMismatch, w.Field)
	}

	// LIKE compiles to ~~, which no numeric or boolean column has an operator for.
	switch {
	case w.Contains != nil:
		if kind != commonmixin.SlotKindText {
			return nil, fmt.Errorf("%w: contains needs a text index, %q is %s", errOperatorSlotMismatch, w.Field, kind)
		}
		return entpredicate.Order(sql.FieldContains(slot, *w.Contains)), nil
	case w.HasPrefix != nil:
		if kind != commonmixin.SlotKindText {
			return nil, fmt.Errorf("%w: hasPrefix needs a text index, %q is %s", errOperatorSlotMismatch, w.Field, kind)
		}
		return entpredicate.Order(sql.FieldHasPrefix(slot, *w.HasPrefix)), nil
	case w.HasSuffix != nil:
		if kind != commonmixin.SlotKindText {
			return nil, fmt.Errorf("%w: hasSuffix needs a text index, %q is %s", errOperatorSlotMismatch, w.Field, kind)
		}
		return entpredicate.Order(sql.FieldHasSuffix(slot, *w.HasSuffix)), nil
	}

	// The operand arrives as a string but the column is typed; coerce it so
	// Postgres compares like against like instead of erroring on text.
	switch {
	case w.Eq != nil:
		return typedScalar(slot, kind, *w.Eq, sql.FieldEQ)
	case w.Neq != nil:
		return typedScalar(slot, kind, *w.Neq, sql.FieldNEQ)
	case w.Gt != nil:
		return typedScalar(slot, kind, *w.Gt, sql.FieldGT)
	case w.Gte != nil:
		return typedScalar(slot, kind, *w.Gte, sql.FieldGTE)
	case w.Lt != nil:
		return typedScalar(slot, kind, *w.Lt, sql.FieldLT)
	case w.Lte != nil:
		return typedScalar(slot, kind, *w.Lte, sql.FieldLTE)
	case w.In != nil:
		return typedIn(slot, kind, w.In, false)
	case w.NotIn != nil:
		return typedIn(slot, kind, w.NotIn, true)
	}
	return nil, errNoDataIndexPredicate
}

// coerceOperand parses a string operand into the Go type the slot's column holds,
// erroring on input the column could not accept.
func coerceOperand(kind, operand string) (any, error) {
	switch kind {
	case commonmixin.SlotKindNumeric:
		f, err := strconv.ParseFloat(operand, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a number", errBadOperand, operand)
		}
		return f, nil
	case commonmixin.SlotKindBool:
		v, err := strconv.ParseBool(operand)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a boolean", errBadOperand, operand)
		}
		return v, nil
	default:
		return operand, nil
	}
}

// typedScalar coerces the operand and builds a single-value predicate with it.
func typedScalar(slot, kind, operand string, field func(string, any) func(*sql.Selector)) (entpredicate.Order, error) {
	v, err := coerceOperand(kind, operand)
	if err != nil {
		return nil, err
	}
	return entpredicate.Order(field(slot, v)), nil
}

// typedIn coerces every operand and builds an IN / NOT IN predicate.
func typedIn(slot, kind string, operands []string, negate bool) (entpredicate.Order, error) {
	values := make([]any, len(operands))
	for i, operand := range operands {
		v, err := coerceOperand(kind, operand)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	if negate {
		return entpredicate.Order(sql.FieldNotIn(slot, values...)), nil
	}
	return entpredicate.Order(sql.FieldIn(slot, values...)), nil
}

// operatorCount reports how many of the mutually exclusive operators are set.
func operatorCount(w *model.DataIndexWhereInput) int {
	n := 0
	for _, set := range []bool{
		w.Eq != nil, w.Neq != nil, w.In != nil, w.NotIn != nil,
		w.Gt != nil, w.Gte != nil, w.Lt != nil, w.Lte != nil,
		w.Contains != nil, w.HasPrefix != nil, w.HasSuffix != nil,
		w.Overlaps != nil,
	} {
		if set {
			n++
		}
	}
	return n
}

// overlapsPredicate is hand-rolled because ent has no JSONB array overlap: `?|`
// resolves the whole array in one index lookup where an OR chain probes per
// value. The candidates travel as a single JSON parameter, not one bind each --
// callers legitimately name thousands and a statement carries at most 65535.
func overlapsPredicate(slot string, values []string) (entpredicate.Order, error) {
	if values == nil {
		// A nil slice marshals to `null`, which jsonb_array_elements_text rejects.
		values = []string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode overlap values: %w", err)
	}
	return func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.Ident(s.C(slot)).
				WriteString(" ?| ARRAY(SELECT jsonb_array_elements_text(").
				Arg(string(encoded)).
				WriteString("::jsonb))")
		}))
	}, nil
}

// negatedPath reports whether the dataIndex filter sits under a not: anywhere in
// the input tree. gqlgen records each input field it descends into, so the whole
// traversal is visible here.
func negatedPath(ctx context.Context) bool {
	for _, elem := range graphql.GetPath(ctx) {
		if name, ok := elem.(ast.PathName); ok && name == "not" {
			return true
		}
	}
	return false
}
