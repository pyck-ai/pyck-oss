package mixin

import (
	"fmt"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// Slot kinds. A slot's kind is encoded in its column name, so the name alone
// tells the projector how to coerce a value and the resolver which operators
// apply.
const (
	// SlotPrefix precedes every slot column, e.g. data_ix_text1.
	SlotPrefix = "data_ix_"

	SlotKindText    = "text"
	SlotKindNumeric = "numeric"
	SlotKindBool    = "bool"
	SlotKindList    = "list"
)

// DataIndexMixin gives an entity a pool of typed columns that the platform
// projects data into, so a runtime-defined key inside data can be looked up by
// an index instead of a sequential scan.
//
// A predicate that extracts a path (data->>'key') cannot use an index over the
// data column, which is why the keys are projected out to real columns. The
// datatype's x-indices block binds a payload path to a slot; the projection hook
// writes the slot in the same transaction as the row, so the slot cannot drift
// from data and callers cannot set it.
//
// Pool sizes are per entity -- allocate what it plausibly needs. Widening a pool
// later is an additive migration; rebinding an occupied slot is not (see the
// datatype validation).
type DataIndexMixin struct {
	mixin.Schema

	Text    int
	Numeric int
	Bool    int
	List    int
}

// SlotName returns the column backing the nth slot of a kind, 1-based.
func SlotName(kind string, n int) string {
	return fmt.Sprintf("%s%s%d", SlotPrefix, kind, n)
}

// Fields returns the entity's pool of typed slot columns, one per declared
// count and kind. They are hidden from the GraphQL mutation inputs: the
// projection hook is their only writer, which is what keeps the index from
// drifting away from data.
func (m DataIndexMixin) Fields() []ent.Field {
	// Slots are server-owned: hidden from the type, from where-inputs (queries go
	// through the semantic dataIndex filter instead), and from mutation inputs.
	hide := entgql.Skip(
		entgql.SkipType |
			entgql.SkipWhereInput |
			entgql.SkipMutationCreateInput |
			entgql.SkipMutationUpdateInput,
	)

	fields := make([]ent.Field, 0, m.Text+m.Numeric+m.Bool+m.List)
	for i := 1; i <= m.Text; i++ {
		fields = append(fields, field.String(SlotName(SlotKindText, i)).
			Optional().Nillable().Annotations(hide))
	}
	for i := 1; i <= m.Numeric; i++ {
		fields = append(fields, field.Float(SlotName(SlotKindNumeric, i)).
			Optional().Nillable().Annotations(hide))
	}
	for i := 1; i <= m.Bool; i++ {
		fields = append(fields, field.Bool(SlotName(SlotKindBool, i)).
			Optional().Nillable().Annotations(hide))
	}
	for i := 1; i <= m.List; i++ {
		fields = append(fields, field.JSON(SlotName(SlotKindList, i), []string{}).
			Optional().Annotations(hide))
	}
	return fields
}

// Indexes returns one index per slot: GIN for the list slots, which are queried
// with the array-overlap operator, and a partial btree for the scalars. The
// partial predicate keeps the index to the rows a binding actually projected.
func (m DataIndexMixin) Indexes() []ent.Index {
	indexes := make([]ent.Index, 0, m.Text+m.Numeric+m.Bool+m.List)

	// Scalar slots: tenant-scoped btree, partial so an unbound slot costs nothing.
	for _, kind := range []struct {
		name string
		n    int
	}{
		{SlotKindText, m.Text},
		{SlotKindNumeric, m.Numeric},
		{SlotKindBool, m.Bool},
	} {
		for i := 1; i <= kind.n; i++ {
			col := SlotName(kind.name, i)
			indexes = append(indexes, index.Fields(TenantFieldTenantID, col).
				Annotations(entsql.IndexWhere(col+" IS NOT NULL")))
		}
	}

	// List slots: GIN with the default jsonb_ops class, which is what serves the
	// ?| overlap operator (jsonb_path_ops does not).
	for i := 1; i <= m.List; i++ {
		indexes = append(indexes, index.Fields(SlotName(SlotKindList, i)).
			Annotations(entsql.IndexTypes(map[string]string{dialect.Postgres: "GIN"})))
	}
	return indexes
}
