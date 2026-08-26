package dataindex

import (
	"context"
	"errors"
	"fmt"

	"entgo.io/ent"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// ErrBulkDataTypeMutation is returned when a bulk update carries a schema change.
// The frozen-binding contract is a diff against each row's previous schema, which
// a bulk statement has no way to supply.
var ErrBulkDataTypeMutation = errors.New("bulk datatype update cannot be validated; update rows individually")

// ErrSchemaNotString is returned when the datatype's schema field does not hold
// a string. The frozen-binding contract is a diff of two parsed schemas, so a
// value it cannot read is refused rather than waved through.
var ErrSchemaNotString = errors.New("datatype schema field is not a string")

// DataTypeFields names the datatype columns the contract is checked against.
// The owning service passes its generated constants, so a column rename breaks
// the build instead of silently disabling the hook.
type DataTypeFields struct {
	JSONSchema string
	Entity     string
	Slug       string
}

// LineageBindings returns every binding a (tenant, slug) has ever declared,
// unioned across all datatype versions including soft-deleted ones. A datatype's
// slug is freed when it is soft-deleted -- so a recreated slug inherits the old
// slots' meaning, and the check has to see the retired versions to enforce it.
type LineageBindings func(ctx context.Context, tenantID uuid.UUID, slug string) (Bindings, error)

// ValidateHook enforces the frozen-binding contract when a datatype is written.
//
// Register it on the datatype type only, e.g.
// client.DataType.Use(ValidateHook(lineage)). Rejecting here is what lets a query
// trust that a slot's meaning never changed under rows already indexed with it.
func ValidateHook(fields DataTypeFields, lineage LineageBindings) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			dm, ok := m.(dataMutation)
			if !ok {
				return next.Mutate(ctx, m)
			}
			rawSchema, hasSchema := dm.Field(fields.JSONSchema)
			if !hasSchema {
				// The bindings live in json_schema, so nothing else can change them.
				return next.Mutate(ctx, m)
			}
			if m.Op().Is(ent.OpUpdate) {
				return nil, fmt.Errorf("%w (type %s)", ErrBulkDataTypeMutation, m.Type())
			}
			nextSchema, ok := rawSchema.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %s is %T", ErrSchemaNotString, fields.JSONSchema, rawSchema)
			}

			nextBindings, err := Parse(nextSchema)
			if err != nil {
				return nil, err
			}
			entity, _ := fieldAs[string](ctx, dm, fields.Entity)
			pool, hasPool := PoolFor(entity)
			if !hasPool {
				// An entity without slots can still be saved; it just cannot
				// declare an index.
				if len(nextBindings) > 0 {
					return nil, fmt.Errorf("%w: entity %q has no indexed slots", ErrUnknownSlot, entity)
				}
				return next.Mutate(ctx, m)
			}

			tenantID, _ := fieldAs[uuid.UUID](ctx, dm, mixin.TenantFieldTenantID)
			slug, _ := fieldAs[string](ctx, dm, fields.Slug)
			oldBindings, err := lineage(ctx, tenantID, slug)
			if err != nil {
				return nil, fmt.Errorf("read datatype lineage: %w", err)
			}
			if oldBindings == nil {
				oldBindings = Bindings{}
			}
			// Union this row's own committed bindings, which the lineage misses for a
			// slug-less datatype (nothing to key on) and for an in-place edit before
			// the new schema lands.
			if err := mergeCurrentBindings(ctx, dm, fields.JSONSchema, oldBindings); err != nil {
				return nil, err
			}

			typeAt, err := SchemaTypes(nextSchema)
			if err != nil {
				return nil, fmt.Errorf("parse datatype schema: %w", err)
			}
			if err := Validate(oldBindings, nextBindings, pool, typeAt); err != nil {
				return nil, err
			}
			return next.Mutate(ctx, m)
		})
	}
}

// mergeCurrentBindings folds the row's own stored bindings into old.
//
// Only a create has no stored schema. On an update a read or parse failure means
// the frozen check could not be performed, so it is propagated rather than
// collapsed into "no history" -- treating an unreadable schema as empty would let
// the update through and silently rebind a slot.
func mergeCurrentBindings(ctx context.Context, dm dataMutation, schemaField string, old Bindings) error {
	if dm.Op().Is(ent.OpCreate) {
		return nil
	}
	oldFldr, ok := dm.(oldFielder)
	if !ok {
		return nil
	}
	raw, err := oldFldr.OldField(ctx, schemaField)
	if err != nil {
		return fmt.Errorf("read stored schema: %w", err)
	}
	prev, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%w: stored %s is %T", ErrSchemaNotString, schemaField, raw)
	}
	bindings, err := Parse(prev)
	if err != nil {
		return fmt.Errorf("parse stored schema: %w", err)
	}
	for name, ix := range bindings {
		if _, seen := old[name]; !seen {
			old[name] = ix
		}
	}
	return nil
}
