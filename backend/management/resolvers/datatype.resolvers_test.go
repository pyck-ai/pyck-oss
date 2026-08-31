package resolvers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	queryDataTypes = resolver.ParseTemplate(`query {
		dataTypes {
			totalCount
			edges {
				node { id tenantID name description jsonSchema }
				cursor
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	createDataType = resolver.ParseTemplate(`mutation {
		createDataType(input: {
			name: "{{.Name}}",
			slug: "{{.Slug}}",
			description: "{{.Description}}",
			entity: "item",
			jsonSchema: "{{.JsonSchema}}"
		}) {
			id name description tenantID jsonSchema version
		}
	}`)

	// createDataTypeWithVersion pins an explicit version, mirroring the
	// import/export path that preserves numbering across environments.
	createDataTypeWithVersion = resolver.ParseTemplate(`mutation {
		createDataType(input: {
			name: "{{.Name}}",
			slug: "{{.Slug}}",
			description: "{{.Description}}",
			entity: "item",
			jsonSchema: "{{.JsonSchema}}",
			version: {{.Version}}
		}) {
			id name description tenantID jsonSchema version
		}
	}`)

	deleteDataType = resolver.ParseTemplate(`mutation {
		deleteDataType(id: "{{.ID}}") {
			deletedID
		}
	}`)

	dataTypeBySlug = resolver.ParseTemplate(`query {
		dataTypeBySlug(slug: "{{.Slug}}") {
			id name slug tenantID jsonSchema version
		}
	}`)

	updateDataType = resolver.ParseTemplate(`mutation {
		updateDataType(id: "{{.ID}}", input: { name: "{{.Name}}" }) {
			id name slug tenantID jsonSchema
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type dataTypeNode struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	JsonSchema  string
	Version     int
}

type queryDataTypesData struct {
	DataTypes struct {
		TotalCount int
		Edges      []struct {
			Node   dataTypeNode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

type createDataTypeData struct {
	CreateDataType dataTypeNode
}

type deleteDataTypeData struct {
	DeleteDataType struct{ DeletedID uuid.UUID }
}

type dataTypeBySlugData struct {
	DataTypeBySlug *dataTypeNode
}

type updateDataTypeData struct {
	UpdateDataType dataTypeNode
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestDataType_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userAWriter)

		data := execOK[queryDataTypesData](te, ctx, queryDataTypes, nil)

		assert.Equal(t, 0, data.DataTypes.TotalCount)
		assert.Empty(t, data.DataTypes.Edges)
	})

	t.Run("returns only own tenant's data types", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userAWriter)
		ctxB := te.ctx(userB)

		dtA := te.newDataType(ctxA, userAWriter).Create()
		te.newDataType(ctxB, userB).Create()

		data := execOK[queryDataTypesData](te, ctxA, queryDataTypes, nil)

		require.Equal(t, 1, data.DataTypes.TotalCount)
		assert.Equal(t, dtA.ID, data.DataTypes.Edges[0].Node.ID)
	})

	t.Run("reader role can query data types", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		ctxBReader := te.ctx(userBReader)

		te.newDataType(ctxB, userB).Create()

		data := execOK[queryDataTypesData](te, ctxBReader, queryDataTypes, nil)

		assert.Equal(t, 1, data.DataTypes.TotalCount)
	})

	t.Run("fails for unauthenticated user", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		unauthUser := &authn.User{}
		ctx := te.ctx(unauthUser)

		execErr(te, ctx, queryDataTypes, nil, "unauthorized")
	})
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestDataType_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates data type successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test description",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})

		created := data.CreateDataType
		assert.Equal(t, "Test DataType", created.Name)
		assert.Equal(t, "Test description", created.Description)
		assert.Equal(t, resolver.TenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.DataType.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "Test DataType", stored.Name)

		// Verify event
		te.assertEvents(ctx, Create("datatype", created.ID))
	})

	t.Run("repeated slug creates a new version (append-only)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		v1 := te.newDataType(ctx, userA).Slug("versioned-slug").Create()
		te.clearEvents(ctx)

		// Same slug, second insert — should succeed as a new version row
		// under #990's append-only model.
		data := execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Versioned DataType v2",
			"Slug":        "versioned-slug",
			"Description": "Second version",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})

		assert.NotEqual(t, v1.ID, data.CreateDataType.ID, "second version must get its own ID")

		// Both rows visible via the relay query.
		list := execOK[queryDataTypesData](te, ctx, queryDataTypes, nil)
		assert.Equal(t, 2, list.DataTypes.TotalCount, "both versions should be queryable")
		te.assertEvents(ctx, Create("datatype", data.CreateDataType.ID))
	})

	t.Run("auto-assigns incrementing versions per slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		for want := 1; want <= 3; want++ {
			data := execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
				"Name":        "Auto Versioned",
				"Slug":        "auto-versioned",
				"Description": "v",
				"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			})
			assert.Equal(t, want, data.CreateDataType.Version, "version should auto-increment")
		}
	})

	t.Run("accepts the next-sequential explicit version and continues numbering", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Seed version 1 via auto-assign.
		execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Imported",
			"Slug":        "imported-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})

		// Pin version 2 (above MAX=1) explicitly — the import replay path,
		// where versions arrive in ascending order and each is above the
		// current max.
		pinned := execOK[createDataTypeData](te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Imported",
			"Slug":        "imported-slug",
			"Description": "v2",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     2,
		})
		assert.Equal(t, 2, pinned.CreateDataType.Version, "explicit next-sequential version must be honored")

		// A subsequent auto-assigned create continues from MAX(version).
		next := execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Next",
			"Slug":        "imported-slug",
			"Description": "auto",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})
		assert.Equal(t, 3, next.CreateDataType.Version, "auto-assign should continue from the explicit version")
	})

	t.Run("allows a gap above the max but rejects backfilling below it", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Family starts at version 1.
		execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Gappy",
			"Slug":        "gappy-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})

		// Pinning version 3 (above MAX, skipping 2) is allowed — import must be
		// able to preserve a source's exact, possibly-sparse version numbers
		// ((slug, version) is the portable identity entity FKs re-resolve by).
		pinned := execOK[createDataTypeData](te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Gappy",
			"Slug":        "gappy-slug",
			"Description": "v3",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     3,
		})
		assert.Equal(t, 3, pinned.CreateDataType.Version, "a version above the max is honored, gap and all")

		// Backfilling the skipped version 2 (≤ MAX, no live row) is rejected:
		// versions are append-only and numbers are never reused.
		execErr(te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Gappy",
			"Slug":        "gappy-slug",
			"Description": "v2",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     2,
		}, "greater than the current maximum")
	})

	t.Run("re-import of an identical version is an idempotent no-op", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		first := execOK[createDataTypeData](te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Idem",
			"Slug":        "idem-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     1,
		})
		te.clearEvents(ctx)

		// Re-importing the same (slug, version) with identical fields returns
		// the same row and emits no event.
		again := execOK[createDataTypeData](te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Idem",
			"Slug":        "idem-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     1,
		})
		assert.Equal(t, first.CreateDataType.ID, again.CreateDataType.ID, "re-import must return the existing row")
		te.assertNoEvents(ctx)

		// Exactly one row exists (isolated per-test tenant).
		list := execOK[queryDataTypesData](te, ctx, queryDataTypes, nil)
		assert.Equal(t, 1, list.DataTypes.TotalCount, "no duplicate row created")
	})

	t.Run("re-import with only a changed name updates in place", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		first := execOK[createDataTypeData](te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Original",
			"Slug":        "rename-import-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     1,
		})

		updated := execOK[createDataTypeData](te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Renamed On Import",
			"Slug":        "rename-import-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     1,
		})
		assert.Equal(t, first.CreateDataType.ID, updated.CreateDataType.ID, "same row")
		assert.Equal(t, "Renamed On Import", updated.CreateDataType.Name, "name updated in place")
	})

	t.Run("rejects a re-import that changes an immutable field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Auto-assigns version 1 with description "v1".
		execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Dup",
			"Slug":        "dup-slug",
			"Description": "v1",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})

		// Re-importing version 1 with a different (immutable) description is a
		// conflict, not a silent overwrite.
		execErr(te, ctx, createDataTypeWithVersion, map[string]any{
			"Name":        "Dup",
			"Slug":        "dup-slug",
			"Description": "v1 changed",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
			"Version":     1,
		}, "different immutable fields")
	})

	t.Run("does not reuse a soft-deleted version number", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newDataType(ctx, userA).Slug("recycled").Create()       // v1
		v2 := te.newDataType(ctx, userA).Slug("recycled").Create() // v2

		execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{"ID": v2.ID})

		// MAX(version) spans deleted rows, so the next create is v3 — not v2.
		data := execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Recycled",
			"Slug":        "recycled",
			"Description": "after delete",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})
		assert.Equal(t, 3, data.CreateDataType.Version, "deleted version numbers must not be reused")
	})

	t.Run("rejects reader role", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userBReader)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		}, "deny rule")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "INVALID SLUG",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		}, "validator failed")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects malformed JSON schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(`{"type": "object", "properties": {`),
		}, "unexpected EOF")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid JSON schema type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(`{"type": "invalid_type"}`),
		}, "value must be one of")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty JSON schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  "",
		}, "EOF")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// VERSION COLUMN DEFAULT (migration backfill value)
// =============================================================================

// TestDataType_VersionColumnDefault verifies the #990 migration's column
// default at the Ent/DB layer: a DataType row inserted without an explicit
// version gets version 1 (field.Int("version").Default(1)). This is the exact
// value the migration stamps onto pre-#990 rows (ADD COLUMN "version" bigint
// NOT NULL DEFAULT 1), so it must hold independently of the resolver's
// MAX(version)+1 auto-assignment — every test elsewhere sets the version
// explicitly, so this is the only coverage of the raw column default.
func TestDataType_VersionColumnDefault(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	var dt *ent.DataType
	err := te.withTx(ctx, func(tx *ent.Tx) error {
		txCtx := ent.NewTxContext(txid.With(ctx, txid.New()), tx)
		var createErr error
		// Deliberately omit SetVersion so the column default applies.
		dt, createErr = tx.DataType.Create().
			SetTenantID(userA.TenantID).
			SetName("Defaulted DataType").
			SetSlug("defaulted-version-slug").
			SetDescription("created without an explicit version").
			SetEntity("item").
			SetJSONSchema(testDataTypeSchema).
			Save(txCtx)
		return createErr
	})
	require.NoError(t, err)
	assert.Equal(t, 1, dt.Version, "a row created without a version must default to 1")
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestDataType_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{
			"ID": dt.ID,
		})

		assert.Equal(t, dt.ID, data.DeleteDataType.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.DataType.Get(te.ctxWithDeleted(userA), dt.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("datatype", dt.ID))
	})

	t.Run("rejects delete of other tenant's data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteDataType, map[string]any{
			"ID": dt.ID,
		}, "data type not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of other tenant's data type for system users too", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		// TenantMixin's mutation filter SKIPS system users, so without an
		// explicit tenant predicate a system caller acting on tenant A could
		// soft-delete tenant B's row — which fans out cache eviction and
		// entity write-blocks across every service for tenant B.
		ctxSys := te.ctxForTenant(systemUser, resolver.TenantA)
		execErr(te, ctxSys, deleteDataType, map[string]any{
			"ID": dt.ID,
		}, "data type not found")

		stored, err := te.Ent.DataType.Get(te.ctxWithDeleted(userB), dt.ID)
		require.NoError(t, err)
		assert.True(t, stored.DeletedAt.IsZero(), "tenant B's data type must stay live")
		te.assertNoEvents(ctxB)
	})

	t.Run("rejects reader role", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxBReader := te.ctx(userBReader)
		execErr(te, ctxBReader, deleteDataType, map[string]any{
			"ID": dt.ID,
		}, "deny rule")

		te.assertNoEvents(ctxBReader)
	})
}

// =============================================================================
// BY-SLUG / VERSIONING TESTS
// =============================================================================

// TestDataType_BySlug exercises the dataTypeBySlug query under the #990
// append-only model: latest non-deleted version per slug wins, deletion
// promotes prior versions, tenant scoping is enforced.
func TestDataType_BySlug(t *testing.T) {
	t.Parallel()

	t.Run("returns latest of multiple versions for slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		v1 := te.newDataType(ctx, userA).Slug("customer").Create()
		v2 := te.newDataType(ctx, userA).Slug("customer").Create()

		data := execOK[dataTypeBySlugData](te, ctx, dataTypeBySlug, map[string]any{
			"Slug": "customer",
		})

		require.NotNil(t, data.DataTypeBySlug)
		assert.Equal(t, v2.ID, data.DataTypeBySlug.ID, "latest version should win")
		assert.NotEqual(t, v1.ID, data.DataTypeBySlug.ID)
		assert.Equal(t, 2, data.DataTypeBySlug.Version, "bySlug must resolve to MAX(version)")
	})

	t.Run("deleting the latest promotes the prior non-deleted version", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		v1 := te.newDataType(ctx, userA).Slug("widget").Create()
		v2 := te.newDataType(ctx, userA).Slug("widget").Create()
		te.clearEvents(ctx)

		execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{
			"ID": v2.ID,
		})

		data := execOK[dataTypeBySlugData](te, ctx, dataTypeBySlug, map[string]any{
			"Slug": "widget",
		})

		require.NotNil(t, data.DataTypeBySlug)
		assert.Equal(t, v1.ID, data.DataTypeBySlug.ID, "deleting latest should expose the prior version")
	})

	t.Run("returns null when all versions are deleted", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		v1 := te.newDataType(ctx, userA).Slug("ghosted").Create()

		execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{"ID": v1.ID})

		data := execOK[dataTypeBySlugData](te, ctx, dataTypeBySlug, map[string]any{
			"Slug": "ghosted",
		})

		assert.Nil(t, data.DataTypeBySlug, "no live version should yield null")
	})

	t.Run("returns null for unknown slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[dataTypeBySlugData](te, ctx, dataTypeBySlug, map[string]any{
			"Slug": "does-not-exist",
		})

		assert.Nil(t, data.DataTypeBySlug)
	})

	t.Run("is tenant-scoped (cross-tenant slug collision is hidden)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userAWriter)
		ctxB := te.ctx(userB)

		dtA := te.newDataType(ctxA, userAWriter).Slug("shared-slug").Create()
		te.newDataType(ctxB, userB).Slug("shared-slug").Create()

		// Tenant A sees only its own row.
		dataA := execOK[dataTypeBySlugData](te, ctxA, dataTypeBySlug, map[string]any{
			"Slug": "shared-slug",
		})
		require.NotNil(t, dataA.DataTypeBySlug)
		assert.Equal(t, dtA.ID, dataA.DataTypeBySlug.ID)
		assert.Equal(t, resolver.TenantA, dataA.DataTypeBySlug.TenantID)
	})
	t.Run("is tenant-scoped for system users too", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userAWriter)
		ctxB := te.ctx(userB)

		// Tenant A holds v1; tenant B holds the globally highest version of
		// the same slug. TenantMixin's privacy filter SKIPS system users, so
		// without an explicit tenant predicate the resolver would return B's
		// v2 to a system caller acting on tenant A.
		dtA := te.newDataType(ctxA, userAWriter).Slug("shared-slug").Create()
		te.newDataType(ctxB, userB).Slug("shared-slug").Create()
		te.newDataType(ctxB, userB).Slug("shared-slug").Create() // v2 in B

		ctxSys := te.ctxForTenant(systemUser, resolver.TenantA)
		data := execOK[dataTypeBySlugData](te, ctxSys, dataTypeBySlug, map[string]any{
			"Slug": "shared-slug",
		})
		require.NotNil(t, data.DataTypeBySlug)
		assert.Equal(t, resolver.TenantA, data.DataTypeBySlug.TenantID,
			"system caller scoped to tenant A must not resolve tenant B's row")
		assert.Equal(t, dtA.ID, data.DataTypeBySlug.ID)
	})
}

// =============================================================================
// RENAME TESTS (name is the only mutable business field on DataType)
// =============================================================================

func TestDataType_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates name in place", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateDataTypeData](te, ctx, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": "Renamed",
		})

		assert.Equal(t, dt.ID, data.UpdateDataType.ID, "same row, only name changed")
		assert.Equal(t, "Renamed", data.UpdateDataType.Name)
		te.assertEvents(ctx, Update("datatype", dt.ID))
	})

	t.Run("same-name update is a side-effect-free no-op", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		// The import path re-sends every DataType on re-import; an unchanged
		// name must not bump updated_at or emit a datatype event (consumers
		// do real work per event: cache fan-out, picking's index backfill).
		data := execOK[updateDataTypeData](te, ctx, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": dt.Name,
		})

		assert.Equal(t, dt.ID, data.UpdateDataType.ID)
		assert.Equal(t, dt.Name, data.UpdateDataType.Name)
		te.assertNoEvents(ctx)

		stored, err := te.Ent.DataType.Get(ctx, dt.ID)
		require.NoError(t, err)
		assert.Equal(t, dt.UpdatedAt, stored.UpdatedAt, "no-op must not bump updated_at")
	})

	t.Run("rejects update of other tenant's data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": "Hacked",
		}, "data type not found")
	})

	t.Run("rejects update of other tenant's data type for system users too", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		// TenantMixin's query and mutation filters both SKIP system users, so
		// without an explicit tenant predicate a system caller acting on
		// tenant A could read (leaking json_schema) and rename tenant B's row.
		ctxSys := te.ctxForTenant(systemUser, resolver.TenantA)
		execErr(te, ctxSys, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": "Hacked",
		}, "data type not found")

		stored, err := te.Ent.DataType.Get(ctxB, dt.ID)
		require.NoError(t, err)
		assert.Equal(t, dt.Name, stored.Name, "tenant B's data type must keep its name")
		te.assertNoEvents(ctxB)
	})

	t.Run("rejects reader role", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxBReader := te.ctx(userBReader)
		execErr(te, ctxBReader, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": "ReadOnlyAttempt",
		}, "deny rule")
	})

	t.Run("rejects update of soft-deleted data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()

		// Soft-delete first.
		execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{
			"ID": dt.ID,
		})

		// Update now must fail with not-found semantics (consistent with the
		// entity-mutation write-block on deleted DataTypes).
		execErr(te, ctx, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": "TooLate",
		}, "data type not found")
	})
}

// =============================================================================
// EVENT PAYLOAD SHAPE
// =============================================================================

// dataTypeEventPayload returns the single outbox entry's payload for the
// given operation, requiring data_after to be a JSON object — the shape
// every downstream DataType cache decodes. The hook publishes whatever the
// mutation returned, and a bulk Update().Save returns the affected-row
// COUNT: data_after then arrives as a number, the cache's map assertion
// fails, the decoded slug is empty, and rename propagation and
// delete-promotion silently die in every service.
func dataTypeEventPayload(te *testEnv, ctx context.Context, op string) map[string]any {
	te.t.Helper()

	entries, err := te.Ent.EntityEventsOutbox.Query().All(ctx)
	require.NoError(te.t, err)
	require.Len(te.t, entries, 1, "expected exactly one %s event", op)
	require.True(te.t, strings.HasSuffix(entries[0].Topic, "."+op),
		"topic %q does not end with .%s", entries[0].Topic, op)

	after, ok := entries[0].Payload["data_after"].(map[string]any)
	require.True(te.t, ok,
		"data_after must be the row (a JSON object), got %T: %v",
		entries[0].Payload["data_after"], entries[0].Payload["data_after"])
	return after
}

func TestDataType_EventPayloadCarriesRow(t *testing.T) {
	t.Parallel()

	t.Run("rename event carries the row with its slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		execOK[updateDataTypeData](te, ctx, updateDataType, map[string]any{
			"ID":   dt.ID,
			"Name": "Renamed",
		})

		after := dataTypeEventPayload(te, ctx, "update")
		assert.Equal(t, dt.Slug, after["slug"], "downstream caches key the slug slot on this field")
		assert.Equal(t, "Renamed", after["name"])
	})

	t.Run("delete event carries the row with its slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{"ID": dt.ID})

		after := dataTypeEventPayload(te, ctx, "delete")
		assert.Equal(t, dt.Slug, after["slug"],
			"delete-promotion re-points the slug slot using this field; without it the deleted version keeps serving")
	})
}
