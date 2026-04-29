package schema

import (
	"fmt"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/importexport"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

var ErrInvalidDataTypeSlug = fmt.Errorf("invalid data type slug, it must be lowercase and contain only alphanumeric characters and hyphens")

type DataType struct {
	ent.Schema
}

func (DataType) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Table("datatypes"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.Directives(importexport.Importable("name",
			importexport.WithList("dataTypes"),
			importexport.WithCreate("createDataType"),
			importexport.WithUpdate("updateDataType"),
		)),
	}
}

func (dt DataType) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("name").
			Default("").
			Annotations(
				entgql.OrderField("NAME"),
			),
		field.String("slug").
			Optional().
			Immutable().
			Validate(dt.validateDataTypeSlug).
			Annotations(
				entgql.OrderField("SLUG"),
			),
		field.String("description").
			Optional().
			Annotations(
				entgql.OrderField("DESCRIPTION"),
			),
		field.String("json_schema").
			NotEmpty().
			Annotations(
				entgql.OrderField("JSON_SCHEMA"),
			),
		field.String("frontend_schema").
			Optional().
			Annotations(
				entgql.OrderField("FRONTEND_SCHEMA"),
			),
		field.Bool("default").
			Default(false).
			Annotations(
				entgql.OrderField("DEFAULT"),
			),
		field.String("entity").
			NotEmpty().
			Annotations(
				entgql.OrderField("ENTITY"),
			),
	}
}

func (DataType) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields(mixin.TenantFieldTenantID, "slug").
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()).
			Unique(),
	}
}

func (DataType) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}

func (DataType) validateDataTypeSlug(s string) error {
	if !std.IsValidSlug(s) {
		return ErrInvalidDataTypeSlug
	}
	return nil
}
