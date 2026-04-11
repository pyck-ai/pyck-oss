//go:build !skippolicy

package schema

import (
	"context"

	entprivacy "entgo.io/ent/privacy"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

type allowIfReaderQueryPolicy struct{}

func (allowIfReaderQueryPolicy) EvalQuery(ctx context.Context, q ent.Query) error {
	tenantQuery, ok := q.(*ent.TenantQuery)
	if !ok {
		log.ForContext(ctx).Error().
			Msg("unexpected query type")
		return entprivacy.Deny // this should never happen
	}

	req := request.ForContext(ctx)
	user := req.User()
	tenantIDs := req.TenantIDs()

	if !user.HasRole(authn.ROLE_READER, tenantIDs...) {
		return entprivacy.Deny
	}

	if !user.IsSystemUser() {
		// system user can see all tenants, everyone else only the ones they
		// have read access to
		tenantQuery.Where(tenant.IDIn(tenantIDs...))
	}

	return entprivacy.Allow
}

type allowTenantCreationMutationPolicy struct{}

func (allowTenantCreationMutationPolicy) EvalMutation(ctx context.Context, m ent.Mutation) error {
	// Allow anonymous creation of tenants, so new customers can register
	if m.Op() == ent.OpCreate {
		return entprivacy.Allow
	}

	return entprivacy.Skip
}

type allowTenantMutationIfAdminPolicy struct{}

func (allowTenantMutationIfAdminPolicy) EvalMutation(ctx context.Context, m ent.Mutation) error {
	tenantMutation, ok := m.(*ent.TenantMutation)
	if !ok {
		log.ForContext(ctx).Error().
			Msg("unexpected mutation type")
		return entprivacy.Deny // this should never happen
	}

	req := request.ForContext(ctx)
	user := req.User()
	tenantIDs := req.TenantIDs()

	if !user.HasRole(authn.ROLE_ADMIN, tenantIDs...) {
		return entprivacy.Deny
	}

	if !user.IsSystemUser() {
		// system user can edit all tenants, everyone else only the ones they
		// have admin access to
		tenantMutation.Where(tenant.IDIn(tenantIDs...))
	}

	return entprivacy.Allow
}

type denyDataUpdateForFlavourTenantsPolicy struct{}

func (denyDataUpdateForFlavourTenantsPolicy) EvalMutation(ctx context.Context, m ent.Mutation) error {
	tenantMutation, ok := m.(*ent.TenantMutation)
	if !ok {
		return entprivacy.Skip
	}

	if m.Op() != ent.OpUpdate && m.Op() != ent.OpUpdateOne {
		return entprivacy.Skip
	}

	req := request.ForContext(ctx)
	user := req.User()

	if user.IsSystemUser() {
		return entprivacy.Skip
	}

	if _, exists := tenantMutation.Data(); !exists {
		return entprivacy.Skip
	}

	tenantID, exists := tenantMutation.ID()
	if !exists {
		ids, err := tenantMutation.IDs(ctx)
		if err != nil || len(ids) == 0 {
			return entprivacy.Skip
		}
		tenantID = ids[0]
	}

	existingTenant, err := tenantMutation.Client().Tenant.Get(ctx, tenantID)
	if err != nil {
		return entprivacy.Skip
	}

	if core.DetectFlavour(existingTenant.Data) != "" {
		return entprivacy.Denyf("data field is read-only for flavour tenants")
	}

	return entprivacy.Skip
}

func (Tenant) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			allowIfReaderQueryPolicy{},
			privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			allowTenantCreationMutationPolicy{},
			denyDataUpdateForFlavourTenantsPolicy{},
			allowTenantMutationIfAdminPolicy{},
			privacy.AlwaysDenyRule(),
		},
	}
}
