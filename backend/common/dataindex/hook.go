package dataindex

import (
	"context"
	"errors"
	"fmt"

	"entgo.io/ent"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// ErrBulkDataMutation is returned when a bulk update carries a data change. A
// bulk mutation has no per-row payload, so slots cannot be projected for it;
// failing is the only safe outcome, since skipping would leave the index stale.
var ErrBulkDataMutation = errors.New("bulk data mutation cannot maintain indexed slots; update rows individually")

// BindingsFor resolves the x-indices of the datatype a row is being written
// against. Services supply this from their datatype cache.
type BindingsFor func(ctx context.Context, tenantID, dataTypeID uuid.UUID, dataTypeSlug string) (Bindings, error)

// dataMutation is the slice of an ent mutation the projector needs. Every
// generated mutation satisfies it.
type dataMutation interface {
	ent.Mutation
	Field(name string) (ent.Value, bool)
	SetField(name string, value ent.Value) error
}

// oldFielder reads a field's stored value. Update mutations only carry the
// fields being written, so the datatype an existing row was created with has to
// come from the row itself.
type oldFielder interface {
	OldField(ctx context.Context, name string) (ent.Value, error)
}

// ProjectHook keeps the indexed slots in step with data. It runs inside the
// mutation's transaction, so a slot is written with the row it derives from and
// cannot drift, and since slots are hidden from mutation inputs it is the only
// writer.
//
// It fires on any change affecting a slot: setting data re-projects, clearing it
// clears the slots, and a datatype change re-projects under the new bindings and
// clears any slot only the old one bound. Skipping either would strand slots
// answering for a key the row no longer has.
func ProjectHook(bindingsFor BindingsFor) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			dm, ok := m.(dataMutation)
			if !ok {
				return next.Mutate(ctx, m)
			}

			dataChanged := fieldTouched(dm, mixin.DataFieldData)
			typeChanged := fieldTouched(dm, mixin.DataFieldDataTypeID) ||
				fieldTouched(dm, mixin.DataFieldDataTypeSlug)
			if !dataChanged && !typeChanged {
				return next.Mutate(ctx, m)
			}

			// OpUpdate (as opposed to OpUpdateOne) is the bulk form: one statement
			// over many rows, with no row to project from. Any slot-affecting change
			// in that form fails rather than silently leaving the index stale.
			if m.Op().Is(ent.OpUpdate) {
				return nil, fmt.Errorf("%w (type %s)", ErrBulkDataMutation, m.Type())
			}

			data := effectiveData(ctx, dm)

			tenantID, _ := fieldAs[uuid.UUID](ctx, dm, mixin.TenantFieldTenantID)
			dataTypeID, _ := fieldAs[uuid.UUID](ctx, dm, mixin.DataFieldDataTypeID)
			dataTypeSlug, _ := fieldAs[string](ctx, dm, mixin.DataFieldDataTypeSlug)

			bindings, err := bindingsFor(ctx, tenantID, dataTypeID, dataTypeSlug)
			if err != nil {
				return nil, fmt.Errorf("resolve data indices: %w", err)
			}

			// A datatype change may have moved off slots the new bindings no longer
			// cover; clear those so the row stops matching under the old mapping.
			if typeChanged {
				if err := clearRetiredSlots(ctx, dm, bindingsFor, bindings); err != nil {
					return nil, err
				}
			}

			for _, b := range bindings {
				if err := projectSlot(dm, b, data); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	}
}

// effectiveData returns the payload the slots must reflect after this mutation:
// the new value when data is set, nil when it is cleared, and the stored value
// when only the datatype changed. A nil result clears every projected slot.
func effectiveData(ctx context.Context, dm dataMutation) map[string]any {
	if dm.FieldCleared(mixin.DataFieldData) {
		return nil
	}
	if raw, ok := dm.Field(mixin.DataFieldData); ok {
		data, _ := raw.(map[string]any)
		return data
	}
	// Data untouched (a bare datatype change): re-project from what is stored.
	data, _ := oldValueAs[map[string]any](ctx, dm, mixin.DataFieldData)
	return data
}

// clearRetiredSlots clears every slot the row's previous datatype bound that the
// new bindings do not, so a datatype change cannot leave a slot projected under
// the old mapping.
func clearRetiredSlots(ctx context.Context, dm dataMutation, bindingsFor BindingsFor, next Bindings) error {
	oldTenant, _ := oldValueAs[uuid.UUID](ctx, dm, mixin.TenantFieldTenantID)
	oldID, _ := oldValueAs[uuid.UUID](ctx, dm, mixin.DataFieldDataTypeID)
	oldSlug, _ := oldValueAs[string](ctx, dm, mixin.DataFieldDataTypeSlug)

	old, err := bindingsFor(ctx, oldTenant, oldID, oldSlug)
	if err != nil {
		return fmt.Errorf("resolve previous data indices: %w", err)
	}
	stillBound := make(map[string]struct{}, len(next))
	for _, b := range next {
		stillBound[b.Slot] = struct{}{}
	}
	for _, b := range old {
		if _, kept := stillBound[b.Slot]; kept {
			continue
		}
		if err := dm.ClearField(b.Slot); err != nil {
			return fmt.Errorf("clear retired slot %q: %w", b.Slot, err)
		}
	}
	return nil
}

// projectSlot writes one binding's slot from data: the projected value, or a
// cleared scalar / emptied list when the key is absent so the row stops matching.
func projectSlot(dm dataMutation, b Binding, data map[string]any) error {
	value, err := Project(data, b)
	if err != nil {
		return fmt.Errorf("project index %q: %w", b.Name, err)
	}
	if value == nil {
		if b.Kind() != mixin.SlotKindList {
			if err := dm.ClearField(b.Slot); err != nil {
				return fmt.Errorf("clear slot %q: %w", b.Slot, err)
			}
			return nil
		}
		value = []string{}
	}
	if err := dm.SetField(b.Slot, value); err != nil {
		return fmt.Errorf("set slot %q: %w", b.Slot, err)
	}
	return nil
}

// fieldTouched reports whether the mutation sets or clears a field.
func fieldTouched(dm dataMutation, name string) bool {
	if _, ok := dm.Field(name); ok {
		return true
	}
	return dm.FieldCleared(name)
}

// fieldAs reads a field from the mutation, falling back to the stored value when
// the mutation does not carry it -- which is the normal case for an update that
// only writes data.
func fieldAs[T any](ctx context.Context, m dataMutation, name string) (T, bool) {
	if v, ok := m.Field(name); ok {
		typed, ok := v.(T)
		return typed, ok
	}
	// A cleared field is set to its zero value, not left alone: falling back to
	// the stored value would resolve bindings the row no longer has and project
	// slots under a datatype it just dropped.
	if m.FieldCleared(name) {
		var zero T
		return zero, true
	}
	return oldValueAs[T](ctx, m, name)
}

// oldValueAs reads a field's stored value, ignoring what the mutation now sets --
// used to resolve the datatype a row is moving away from.
func oldValueAs[T any](ctx context.Context, m dataMutation, name string) (T, bool) {
	var zero T
	old, ok := m.(oldFielder)
	if !ok {
		return zero, false
	}
	v, err := old.OldField(ctx, name)
	if err != nil {
		return zero, false
	}
	typed, ok := v.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}
