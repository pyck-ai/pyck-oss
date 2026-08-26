package mixin

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// IdempotencyKeyMixin provides the per-service idempotency_keys table
// schema. Each service must define an IdempotencyKey entity that uses
// this mixin and annotates the table with its own database schema name:
//
//	type IdempotencyKey struct{ ent.Schema }
//
//	func (IdempotencyKey) Mixin() []ent.Mixin {
//	    return []ent.Mixin{ mixin.IdempotencyKeyMixin{} }
//	}
//
//	func (IdempotencyKey) Annotations() []schema.Annotation {
//	    return []schema.Annotation{
//	        entsql.Schema("<service>"),
//	        entsql.Table("idempotency_keys"),
//	    }
//	}
//
// The mixin is deliberately bare — no TenantMixin, no DataMixin, no
// HistoryMixin. Idempotency rows are infrastructure: they are written
// inside the mutation transaction by the gqltx middleware, never exposed
// via GraphQL, and their tenant_id is supplied explicitly by the
// idempotency package rather than auto-populated from request context.
type IdempotencyKeyMixin struct {
	mixin.Schema
}

// Compile-time guard that the mixin satisfies ent.Mixin.
var _ ent.Mixin = (*IdempotencyKeyMixin)(nil)

// Annotations hides the entity from the GraphQL schema and declares the
// table-level CHECK constraints.
//
// Idempotency rows are infrastructure-only: they are written by the gqltx
// middleware and read by the janitor, never queried or mutated by clients,
// hence entgql.SkipAll.
//
// The CHECK constraints mirror the Go-side validators (the status enum and
// the 32-byte checksum) at the database level. ent does not emit checks for
// enum fields or byte-length validators on its own, so they are declared
// explicitly here to keep the generated schema in lockstep with the
// committed migrations.
func (IdempotencyKeyMixin) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Skip(entgql.SkipAll),
		entsql.Checks(map[string]string{
			"idempotency_keys_status_check":       "status IN ('in_flight', 'committed')",
			"idempotency_keys_checksum_len_check": "octet_length(operation_checksum) = 32",
		}),
	}
}

// Fields of the idempotency_keys table.
func (IdempotencyKeyMixin) Fields() []ent.Field {
	return []ent.Field{
		// Primary key (UUID v7 for time-ordering — helps the janitor's
		// (status, created_at) range scan stay locality-friendly).
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),

		// Client-supplied idempotency key. Bounded at 255 to match the
		// HTTP header contract enforced by common/idempotency.FromHeaders.
		// MaxLen is only a Go-side validator, so the SQL column type is
		// pinned explicitly to varchar(255) to carry the bound into the DB.
		field.String("key").
			MaxLen(255).
			SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).
			Immutable(),

		// Tenant of the originating mutation. Always non-nil: PreCheck
		// rejects unauthenticated requests with 400 before we get here.
		field.UUID("tenant_id", uuid.UUID{}).
			Immutable(),

		// User that triggered the mutation. Always non-nil for the same
		// reason as tenant_id.
		field.UUID("user_id", uuid.UUID{}).
			Immutable(),

		// Operation name from the GraphQL request (e.g. "CreateItem").
		// Used to surface a meaningful error on operation mismatch.
		field.String("operation_name").
			Immutable(),

		// sha256 over (operation_name + canonical_json(variables)).
		// 32 fixed bytes; rejects a key reused with different inputs.
		field.Bytes("operation_checksum").
			MaxLen(32).
			MinLen(32).
			Immutable(),

		// Lifecycle status. "in_flight" while the mutation tx is open,
		// "committed" once the response has been cached.
		field.Enum("status").
			Values("in_flight", "committed").
			Default("in_flight"),

		// Serialized graphql.Response for replay. Nil while in_flight.
		field.Bytes("response").
			Optional().
			Nillable(),

		// Default(nowUTC) populates the value Go-side; DefaultExpr pins the
		// matching SQL column default (now()) so the generated schema agrees
		// with the committed migrations.
		field.Time("created_at").
			Default(nowUTC).
			Annotations(entsql.DefaultExpr("now()")).
			Immutable(),

		field.Time("updated_at").
			Default(nowUTC).
			UpdateDefault(nowUTC).
			Annotations(entsql.DefaultExpr("now()")),
	}
}

// Indexes ensure (tenant_id, user_id, key) uniqueness and an efficient
// scan path for the janitor.
func (IdempotencyKeyMixin) Indexes() []ent.Index {
	return []ent.Index{
		// Uniqueness — also the lookup index for PreCheck.
		// Column order is (tenant_id, user_id, key) rather than the
		// natural (key, tenant_id, user_id): all real queries supply
		// the full triple so lookup performance is identical, but this
		// order clusters rows for the same tenant + user contiguously
		// in the index, which makes a retry burst from one client
		// touch fewer index pages.
		index.Fields("tenant_id", "user_id", "key").Unique().
			StorageKey("idempotency_keys_tenant_user_key"),
		// Janitor: DELETE WHERE status='committed' AND created_at < $cutoff.
		// Partial index on status='committed' so the scan walks only
		// rows the janitor can actually delete (in_flight rows live
		// only for the duration of one mutation tx and are gone by
		// the next scan).
		index.Fields("created_at").
			Annotations(entsql.IndexWhere("status = 'committed'")).
			StorageKey("idempotency_keys_committed_created"),
	}
}
