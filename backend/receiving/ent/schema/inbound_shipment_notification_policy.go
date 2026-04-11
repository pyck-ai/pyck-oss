//go:build !skippolicy

package schema

import (
	"entgo.io/ent"

	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
)

func (InboundShipmentNotification) Policy() ent.Policy { //nolint:ireturn
	return privacy.Policy{
		Mutation: privacy.MutationPolicy{
			privacy.AllowIfWriter(),
			privacy.AlwaysDenyRule(),
		},
		Query: privacy.QueryPolicy{
			privacy.AllowIfReader(),
			privacy.AlwaysDenyRule(),
		},
	}
}
