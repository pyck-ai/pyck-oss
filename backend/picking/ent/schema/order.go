package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/importexport"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Order holds the schema definition for the Order entity.
type Order struct {
	ent.Schema
}

func (Order) Annotations() []schema.Annotation {
	keyDirective := entgql.NewDirective("key", &ast.Argument{
		Name: "fields",
		Value: &ast.Value{
			Raw:  "id",
			Kind: ast.StringValue,
		},
	})
	return []schema.Annotation{
		entgql.Type("PickingOrder"),
		entsql.Schema("picking"),
		entsql.Table("orders"),
		entgql.RelayConnection(),
		entgql.Directives(keyDirective, importexport.Importable("",
			importexport.WithList("pickingOrders"),
			importexport.WithCreate("createPickingOrder"),
		)),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the Order.
func (Order) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("customer_id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Annotations(
				entgql.OrderField("CUSTOMER_ID"),
			),
	}
}

// Edges of the Order
func (Order) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("orderItems", OrderItems.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("orderItems"),
			),
		edge.To("outboundShipmentNotifications", OutboundShipmentNotification.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("outboundShipmentNotifications"),
			),
	}
}

func (Order) Indexes() []ent.Index {
	return []ent.Index{
		// Deployed whole-column GIN. The indexed slots supersede it for lookups;
		// dropping it is a separate, deliberate migration.
		index.Fields("data").
			Annotations(entsql.IndexTypes(map[string]string{dialect.Postgres: "GIN"})),
		// The boot backfill probes (tenant_id, data_type_slug) per binding. Every
		// slot index is partial on "slot IS NOT NULL" -- the exact inverse of the
		// probe's "IS NULL" -- so without this the steady-state probe, which
		// matches nothing and therefore never short-circuits on LIMIT, seq-scans
		// the whole table once per datatype x binding before the server starts.
		index.Fields("tenant_id", "data_type_slug"),
	}
}

func (Order) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		// Indexed slots for runtime-defined data keys (serial numbers today).
		dataindex.PoolMixin(dataindex.EntityPickingOrder),
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
