package dataindex

import (
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// SchemaTypeAt reports the JSONSchema type declared at a payload path, so a
// binding can be checked against the shape it claims to index. It returns an
// empty string when the path is not declared.
type SchemaTypeAt func(pointer string) string

// Validate checks a datatype's new x-indices against every binding its slug has
// ever declared (old is the union across all versions, including soft-deleted).
//
// The contract: a slot, once bound, keeps its (name, source) for the life of the
// entity, because rows indexed under it live in another service's database and
// cannot be re-checked here. So a binding can only be added, never changed or
// removed: removing one would let a new binding claim a slot old rows still fill,
// and a query could no longer trust what a slot means.
//
// typeAt is optional; when nil, the assignability check is skipped.
func Validate(old, next Bindings, pool SlotPool, typeAt SchemaTypeAt) error {
	bySlot := make(map[string]string, len(next))
	for name, ix := range next {
		if !pool.Has(ix.Slot) {
			return fmt.Errorf("index %q: %w %q", name, ErrUnknownSlot, ix.Slot)
		}
		// Reject a source the backfill could not index the same way the hook reads
		// it, at save time, so no binding can be created that the two paths would
		// disagree on.
		if _, err := pointerSegments(ix.Source); err != nil {
			return fmt.Errorf("index %q: %w", name, err)
		}
		if prev, dup := bySlot[ix.Slot]; dup {
			return fmt.Errorf("%w: slot %q bound by both %q and %q", ErrSlotCollision, ix.Slot, prev, name)
		}
		bySlot[ix.Slot] = name

		if typeAt != nil {
			if declared := typeAt(ix.Source); declared != "" && !assignable(declared, ix.Kind()) {
				return fmt.Errorf("index %q: %w (%s is %s, slot %s is %s)",
					name, ErrNotAssignable, ix.Source, declared, ix.Slot, ix.Kind())
			}
		}
	}

	// Every binding the slug has ever had must survive unchanged. Keeping them in
	// next is also what makes the collision check above see the occupied slots, so
	// a new binding cannot claim one.
	for name, prev := range old {
		cur, kept := next[name]
		switch {
		case !kept:
			return fmt.Errorf("index %q: %w (removed; its slot %q may still hold rows)",
				name, ErrBindingFrozen, prev.Slot)
		case cur.Slot != prev.Slot || cur.Source != prev.Source:
			return fmt.Errorf("index %q: %w (%s->%s cannot become %s->%s)",
				name, ErrBindingFrozen, prev.Source, prev.Slot, cur.Source, cur.Slot)
		}
	}
	return nil
}

// assignable reports whether a JSONSchema type can feed a slot kind.
func assignable(schemaType, slotKind string) bool {
	switch slotKind {
	case mixin.SlotKindText:
		return schemaType == "string"
	case mixin.SlotKindNumeric:
		return schemaType == "number" || schemaType == "integer"
	case mixin.SlotKindBool:
		return schemaType == "boolean"
	case mixin.SlotKindList:
		return schemaType == "array"
	default:
		return false
	}
}
