package dataindex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
)

func TestBackfillSQL(t *testing.T) {
	t.Parallel()

	t.Run("a list slot rebuilds each element the way Project does", func(t *testing.T) {
		t.Parallel()
		got, err := dataindex.BackfillSQL("picking", "orders",
			dataindex.Binding{Source: "/MainItemSerialNumber", Slot: "data_ix_list1"}, 500)
		require.NoError(t, err)
		// Numbers render via float8::text (shortest round-trip, matching Go's
		// FormatFloat) then ::numeric::text to strip the exponent; a straight
		// float8::numeric would lose precision to 15 significant digits.
		assert.Contains(t, got, "((e#>>'{}')::float8::text)::numeric::text")
		assert.Contains(t, got, "{MainItemSerialNumber}")
		// Without this guard a non-array payload aborts the whole pass, since
		// jsonb_array_elements rejects a scalar.
		assert.Contains(t, got, "jsonb_typeof(data #> '{MainItemSerialNumber}') = 'array'")
	})

	t.Run("a scalar slot only matches its own JSON type", func(t *testing.T) {
		t.Parallel()
		// A JSON null passes an IS NOT NULL guard but extracts to SQL NULL, so the
		// row would be rewritten on every pass and never settle.
		for slot, jsonType := range map[string]string{
			"data_ix_text1":    "string",
			"data_ix_numeric1": "number",
			"data_ix_bool1":    "boolean",
		} {
			got, err := dataindex.BackfillSQL("picking", "orders",
				dataindex.Binding{Source: "/f", Slot: slot}, 500)
			require.NoError(t, err)
			assert.Contains(t, got, "jsonb_typeof(data #> '{f}') = '"+jsonType+"'", slot)
		}
	})

	t.Run("a scalar slot extracts the value directly", func(t *testing.T) {
		t.Parallel()
		got, err := dataindex.BackfillSQL("picking", "orders",
			dataindex.Binding{Source: "/outer/ref", Slot: "data_ix_text1"}, 500)
		require.NoError(t, err)
		assert.Contains(t, got, `data #>> '{outer,ref}'`)
	})

	t.Run("rejects a pointer that could break out of the path literal", func(t *testing.T) {
		t.Parallel()
		_, err := dataindex.BackfillSQL("picking", "orders",
			dataindex.Binding{Source: "/a'}, x = '1", Slot: "data_ix_text1"}, 500)
		require.ErrorIs(t, err, dataindex.ErrBadSourcePath)
	})

	t.Run("rejects an empty pointer", func(t *testing.T) {
		t.Parallel()
		_, err := dataindex.BackfillSQL("picking", "orders",
			dataindex.Binding{Source: "/", Slot: "data_ix_text1"}, 500)
		require.ErrorIs(t, err, dataindex.ErrBadSourcePath)
	})
}

func TestBackfillSQLRejectsUnsafeIdentifiers(t *testing.T) {
	t.Parallel()

	t.Run("a slot that is not a safe identifier is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := dataindex.BackfillSQL("picking", "orders",
			dataindex.Binding{Source: "/x", Slot: `x"); DROP TABLE orders; --`}, 500)
		require.ErrorIs(t, err, dataindex.ErrUnsafeIdentifier)
	})

	t.Run("an unsafe schema or table is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := dataindex.BackfillSQL(`pick"ing`, "orders",
			dataindex.Binding{Source: "/x", Slot: "data_ix_text1"}, 500)
		require.ErrorIs(t, err, dataindex.ErrUnsafeIdentifier)
	})

	t.Run("identifiers are wrapped in Postgres double quotes", func(t *testing.T) {
		t.Parallel()
		got, err := dataindex.BackfillSQL("picking", "orders",
			dataindex.Binding{Source: "/x", Slot: "data_ix_text1"}, 500)
		require.NoError(t, err)
		assert.Contains(t, got, `"picking"."orders"`)
		assert.Contains(t, got, `"data_ix_text1"`)
	})
}

func TestSQLJSONPathRejectsComma(t *testing.T) {
	t.Parallel()
	// A comma would splice one key into two path elements in the {a,b} literal.
	_, err := dataindex.BackfillSQL("picking", "orders",
		dataindex.Binding{Source: "/a,b", Slot: "data_ix_text1"}, 500)
	require.ErrorIs(t, err, dataindex.ErrBadSourcePath)
}
