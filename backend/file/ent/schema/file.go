package schema

import (
	"errors"
	"fmt"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/datatype"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

const fileEntityName = "file"

var ErrInvalidAlias = errors.New("invalid alias")

type File struct {
	ent.Schema
}

func (File) Annotations() []schema.Annotation {
	keyDirective := entgql.NewDirective("key", &ast.Argument{
		Name: "fields",
		Value: &ast.Value{
			Raw:  "id",
			Kind: ast.StringValue,
		},
	})
	return []schema.Annotation{
		entgql.Type("File"),
		entsql.Schema("file"),
		entsql.Table("files"),
		entgql.RelayConnection(),
		entgql.Directives(keyDirective),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

func (f File) Fields() []ent.Field {
	entities := datatype.DataTypeEntities()
	refTypes := make([]string, 0, len(entities))

	for _, entity := range entities {
		if entity == fileEntityName {
			continue
		}

		refTypes = append(refTypes, entity)
	}

	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("refid", uuid.UUID{}),
		field.Enum("reftype").
			Values(refTypes...).
			Annotations(entgql.OrderField("REFTYPE")),
		field.String("description").
			Optional().
			Annotations(entgql.OrderField("DESCRIPTION")),
		field.String("name").
			NotEmpty().
			Annotations(
				entgql.Skip(entgql.SkipMutationUpdateInput),
				entgql.OrderField("NAME"),
			),
		field.Int64("size").
			Min(0).
			Optional().
			Nillable().
			Annotations(
				entgql.Skip(entgql.SkipMutationUpdateInput),
				entgql.OrderField("SIZE"),
			),
		field.String("content_type").
			NotEmpty().
			Annotations(
				entgql.Skip(entgql.SkipMutationUpdateInput),
				entgql.OrderField("CONTENT_TYPE"),
			),
		field.String("public_alias").
			Optional().
			Nillable().
			Validate(f.validatePublicAlias).
			Annotations(
				entgql.OrderField("PUBLIC_ALIAS"),
			),
	}
}

func (File) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("refid"),
		index.Fields(mixin.TenantFieldTenantID, "refid", "name").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
		index.Fields(mixin.TenantFieldTenantID, "public_alias").
			Unique().
			// Partial on public_alias IS NOT NULL too: the unique constraint must
			// only apply to rows that actually have an alias (matches the DB).
			Annotations(entsql.IndexWhere("deleted_at IS NULL AND public_alias IS NOT NULL")),
	}
}

func (File) validatePublicAlias(alias string) error {
	if !std.IsValidSlug(alias) {
		return fmt.Errorf("%w %q: must be lowercase alphanumeric with hyphens", ErrInvalidAlias, alias)
	}
	return nil
}

func (File) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
