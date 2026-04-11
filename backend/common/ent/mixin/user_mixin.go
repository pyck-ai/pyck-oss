package mixin

import (
	"context"
	"strings"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	entprivacy "entgo.io/ent/privacy"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
	"github.com/pyck-ai/pyck/backend/common/request"
)

var (
	UserFieldUserID = "user_id"
)

// UserMixin adds user isolation via user_id field.
type UserMixin struct {
	mixin.Schema
}

func (UserMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID(UserFieldUserID, uuid.UUID{}).
			Optional().
			Immutable().
			Annotations(
				entgql.OrderField(strings.ToUpper(UserFieldUserID)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),
	}
}

func (UserMixin) Policy() ent.Policy {
	return entprivacy.Policy{
		Query: entprivacy.QueryPolicy{
			UserIDQueryFilter{},
		},
		Mutation: entprivacy.MutationPolicy{
			UserIDMutationFilter{},
		},
	}
}

func (UserMixin) Hooks() []ent.Hook {
	return []ent.Hook{
		UserIDMutationHook,
	}
}

func UserIDMutationHook(next ent.Mutator) ent.Mutator {
	return UserIDMutationMutator{next: next}
}

type UserIDMutationMutator struct {
	next ent.Mutator
}

func (h UserIDMutationMutator) Mutate(ctx context.Context, m ent.Mutation) (ent.Value, error) {
	req := request.ForContext(ctx)
	user := req.User()

	if !user.IsAuthenticated() {
		return nil, ErrUnauthorized
	}

	if user.IsSystemUser() {
		return h.next.Mutate(ctx, m)
	}

	switch m.Op() {
	case ent.OpCreate:
		if err := m.SetField(UserFieldUserID, user.ID); err != nil {
			return nil, err
		}
	}

	return h.next.Mutate(ctx, m)
}

type UserIDQueryFilter struct{}

func (UserIDQueryFilter) EvalQuery(ctx context.Context, q ent.Query) error {
	req := request.ForContext(ctx)
	user := req.User()

	if !user.IsAuthenticated() {
		return entprivacy.Denyf("%w: no user", ErrUnauthorized)
	}

	// If the user is a system user, skip the tenant filter.
	if user.IsSystemUser() {
		return entprivacy.Skip
	}

	// Apply the user filter to the query.
	err := privacy.Where(q, sql.Or(sql.IsNull(UserFieldUserID), sql.EQ(UserFieldUserID, user.ID.String())))
	if err != nil {
		return entprivacy.Denyf("%w: filter error: %w", ErrUnauthorized, err)
	}

	return entprivacy.Skip
}

type UserIDMutationFilter struct{}

func (UserIDMutationFilter) EvalMutation(ctx context.Context, m ent.Mutation) error {
	req := request.ForContext(ctx)
	user := req.User()

	if !user.IsAuthenticated() {
		return entprivacy.Denyf("%w: no user", ErrUnauthorized)
	}

	// If the user is a system user, skip the tenant filter.
	if user.IsSystemUser() {
		return entprivacy.Skip
	}

	// Apply the user filter to the query.
	err := privacy.Where(m, sql.EQ(UserFieldUserID, user.ID.String()))
	if err != nil {
		return entprivacy.Denyf("%w: filter error: %v", ErrUnauthorized, err)
	}

	return entprivacy.Skip
}
