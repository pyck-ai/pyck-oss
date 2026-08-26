package dataindex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

const schemaWithIndices = `{
  "type": "object",
  "properties": {
    "customerRef":   { "type": "string" },
    "serialNumbers": { "type": "array", "items": { "type": "string" } }
  },
  "x-indices": {
    "customerRef":   { "source": "/customerRef",   "slot": "data_ix_text1" },
    "serialNumbers": { "source": "/serialNumbers", "slot": "data_ix_list1" }
  }
}`

func pool() dataindex.SlotPool {
	return dataindex.NewSlotPool(mixin.DataIndexMixin{Text: 2, Numeric: 1, Bool: 1, List: 2})
}

func TestParse(t *testing.T) {
	t.Parallel()

	t.Run("reads bindings and back-fills the name", func(t *testing.T) {
		t.Parallel()
		b, err := dataindex.Parse(schemaWithIndices)
		require.NoError(t, err)
		require.Len(t, b, 2)
		assert.Equal(t, "/serialNumbers", b["serialNumbers"].Source)
		assert.Equal(t, "data_ix_list1", b["serialNumbers"].Slot)
		assert.Equal(t, "serialNumbers", b["serialNumbers"].Name)
	})

	t.Run("a schema without the block has no indexed keys", func(t *testing.T) {
		t.Parallel()
		b, err := dataindex.Parse(`{"type":"object","properties":{}}`)
		require.NoError(t, err)
		assert.Empty(t, b)
	})

	t.Run("empty schema is not an error", func(t *testing.T) {
		t.Parallel()
		b, err := dataindex.Parse("")
		require.NoError(t, err)
		assert.Empty(t, b)
	})
}

func TestBindingKind(t *testing.T) {
	t.Parallel()
	for slot, want := range map[string]string{
		"data_ix_text1":     mixin.SlotKindText,
		"data_ix_numeric12": mixin.SlotKindNumeric,
		"data_ix_bool2":     mixin.SlotKindBool,
		"data_ix_list1":     mixin.SlotKindList,
		"nonsense":          "",
	} {
		assert.Equal(t, want, dataindex.Binding{Slot: slot}.Kind(), slot)
	}
}

func TestProject(t *testing.T) {
	t.Parallel()

	t.Run("list slot renders every element as a string", func(t *testing.T) {
		t.Parallel()
		got, err := dataindex.Project(
			map[string]any{"serialNumbers": []any{"SN-1", float64(123), "SN-2"}},
			dataindex.Binding{Source: "/serialNumbers", Slot: "data_ix_list1"},
		)
		require.NoError(t, err)
		// A numeric serial must render like the string a caller queries with.
		assert.Equal(t, []string{"SN-1", "123", "SN-2"}, got)
	})

	t.Run("nested source is reachable", func(t *testing.T) {
		t.Parallel()
		got, err := dataindex.Project(
			map[string]any{"outer": map[string]any{"ref": "ACME-42"}},
			dataindex.Binding{Source: "/outer/ref", Slot: "data_ix_text1"},
		)
		require.NoError(t, err)
		assert.Equal(t, "ACME-42", got)
	})

	t.Run("missing path clears the slot", func(t *testing.T) {
		t.Parallel()
		got, err := dataindex.Project(
			map[string]any{"other": "x"},
			dataindex.Binding{Source: "/customerRef", Slot: "data_ix_text1"},
		)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("numeric slot rejects a numeric string", func(t *testing.T) {
		t.Parallel()
		// The backfill's jsonb_typeof='number' guard cannot reproduce a string, so
		// accepting one here would index a row the backfill would leave empty.
		_, err := dataindex.Project(
			map[string]any{"n": "42.5"},
			dataindex.Binding{Source: "/n", Slot: "data_ix_numeric1"},
		)
		require.ErrorIs(t, err, dataindex.ErrNotAssignable)
	})

	t.Run("list normalizes numbers the way the backfill does", func(t *testing.T) {
		t.Parallel()
		// 1.50 and 123.0 arrive as 1.5 and 123 after the JSON decode, and the
		// backfill reaches the same text via float8. Empty and null drop on both.
		got, err := dataindex.Project(
			map[string]any{"l": []any{"SN-1", 1.5, float64(123), true, "", nil}},
			dataindex.Binding{Source: "/l", Slot: "data_ix_list1"},
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"SN-1", "1.5", "123", "true"}, got)
	})

	t.Run("list renders extreme-magnitude numbers in plain notation", func(t *testing.T) {
		t.Parallel()
		// A fast, DB-free pin on the Go side (the pg-backed test proves it agrees
		// with the SQL). Plain notation at every magnitude; 2^63 must not overflow.
		got, err := dataindex.Project(
			map[string]any{"l": []any{1e20, 1e-10, 9223372036854775808.0}},
			dataindex.Binding{Source: "/l", Slot: "data_ix_list1"},
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"100000000000000000000", "0.0000000001", "9223372036854776000"}, got)
	})

	t.Run("list rejects a non-scalar element", func(t *testing.T) {
		t.Parallel()
		// An object has no shared Go/SQL rendering, so it must never reach a slot.
		_, err := dataindex.Project(
			map[string]any{"l": []any{"SN-1", map[string]any{"a": 1}}},
			dataindex.Binding{Source: "/l", Slot: "data_ix_list1"},
		)
		require.ErrorIs(t, err, dataindex.ErrNotAssignable)
	})

	t.Run("wrong shape is rejected rather than silently skipped", func(t *testing.T) {
		t.Parallel()
		_, err := dataindex.Project(
			map[string]any{"serialNumbers": "not-a-list"},
			dataindex.Binding{Source: "/serialNumbers", Slot: "data_ix_list1"},
		)
		require.ErrorIs(t, err, dataindex.ErrNotAssignable)
	})
}

func TestValidate(t *testing.T) {
	t.Parallel()
	typeAt := func(p string) string {
		switch p {
		case "/customerRef":
			return "string"
		case "/serialNumbers":
			return "array"
		}
		return ""
	}

	t.Run("accepts a fresh, well-formed binding set", func(t *testing.T) {
		t.Parallel()
		next, err := dataindex.Parse(schemaWithIndices)
		require.NoError(t, err)
		require.NoError(t, dataindex.Validate(nil, next, pool(), typeAt))
	})

	t.Run("rejects a slot the entity does not have", func(t *testing.T) {
		t.Parallel()
		next := dataindex.Bindings{"x": {Name: "x", Source: "/x", Slot: "data_ix_text9"}}
		require.ErrorIs(t, dataindex.Validate(nil, next, pool(), nil), dataindex.ErrUnknownSlot)
	})

	t.Run("rejects two indices sharing a slot", func(t *testing.T) {
		t.Parallel()
		next := dataindex.Bindings{
			"a": {Name: "a", Source: "/a", Slot: "data_ix_text1"},
			"b": {Name: "b", Source: "/b", Slot: "data_ix_text1"},
		}
		require.ErrorIs(t, dataindex.Validate(nil, next, pool(), nil), dataindex.ErrSlotCollision)
	})

	t.Run("rejects an array source bound to a text slot", func(t *testing.T) {
		t.Parallel()
		next := dataindex.Bindings{"serialNumbers": {Name: "serialNumbers", Source: "/serialNumbers", Slot: "data_ix_text1"}}
		require.ErrorIs(t, dataindex.Validate(nil, next, pool(), typeAt), dataindex.ErrNotAssignable)
	})

	t.Run("rejects rebinding an existing index", func(t *testing.T) {
		t.Parallel()
		old := dataindex.Bindings{"ref": {Name: "ref", Source: "/customerRef", Slot: "data_ix_text1"}}
		next := dataindex.Bindings{"ref": {Name: "ref", Source: "/customerRef", Slot: "data_ix_text2"}}
		require.ErrorIs(t, dataindex.Validate(old, next, pool(), nil), dataindex.ErrBindingFrozen)
	})

	t.Run("rejects removing a binding, even to reuse its slot", func(t *testing.T) {
		t.Parallel()
		// Hole 1: dropping A and adding B on A's slot in one save. The lineage keeps
		// A in old, so this is a removal -- rejected -- not a silent rebind.
		old := dataindex.Bindings{"a": {Name: "a", Source: "/a", Slot: "data_ix_text1"}}
		next := dataindex.Bindings{"b": {Name: "b", Source: "/b", Slot: "data_ix_text1"}}
		require.ErrorIs(t, dataindex.Validate(old, next, pool(), nil), dataindex.ErrBindingFrozen)
	})

	t.Run("rejects dropping a binding outright", func(t *testing.T) {
		t.Parallel()
		old := dataindex.Bindings{"ref": {Name: "ref", Source: "/customerRef", Slot: "data_ix_text1"}}
		require.ErrorIs(t, dataindex.Validate(old, dataindex.Bindings{}, pool(), nil), dataindex.ErrBindingFrozen)
	})

	t.Run("allows adding a new index alongside a frozen one", func(t *testing.T) {
		t.Parallel()
		old := dataindex.Bindings{"ref": {Name: "ref", Source: "/customerRef", Slot: "data_ix_text1"}}
		next := dataindex.Bindings{
			"ref":           {Name: "ref", Source: "/customerRef", Slot: "data_ix_text1"},
			"serialNumbers": {Name: "serialNumbers", Source: "/serialNumbers", Slot: "data_ix_list1"},
		}
		require.NoError(t, dataindex.Validate(old, next, pool(), typeAt))
	})
}

func TestProjectClearsFromEmptyPayload(t *testing.T) {
	t.Parallel()
	// Clearing data must clear the slots; a nil payload is the extreme of that.
	for name, data := range map[string]map[string]any{
		"nil payload":   nil,
		"empty payload": {},
	} {
		got, err := dataindex.Project(data, dataindex.Binding{Source: "/serialNumbers", Slot: "data_ix_list1"})
		require.NoError(t, err, name)
		assert.Nil(t, got, name)
	}
}

func TestValidateRejectsUnsafeSource(t *testing.T) {
	t.Parallel()
	// A comma or whitespace source diverges between the hook (literal key) and the
	// backfill (path array), so it must not be creatable.
	for name, src := range map[string]string{
		"comma":          "/foo,bar",
		"trailing space": "/foo ",
		"tab":            "/foo\tbar",
	} {
		next := dataindex.Bindings{"x": {Name: "x", Source: src, Slot: "data_ix_text1"}}
		require.ErrorIs(t, dataindex.Validate(nil, next, pool(), nil), dataindex.ErrBadSourcePath, name)
	}
}
