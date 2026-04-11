//go:build !skippolicy

package schema

import (
	"entgo.io/ent"
	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
)

func (Group) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			privacy.AllowIfReader(),
			privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			privacy.AllowIfAdmin(),
			privacy.AlwaysDenyRule(),
		},
	}
}
