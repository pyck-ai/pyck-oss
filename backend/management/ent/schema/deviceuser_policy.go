//go:build !skippolicy

package schema

import (
	"entgo.io/ent"

	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
)

func (DeviceUser) Policy() ent.Policy { //nolint:ireturn
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			privacy.AllowIfReader(),
			privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			privacy.AllowIfWriter(),
			privacy.AlwaysDenyRule(),
		},
	}
}
