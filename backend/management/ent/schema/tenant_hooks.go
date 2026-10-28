//go:build !skiphooks

package schema

import (
	"context"
	"errors"

	entgo "entgo.io/ent"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enthook "github.com/pyck-ai/pyck/backend/management/ent/gen/hook"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
)

// ErrTenantNameKeyWithoutName refuses a tenant mutation that sets name_key
// without name: the key is derived from the name and nothing else may
// write it.
var ErrTenantNameKeyWithoutName = errors.New("tenant name_key is derived from name and cannot be set on its own")

// Hooks derive name_key from name on every create and update that sets
// name, so the tenant_name_key unique index compares every row by the same
// fold whichever path wrote it (resolvers, workflows, fixtures, tests).
func (Tenant) Hooks() []entgo.Hook {
	return []entgo.Hook{
		enthook.On(func(next entgo.Mutator) entgo.Mutator {
			return enthook.TenantFunc(func(ctx context.Context, m *ent.TenantMutation) (ent.Value, error) {
				name, ok := m.Name()
				if !ok {
					if _, keySet := m.NameKey(); keySet {
						return nil, ErrTenantNameKeyWithoutName
					}
					return next.Mutate(ctx, m)
				}
				m.SetNameKey(types.TenantNameKey(name))
				return next.Mutate(ctx, m)
			})
		}, entgo.OpCreate|entgo.OpUpdate|entgo.OpUpdateOne),
	}
}
