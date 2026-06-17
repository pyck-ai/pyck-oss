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
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Stock holds the schema definition for the Stock entity.
type Stock struct {
	ent.Schema
}

func (Stock) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("inventory"),
		entsql.Table("stocks"),
		entgql.RelayConnection(),
		entgql.QueryField(),
	}
}

// Fields of the Stock.
func (Stock) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("ITEM_ID"),
			),
		field.UUID("repository_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("REPOSITORY_ID"),
			),
		field.Int64("quantity").
			Min(0).
			Annotations(
				entgql.OrderField("QUANTITY"),
			),
		field.UUID("movement_id", uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField("MOVEMENT_ID"),
			),
		field.Int64("incoming_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("INCOMING_STOCK"),
			),
		field.Int64("outgoing_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OUTGOING_STOCK"),
			),

		field.Int64("own_quantity").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OWN_QUANTITY"),
			),

		field.Int64("own_incoming_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OWN_INCOMING_STOCK"),
			),
		field.Int64("own_outgoing_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OWN_OUTGOING_STOCK"),
			),

		field.Int64("version").
			Default(0).
			Annotations(
				entgql.Skip(entgql.SkipAll),
			),
	}
}

// Edges of the Stock.
func (Stock) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("itemStocks").
			Field("item_id").
			Required().
			Unique(),
		edge.From("repository", Repository.Type).
			Ref("repositoryStocks").
			Field("repository_id").
			Required().
			Unique(),
	}
}

// Indexes of the Stock.
func (Stock) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "repository_id", "item_id", "created_at"),
		index.Fields("movement_id"),
		index.Fields("tenant_id", "repository_id", "item_id", "version").Unique(),
		// Lets the tenant-less NOT EXISTS-on-self stock subqueries in
		// loadAncestorStocks / loadLatestStockPerRepo (repository_id, item_id,
		// created_at > outer) index-scan instead of seq-scanning: they don't
		// filter by tenant, so they can't use the tenant-prefixed index. Added
		// in migration 20260521135054; kept as created_at DESC to match the
		// existing index (no rebuild).
		index.Fields("repository_id", "item_id", "created_at").
			Annotations(entsql.DescColumns("created_at")),
	}
}

func (Stock) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
