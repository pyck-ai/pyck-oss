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
				entgql.Type("Int64"),
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
				entgql.Type("Int64"),
			),
		field.Int64("outgoing_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OUTGOING_STOCK"),
				entgql.Type("Int64"),
			),

		field.Int64("own_quantity").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OWN_QUANTITY"),
				entgql.Type("Int64"),
			),

		field.Int64("own_incoming_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OWN_INCOMING_STOCK"),
				entgql.Type("Int64"),
			),
		field.Int64("own_outgoing_stock").
			Min(0).
			Default(0).
			Annotations(
				entgql.OrderField("OWN_OUTGOING_STOCK"),
				entgql.Type("Int64"),
			),

		// Unique and monotonic per (tenant, repository, item) — the total order
		// "current row" is decided by. Exposed read-only for row identity.
		field.Int64("version").
			Default(0).
			Annotations(
				entgql.OrderField("VERSION"),
				entgql.Type("Int64"),
				entgql.Skip(entgql.SkipMutationCreateInput|entgql.SkipMutationUpdateInput),
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
		// Serves the NOT EXISTS-on-self subqueries in loadAncestorStocks /
		// loadItemIDsAtRepo / loadLatestStockPerRepo, which filter by neither
		// tenant_id (so the unique index above cannot serve them) nor
		// deleted_at (so this one must not be partial).
		index.Fields("repository_id", "item_id", "version").
			Annotations(entsql.DescColumns("version")),
		// Serves InventoryItem.itemstocks: the repository-led indexes above read
		// the rows of every item in the tenant.
		index.Fields("tenant_id", "item_id", "repository_id", "version").
			Annotations(entsql.DescColumns("version")),
	}
}

func (Stock) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
