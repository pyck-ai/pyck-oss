//go:build !skippolicy

package schema

import (
	"entgo.io/ent"
	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
)

func (Collection_Movement) Policy() ent.Policy {
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
