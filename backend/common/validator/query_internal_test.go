package validator

import (
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// TestCreateQueryForCountingUniqueRecords asserts the exact SQL produced by the
// uniqueness count-query builder for each dialect.
//
// The strings matter literally: PostgreSQL must use positional $1, $2, …
// placeholders, never ?. A ? collides with the jsonb ? existence operator and
// the driver does not rebind raw SQL, so a literal ? produces a syntax error at
// execution time (regression from #1142). SQLite uses ? placeholders and the
// json_extract() accessor. Both must thread the field value, data_type_id and
// tenant_id — plus excludeID when updating — in placeholder order.
func TestCreateQueryForCountingUniqueRecords(t *testing.T) {
	t.Parallel()

	tenantID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	dataTypeID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	excludeID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	// Path() walks up to a nil parent, so the schema root is an empty parent
	// Field; a top-level field points at it, a nested field chains through it.
	root := Field{}
	taxID := Field{Name: "taxId", Type: "string", Unique: true, Parent: &root}
	userEmail := Field{
		Name: "email", Type: "string", Unique: true,
		Parent: &Field{Name: "user", Parent: &root},
	}

	tests := []struct {
		name      string
		dbDriver  string
		field     Field
		excludeID *uuid.UUID
		wantQuery string
		wantArgs  []any
	}{
		{
			name:      "postgres top-level field, no excludeID (create path)",
			dbDriver:  dialect.Postgres,
			field:     taxID,
			excludeID: nil,
			wantQuery: "SELECT count(*) FROM customers WHERE (data::jsonb ->> 'taxId') = $1 " +
				"AND data_type_id = $2 AND tenant_id = $3",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID},
		},
		{
			name:      "postgres top-level field, with excludeID (update path)",
			dbDriver:  dialect.Postgres,
			field:     taxID,
			excludeID: &excludeID,
			wantQuery: "SELECT count(*) FROM customers WHERE (data::jsonb ->> 'taxId') = $1 " +
				"AND data_type_id = $2 AND tenant_id = $3 AND id != $4",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID, excludeID},
		},
		{
			name:      "postgres nested field chains -> then ->>",
			dbDriver:  dialect.Postgres,
			field:     userEmail,
			excludeID: nil,
			wantQuery: "SELECT count(*) FROM customers WHERE (data::jsonb -> 'user' ->> 'email') = $1 " +
				"AND data_type_id = $2 AND tenant_id = $3",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID},
		},
		{
			name:      "sqlite top-level field, no excludeID",
			dbDriver:  dialect.SQLite,
			field:     taxID,
			excludeID: nil,
			wantQuery: "SELECT count(*) FROM customers WHERE json_extract(data, '$.taxId') = ? " +
				"AND data_type_id = ? AND tenant_id = ?",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID},
		},
		{
			name:      "sqlite top-level field, with excludeID",
			dbDriver:  dialect.SQLite,
			field:     taxID,
			excludeID: &excludeID,
			wantQuery: "SELECT count(*) FROM customers WHERE json_extract(data, '$.taxId') = ? " +
				"AND data_type_id = ? AND tenant_id = ? AND id != ?",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID, excludeID},
		},
		{
			name:      "sqlite nested field uses dotted json path",
			dbDriver:  dialect.SQLite,
			field:     userEmail,
			excludeID: nil,
			wantQuery: "SELECT count(*) FROM customers WHERE json_extract(data, '$.user.email') = ? " +
				"AND data_type_id = ? AND tenant_id = ?",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID},
		},
		{
			name:      "empty dialect falls back to sqlite syntax (test default)",
			dbDriver:  "",
			field:     taxID,
			excludeID: nil,
			wantQuery: "SELECT count(*) FROM customers WHERE json_extract(data, '$.taxId') = ? " +
				"AND data_type_id = ? AND tenant_id = ?",
			wantArgs: []any{"VAT-1", dataTypeID, tenantID},
		},
	}

	v := NewValidator(nil)
	ctx := tenant.Context(t.Context(), tenantID)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			query, args, err := v.createQueryForCountingUniqueRecords(
				ctx, tt.dbDriver, "customers", "data", tt.field, "VAT-1", dataTypeID, tt.excludeID)
			require.NoError(t, err)

			assert.Equal(t, tt.wantQuery, query)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// TestCreateQueryForCountingUniqueRecords_UnsupportedDialect ensures an unknown
// driver is rejected rather than silently producing malformed SQL.
func TestCreateQueryForCountingUniqueRecords_UnsupportedDialect(t *testing.T) {
	t.Parallel()

	v := NewValidator(nil)
	tenantID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ctx := tenant.Context(t.Context(), tenantID)
	root := Field{}
	field := Field{Name: "taxId", Type: "string", Unique: true, Parent: &root}

	_, _, err := v.createQueryForCountingUniqueRecords(
		ctx, "mysql", "customers", "data", field, "VAT-1", uuid.New(), nil)
	require.ErrorIs(t, err, ErrUnsupportedDialect)
}
