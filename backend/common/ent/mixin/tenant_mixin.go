package mixin

import (
	"context"
	"fmt"
	"strings"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	entprivacy "entgo.io/ent/privacy"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
	"github.com/pyck-ai/pyck/backend/common/request"
)

var (
	TenantFieldTenantID = "tenant_id"
)

// TenantMixin adds tenant isolation via tenant_id field.
type TenantMixin struct {
	mixin.Schema
}

func (TenantMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID(TenantFieldTenantID, uuid.UUID{}).
			Immutable().
			Annotations(
				entgql.OrderField(strings.ToUpper(TenantFieldTenantID)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),
	}
}

func (TenantMixin) Policy() ent.Policy {
	return entprivacy.Policy{
		Query: entprivacy.QueryPolicy{
			TenantIDQueryFilter{},
		},
		Mutation: entprivacy.MutationPolicy{
			TenantIDMutationFilter{},
		},
	}
}

func (TenantMixin) Hooks() []ent.Hook {
	return []ent.Hook{
		TenantIDMutationHook(),
	}
}

func TenantIDMutationHook() ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			switch m.Op() {
			case ent.OpCreate, ent.OpUpdate, ent.OpUpdateOne:
				break
			default:
				return next.Mutate(ctx, m)

			}

			req := request.ForContext(ctx)
			user := req.User()
			tenantID := req.MutationTenantID()

			// If a tenant ID is set in the mutation, ensure it matches the one
			// in the request context
			if v, ok := m.Field(TenantFieldTenantID); ok {
				mutationTenantID, ok := v.(uuid.UUID)
				if !ok {
					return nil, fmt.Errorf("%w: unable to parse mutation tenantID", ErrInvalidTenantID)
				}

				if user.IsSystemUser() {
					// If the user is a system-user, skip the tenant ID check
					return next.Mutate(ctx, m)
				}

				if mutationTenantID != tenantID {
					return nil, fmt.Errorf("%w: mutation tenant ID %q does not match request tenant ID %q", ErrUnauthorized, mutationTenantID.String(), tenantID.String())
				}
			}

			if err := m.SetField(TenantFieldTenantID, tenantID); err != nil {
				return nil, err
			}

			return next.Mutate(ctx, m)

		})
	}
}

type TenantIDQueryFilter struct{}

func (TenantIDQueryFilter) EvalQuery(ctx context.Context, q ent.Query) error {
	req := request.ForContext(ctx)
	user := req.User()

	if !user.IsAuthenticated() {
		return entprivacy.Denyf("%w: no user", ErrUnauthorized)
	}

	if user.IsSystemUser() {
		return entprivacy.Skip
	}

	tenantIDs := req.TenantIDs()
	tenantIDsAny := make([]any, len(tenantIDs))
	for i, tenantID := range tenantIDs {
		if !user.HasRole(authn.ROLE_READER, tenantID) {
			return entprivacy.Denyf("%w: user %q does not have reader role for tenant %q", ErrUnauthorized, user.ID.String(), tenantID.String())
		}

		tenantIDsAny[i] = tenantID
	}

	if err := privacy.Where(q, sql.In(TenantFieldTenantID, tenantIDsAny...)); err != nil {
		return entprivacy.Denyf("%w: filter error: %w", ErrUnauthorized, err)
	}

	return entprivacy.Skip
}

type TenantIDMutationFilter struct{}

func (TenantIDMutationFilter) EvalMutation(ctx context.Context, m ent.Mutation) error {
	req := request.ForContext(ctx)
	user := req.User()

	if !user.IsAuthenticated() {
		return entprivacy.Denyf("%w: no user", ErrUnauthorized)
	}

	if user.IsSystemUser() {
		return entprivacy.Skip
	}

	if !user.HasRole(authn.ROLE_WRITER, req.MutationTenantID()) {
		return entprivacy.Denyf("%w: user %q does not have writer role for tenant %q", ErrUnauthorized, user.ID.String(), req.MutationTenantID().String())
	}

	if err := privacy.Where(m, sql.EQ(TenantFieldTenantID, req.MutationTenantID())); err != nil {
		return entprivacy.Denyf("%w: filter error: %w", ErrUnauthorized, err)
	}

	return entprivacy.Skip
}
