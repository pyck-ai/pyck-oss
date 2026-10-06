package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/importexport"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/vektah/gqlparser/v2/ast"
)

// Item holds the schema definition for the Item entity.
type Item struct {
	ent.Schema
}

func (Item) Annotations() []schema.Annotation {
	keyDirective := entgql.NewDirective("key", &ast.Argument{
		Name: "fields",
		Value: &ast.Value{
			Raw:  "sku",
			Kind: ast.StringValue,
		},
	})
	return []schema.Annotation{
		entgql.Type("InventoryItem"),
		entsql.Schema("inventory"),
		entsql.Table("items"),
		entgql.RelayConnection(),
		entgql.Directives(keyDirective, importexport.Importable("sku",
			importexport.WithList("inventoryItems"),
			importexport.WithCreate("createInventoryItem"),
			importexport.WithUpdate("updateInventoryItem"),
			importexport.WithDataTypeReference(),
		)),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the Item.
func (Item) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("sku").
			NotEmpty().
			Annotations(
				entgql.OrderField("SKU"),
			),
	}
}

func (Item) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

func (Item) Edges() []ent.Edge {
	return []ent.Edge{
		// Resolved in graph/item.graphql: eager-loading these append-only edges
		// is unbounded, since the LimitMixin cap does not reach eager loads.
		edge.To("itemMovementItems", ItemMovement.Type).
			Annotations(entgql.Skip(entgql.SkipType)),
		edge.To("itemTransactions", Transaction.Type).
			Annotations(entgql.Skip(entgql.SkipType)),
		edge.To("itemStocks", Stock.Type).
			Annotations(entgql.Skip(entgql.SkipType)),
		edge.From("itemSet", ItemSet.Type).Ref("items"),
	}
}

func (Item) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
