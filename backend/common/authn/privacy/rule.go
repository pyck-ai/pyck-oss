package privacy

import (
	"context"

	entprivacy "entgo.io/ent/privacy"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
)

type (
	Policy            = entprivacy.Policy
	MutationPolicy    = entprivacy.MutationPolicy
	QueryPolicy       = entprivacy.QueryPolicy
	QueryMutationRule = entprivacy.QueryMutationRule
)

func AlwaysAllowRule() QueryMutationRule {
	return entprivacy.AlwaysAllowRule()
}

func AlwaysDenyRule() QueryMutationRule {
	return entprivacy.AlwaysDenyRule()
}

func AllowIfReader() QueryMutationRule {
	return AllowIfRole(authn.ROLE_READER)
}

func DenyIfNoReader() QueryMutationRule {
	return DenyIfNotRole(authn.ROLE_READER)
}

func AllowIfWriter() QueryMutationRule {
	return AllowIfRole(authn.ROLE_WRITER)
}

func DenyIfNoWriter() QueryMutationRule {
	return DenyIfNotRole(authn.ROLE_WRITER)
}

func AllowIfAdmin() QueryMutationRule {
	return AllowIfRole(authn.ROLE_ADMIN)
}

func DenyIfNoAdmin() QueryMutationRule {
	return DenyIfNotRole(authn.ROLE_ADMIN)
}

func AllowIfRole(role authn.Role) entprivacy.QueryMutationRule {
	return entprivacy.ContextQueryMutationRule(func(ctx context.Context) error {
		req := request.ForContext(ctx)
		user := req.User()

		if !user.IsAuthenticated() {
			log.ForContext(ctx).Debug().
				Msg("unauthenticated user")
			return entprivacy.Skip
		}

		if !user.HasRole(role, req.TenantIDs()...) {
			log.ForContext(ctx).Debug().
				Str("role", role.String()).
				Str("user-id", user.ID.String()).
				Any("user-roles", user.Roles).
				Any("tenant-ids", req.TenantIDs()).
				Msg("user does not have required role")
			return entprivacy.Skip
		}

		return entprivacy.Allow
	})
}

func DenyIfNotRole(role authn.Role) entprivacy.QueryMutationRule {
	return entprivacy.ContextQueryMutationRule(func(ctx context.Context) error {
		req := request.ForContext(ctx)
		user := req.User()

		if !user.IsAuthenticated() {
			return entprivacy.Deny
		}

		if !user.HasRole(role, req.TenantIDs()...) {
			return entprivacy.Deny
		}

		return entprivacy.Skip
	})
}
