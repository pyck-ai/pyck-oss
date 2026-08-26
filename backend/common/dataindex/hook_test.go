package dataindex_test

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// fakeMutation implements just the slice of ent.Mutation the hook uses; the
// embedded nil provides the rest, which the hook never calls.
type fakeMutation struct {
	ent.Mutation
	op      ent.Op
	set     map[string]ent.Value
	cleared map[string]struct{}
	old     map[string]ent.Value
	oldErr  map[string]error
}

func newFakeMutation(op ent.Op) *fakeMutation {
	return &fakeMutation{
		op:      op,
		set:     map[string]ent.Value{},
		cleared: map[string]struct{}{},
		old:     map[string]ent.Value{},
	}
}

func (m *fakeMutation) Op() ent.Op   { return m.op }
func (m *fakeMutation) Type() string { return "Order" }

func (m *fakeMutation) Field(name string) (ent.Value, bool) {
	v, ok := m.set[name]
	return v, ok
}

func (m *fakeMutation) SetField(name string, value ent.Value) error {
	m.set[name] = value
	delete(m.cleared, name)
	return nil
}

func (m *fakeMutation) FieldCleared(name string) bool {
	_, ok := m.cleared[name]
	return ok
}

func (m *fakeMutation) ClearField(name string) error {
	m.cleared[name] = struct{}{}
	delete(m.set, name)
	return nil
}

func (m *fakeMutation) OldField(_ context.Context, name string) (ent.Value, error) {
	if err := m.oldErr[name]; err != nil {
		return nil, err
	}
	v, ok := m.old[name]
	if !ok {
		// ent errors when a field was not loaded; the hook treats that as absent.
		return nil, errNoOldValue
	}
	return v, nil
}

var errNoOldValue = errors.New("no old value")

// clear marks a field cleared, as ClearData() does.
func (m *fakeMutation) clear(name string) { m.cleared[name] = struct{}{} }

// bindingsFor serves two datatypes: "serials" binds a list slot, "plain" binds none.
func testBindingsFor(_ context.Context, _, _ uuid.UUID, slug string) (dataindex.Bindings, error) {
	if slug == "serials" {
		return dataindex.Bindings{"serialNumbers": {Name: "serialNumbers", Source: "/MainItemSerialNumber", Slot: "data_ix_list1"}}, nil
	}
	return dataindex.Bindings{}, nil
}

func runHook(t *testing.T, m *fakeMutation) error {
	t.Helper()
	var reached bool
	next := ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) {
		reached = true
		return struct{}{}, nil // ent returns the entity here; the hook ignores it
	})
	_, err := dataindex.ProjectHook(testBindingsFor)(next).Mutate(context.Background(), m)
	if err == nil {
		assert.True(t, reached, "hook must call next on success")
	}
	return err
}

func TestProjectHook(t *testing.T) {
	t.Parallel()

	t.Run("setting data projects the slot", func(t *testing.T) {
		t.Parallel()
		m := newFakeMutation(ent.OpUpdateOne)
		m.set[mixin.DataFieldDataTypeSlug] = "serials"
		m.set[mixin.DataFieldData] = map[string]any{"MainItemSerialNumber": []any{"SN-1", "SN-2"}}
		require.NoError(t, runHook(t, m))
		assert.Equal(t, []string{"SN-1", "SN-2"}, m.set["data_ix_list1"])
	})

	t.Run("clearing data empties the slot", func(t *testing.T) {
		t.Parallel()
		// The bug: a cleared data field reported not-ok, so the hook skipped and the
		// slot kept answering for a key the row no longer has.
		m := newFakeMutation(ent.OpUpdateOne)
		m.old[mixin.DataFieldDataTypeSlug] = "serials"
		m.clear(mixin.DataFieldData)
		require.NoError(t, runHook(t, m))
		assert.Equal(t, []string{}, m.set["data_ix_list1"])
	})

	t.Run("clearing the datatype retires the slot", func(t *testing.T) {
		t.Parallel()
		// The row drops its datatype: nothing owns the binding any more, so the
		// slot must be cleared. Falling back to the stored slug instead would
		// re-project under bindings the row no longer has, and the backfill --
		// which matches on data_type_slug, now NULL -- could never repair it.
		m := newFakeMutation(ent.OpUpdateOne)
		m.old[mixin.DataFieldDataTypeSlug] = "serials"
		m.old[mixin.DataFieldData] = map[string]any{"MainItemSerialNumber": []any{"SN-1"}}
		m.clear(mixin.DataFieldDataTypeSlug)

		require.NoError(t, runHook(t, m))
		_, projected := m.set["data_ix_list1"]
		assert.False(t, projected, "slot must not be projected under a datatype the row dropped")
		assert.Contains(t, m.cleared, "data_ix_list1", "the retired slot must be cleared")
	})

	t.Run("a bulk data clear is rejected, not silently skipped", func(t *testing.T) {
		t.Parallel()
		m := newFakeMutation(ent.OpUpdate)
		m.clear(mixin.DataFieldData)
		require.ErrorIs(t, runHook(t, m), dataindex.ErrBulkDataMutation)
	})

	t.Run("a bulk datatype change is rejected", func(t *testing.T) {
		t.Parallel()
		m := newFakeMutation(ent.OpUpdate)
		m.set[mixin.DataFieldDataTypeSlug] = "serials"
		require.ErrorIs(t, runHook(t, m), dataindex.ErrBulkDataMutation)
	})

	t.Run("a datatype change re-projects from the stored data", func(t *testing.T) {
		t.Parallel()
		// Data untouched, datatype set to one that binds the slot: the slot must be
		// filled from what is stored, not left empty.
		m := newFakeMutation(ent.OpUpdateOne)
		m.set[mixin.DataFieldDataTypeSlug] = "serials"
		m.old[mixin.DataFieldData] = map[string]any{"MainItemSerialNumber": []any{"SN-9"}}
		require.NoError(t, runHook(t, m))
		assert.Equal(t, []string{"SN-9"}, m.set["data_ix_list1"])
	})

	t.Run("moving to a datatype without the binding clears the retired slot", func(t *testing.T) {
		t.Parallel()
		// Row was on "serials" (binds list1); moving to "plain" (binds nothing) must
		// clear list1 so the row stops matching under the old mapping.
		m := newFakeMutation(ent.OpUpdateOne)
		m.old[mixin.DataFieldDataTypeSlug] = "serials"
		m.set[mixin.DataFieldDataTypeSlug] = "plain"
		m.old[mixin.DataFieldData] = map[string]any{"MainItemSerialNumber": []any{"SN-9"}}
		require.NoError(t, runHook(t, m))
		assert.True(t, m.FieldCleared("data_ix_list1"), "retired slot must be cleared")
	})

	t.Run("a mutation touching neither data nor datatype is left alone", func(t *testing.T) {
		t.Parallel()
		m := newFakeMutation(ent.OpUpdateOne)
		m.set["some_other_field"] = "x"
		require.NoError(t, runHook(t, m))
		_, touched := m.set["data_ix_list1"]
		assert.False(t, touched)
	})
}

// lineageEmpty is a LineageBindings that reports no prior versions.
func lineageEmpty(context.Context, uuid.UUID, string) (dataindex.Bindings, error) {
	return dataindex.Bindings{}, nil
}

func runValidateHook(t *testing.T, m *fakeMutation) error {
	t.Helper()
	next := ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) {
		return struct{}{}, nil
	})
	_, err := dataindex.ValidateHook(testDataTypeFields, lineageEmpty)(next).Mutate(context.Background(), m)
	return err
}

func TestValidateHookFailsClosed(t *testing.T) {
	t.Parallel()

	t.Run("a stored-schema read failure blocks the update", func(t *testing.T) {
		t.Parallel()
		// The frozen check cannot run without the previous schema; failing open
		// here would let an update silently rebind a slot.
		m := newFakeMutation(ent.OpUpdateOne)
		m.set["json_schema"] = `{"x-indices":{"a":{"source":"/a","slot":"data_ix_text1"}}}`
		m.set["entity"] = dataindex.EntityPickingOrder
		m.oldErr = map[string]error{"json_schema": errReadFailed}
		require.ErrorIs(t, runValidateHook(t, m), errReadFailed)
	})

	t.Run("a create with no prior schema is allowed", func(t *testing.T) {
		t.Parallel()
		m := newFakeMutation(ent.OpCreate)
		m.set["json_schema"] = `{"x-indices":{"a":{"source":"/a","slot":"data_ix_text1"}}}`
		m.set["entity"] = dataindex.EntityPickingOrder
		require.NoError(t, runValidateHook(t, m))
	})
}

var errReadFailed = errors.New("database read failed")

// testDataTypeFields mirrors management's generated column names.
var testDataTypeFields = dataindex.DataTypeFields{
	JSONSchema: "json_schema",
	Entity:     "entity",
	Slug:       "slug",
}
