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

	"github.com/pyck-ai/pyck/backend/management/ent/types"
)

type Tenant struct {
	ent.Schema
}

func (Tenant) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Table("tenants"),
		entgql.RelayConnection(),
		entgql.Type("Tenant"),
		entgql.QueryField(),
	}
}

func (Tenant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tenantUsers", User.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("tenantUsers"),
			),
	}
}

func (Tenant) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("name").
			NotEmpty().
			Annotations(
				entgql.OrderField("NAME"),
			),
		// name folded by types.TenantNameKey, which is what tenant names
		// are unique by (see Indexes). Set by the schema hook from name on
		// every write that sets name; no caller sets it, and entgql.Skip
		// keeps it out of the GraphQL type, filters and ordering.
		field.String("name_key").
			NotEmpty().
			Annotations(entgql.Skip(entgql.SkipAll)),
		field.String("idp_org_ref").
			Annotations(
				entgql.OrderField("IDP_ORG_REF"),
			),
		field.Time("expires_at").
			Optional().
			Nillable().
			Annotations(
				entgql.OrderField("EXPIRES_AT"),
			),
		// Registration progress. "pending" between the registerTenant
		// resolver's insert and the end of the registration run
		// (RegisterTenantWorkflow, registered with Temporal as
		// RegisterTenantDbFirstWorkflow): the Zitadel org may not exist
		// yet, so the sync and expiry sweeps skip the row, and the lifecycle trigger starts the registration run on
		// its create event; update and delete events are dispatched
		// whatever the status. "done" once provisioning finished;
		// only done tenants take part in reconciliation. "error" when a
		// provisioning step failed for good: whatever was created in Zitadel
		// stays, the row keeps its name and its registration data so an
		// operator can re-run the workflow or retire the row with
		// deleteTenant; error rows are excluded from every sweep like
		// pending ones.
		//
		// The default is "done" on purpose: only the resolver knows a row
		// is still being provisioned and sets "pending" explicitly. Every
		// other writer (fixtures, tooling, a build that predates the column)
		// inserts a tenant whose org already exists, and a wrong "pending"
		// would hide that tenant from every sweep for good.
		field.Enum("status").
			Values("pending", "done", "error").
			Default("done").
			Annotations(
				entgql.OrderField("STATUS"),
			),
		// The admin account the registration workflow creates in Zitadel,
		// with the bcrypt hash of its initial password. Set by the
		// registerTenant resolver together with status=pending, cleared by
		// the workflow when it marks the row done and by every soft delete
		// (deleteTenant, the expiry sweep), so the hash lives no longer
		// than the registration it serves and a retired row cannot be
		// re-run. Sensitive keeps it out of the
		// entity's JSON (and therefore out of the outbox/NATS payloads the
		// MutationEventHook marshals) and entgql.Skip keeps it out of the
		// GraphQL type, WhereInput and ordering. It does not keep it out of
		// the Ent debug driver: with DB_DEBUG on, the INSERT that writes
		// this column is logged with its arguments, hash included.
		field.JSON("registration", &types.TenantRegistration{}).
			Optional().
			Sensitive().
			Annotations(entgql.Skip()),
	}
}

func (Tenant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idp_org_ref").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
		// Tenant names are unique across every row, soft-deleted ones
		// included, compared by name_key: the name folded the way Zitadel
		// folds an org name for its own uniqueness constraint
		// (types.TenantNameKey), so a pair Zitadel would refuse as the same
		// org collides here, before its registration run starts. The
		// registerTenant pre-flight only produces the friendly error; this
		// index is what stops two concurrent registrations of such names
		// from both committing a pending row: the loser's insert fails with
		// 23505. A retired tenant keeps its name, so restoreTenant can
		// always bring it back under it.
		index.Fields("name_key").
			Unique(),
		// Serves the tenant-expiry-check sweep query
		// (expires_at IS NOT NULL AND expires_at <= now AND deleted_at IS NULL).
		// Partial WHERE filters out the dominant "no expiry / already deleted"
		// rows so the sweep stays O(eligible) instead of O(tenants).
		index.Fields("expires_at").
			Annotations(entsql.IndexWhere("expires_at IS NOT NULL AND deleted_at IS NULL")),
	}
}

func (Tenant) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
