package mixin

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

var (
	DataFieldDataTypeID   = "data_type_id"
	DataFieldDataTypeSlug = "data_type_slug"
	DataFieldData         = "data"
)

// DataMixin adds a data field with JSON schema support.
type DataMixin struct {
	mixin.Schema
}

func (DataMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID(DataFieldDataTypeID, uuid.UUID{}).
			Optional(),
		// data_type_slug is server-derived from data_type_id via the generated
		// SetInputWithDataType builder method (backed by
		// validator.ValidateDataTypeInput). Hidden from GraphQL Create/Update inputs
		// so clients can't drift the (id, slug) pair apart. Still queryable
		// via Where inputs and returned in entity reads — the cross-service
		// filter use case ("WHERE data_type_slug = 'customer'") still works.
		field.String(DataFieldDataTypeSlug).
			Optional().
			Annotations(
				entgql.Skip(entgql.SkipMutationCreateInput | entgql.SkipMutationUpdateInput),
			),
		field.JSON(DataFieldData, map[string]any{}).
			Optional().
			Annotations(entgql.Type("Map")),
	}
}
