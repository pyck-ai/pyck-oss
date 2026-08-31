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
		// Identity is (slug, version): a DataType is append-only and ids are
		// server-generated, so the portable reference across environments is the
		// slug plus its version, not the name. Import/export resolves and
		// existence-checks DataTypes on this composite key.
		entgql.Directives(importexport.Importable("slug,version",
			importexport.WithList("dataTypes"),
			importexport.WithCreate("createDataType"),
			// Update is wired so a re-imported name change reaches the
			// server's rename-in-place branch (name is deliberately NOT an
			// immutable field). Without it the importer treats DataType as
			// create-only and silently skips an existing (slug, version),
			// dropping the rename.
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
		// name is the ONLY mutable business field — renaming is a label change,
		// not a schema change, so it doesn't warrant a new version row. Every
		// other field is Immutable, so gqlgen's MutationUpdate emits an
		// `UpdateDataTypeInput` exposing only `name` (the generic
		// `updateDataType` mutation replaces the old bespoke `renameDataType`).
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
			Immutable().
			Annotations(
				entgql.OrderField("DESCRIPTION"),
			),
		field.String("json_schema").
			NotEmpty().
			Immutable().
			Annotations(
				entgql.OrderField("JSON_SCHEMA"),
			),
		field.String("frontend_schema").
			Optional().
			Immutable().
			Annotations(
				entgql.OrderField("FRONTEND_SCHEMA"),
			),
		// entity picks the slot pool a datatype's indices are validated
		// against; changing it would leave the bindings naming slots the new
		// entity lacks, so it is fixed at creation.
		field.String("entity").
			NotEmpty().
			Immutable().
			Annotations(
				entgql.OrderField("ENTITY"),
			),
		// default marks the tenant-init DataType seeded by register-tenant.
		// PER-ROW, not a family label: a new version must re-assert it or it
		// drops to false. Written by register-tenant; nothing reads the
		// value today. Immutable: set once at create time — it stays in
		// CreateDataTypeInput but is excluded from UpdateDataTypeInput.
		field.Bool("default").
			Default(false).
			Immutable().
			Annotations(
				entgql.OrderField("DEFAULT"),
			),
		// version identifies a specific row within a (tenant_id, slug)
		// family. NOT NULL with a DB default of 1. The migration backfills
		// pre-#990 rows by densely numbering each (tenant_id, slug) family by
		// age (ROW_NUMBER() ... ORDER BY created_at, id) — not all-to-1, which
		// would collide on the new non-partial (tenant_id, slug, version) unique
		// index; the DEFAULT 1 applies to new rows only. NOT NULL because a NULL
		// would sort NULLS-FIRST and masquerade as the latest version. When
		// omitted on input the createDataType resolver assigns MAX(version)+1
		// for the family; explicitly settable so import/export can preserve
		// version numbers across environments. Immutable once written.
		field.Int("version").
			Default(1).
			Immutable().
			Annotations(
				entgql.OrderField("VERSION"),
			),
	}
}

func (DataType) Indexes() []ent.Index {
	return []ent.Index{
		// Versioned unique constraint: a version number is unique within a
		// (tenant_id, slug) family. Deliberately NOT a partial index — it
		// spans soft-deleted rows so a deleted version number can never be
		// reused, keeping ID-pinned references and import semantics stable.
		index.Fields(mixin.TenantFieldTenantID, "slug", "version").
			StorageKey("datatype_tenant_id_slug_version_uniq").
			Unique(),
		// Lookup index for "latest live version per slug" (MAX(version)
		// among non-deleted rows). Partial on deleted_at IS NULL so
		// dataTypeBySlug and the cache's delete-time promotion skip
		// soft-deleted versions.
		index.Fields(mixin.TenantFieldTenantID, "slug", "version").
			StorageKey("datatype_tenant_id_slug_version_desc").
			Annotations(
				entsql.DescColumns("version"),
				mixin.HistoryMixinNotDeletedIndexAnnotation(),
			),
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
