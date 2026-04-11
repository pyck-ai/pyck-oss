// TODO(mkrupp): split this into soft-delete and date mixins
package mixin

import (
	"context"
	"strings"
	"time"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/dialect/sql"
	entprivacy "entgo.io/ent/privacy"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/internal/fieldnames"
	"github.com/pyck-ai/pyck/backend/common/request"
)

// HistoryMixin adds created_at, created_by, updated_at, updated_by, deleted_at,
// and deleted_by with soft-delete capabilities. For unique constraints, apply
// HistoryMixinNotDeletedIndexAnnotation() to create partial indexes like:
//
//	index.Fields("tenant_id", "name").
//	    Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()).
//	    Unique(),
//
// This mixin provides soft-delete fields but does NOT implement soft-delete
// functionality - you must manually set deleted_at to soft-delete records.
type HistoryMixin struct {
	mixin.Schema
}

// TODO(mkrupp): intercept the delete operation so one can simply call the
// delete mehtod in the resolvers instead of having to mantually set the
// deleted_at field.

func (HistoryMixin) Fields() []ent.Field {
	return []ent.Field{
		// created
		field.Time(fieldnames.DBColumnCreatedAt).
			Immutable().
			Annotations(
				entgql.OrderField(strings.ToUpper(fieldnames.DBColumnCreatedAt)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),
		field.UUID(fieldnames.DBColumnCreatedBy, uuid.UUID{}).
			Immutable().
			Annotations(
				entgql.OrderField(strings.ToUpper(fieldnames.DBColumnCreatedBy)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),

		// updated
		field.Time(fieldnames.DBColumnUpdatedAt).
			Optional().
			Annotations(
				entgql.OrderField(strings.ToUpper(fieldnames.DBColumnUpdatedAt)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),
		field.UUID(fieldnames.DBColumnUpdatedBy, uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField(strings.ToUpper(fieldnames.DBColumnUpdatedBy)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),

		// deleted
		field.Time(fieldnames.DBColumnDeletedAt).
			Optional().
			Annotations(
				entgql.OrderField(strings.ToUpper(fieldnames.DBColumnDeletedAt)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput, entgql.SkipWhereInput),
			),
		field.UUID(fieldnames.DBColumnDeletedBy, uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField(strings.ToUpper(fieldnames.DBColumnDeletedBy)),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput, entgql.SkipWhereInput),
			),
	}
}

func (HistoryMixin) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				now := time.Now()

				req := request.ForContext(ctx)
				user := req.User()

				if !user.IsAuthenticated() {
					return nil, ErrUnauthorized
				}

				switch m.Op() {
				case ent.OpCreate:
					if err := m.SetField(fieldnames.DBColumnCreatedAt, now); err != nil {
						return nil, err
					}

					if err := m.SetField(fieldnames.DBColumnCreatedBy, user.ID); err != nil {
						return nil, err
					}
				case ent.OpUpdate, ent.OpUpdateOne:
					if err := setUpdateFields(m, now, user.ID); err != nil {
						return nil, err
					}
				}

				return next.Mutate(ctx, m)
			})
		},
	}
}

// setUpdateFields sets the appropriate timestamp and user fields for updates.
// If deleted_at is being set, this is a soft-delete; otherwise it's a regular update.
func setUpdateFields(m ent.Mutation, now time.Time, userID uuid.UUID) error {
	if _, exists := m.Field(fieldnames.DBColumnDeletedAt); exists {
		if err := m.SetField(fieldnames.DBColumnDeletedAt, now); err != nil {
			return err
		}
		return m.SetField(fieldnames.DBColumnDeletedBy, userID)
	}

	if err := m.SetField(fieldnames.DBColumnUpdatedAt, now); err != nil {
		return err
	}
	return m.SetField(fieldnames.DBColumnUpdatedBy, userID)
}

func (HistoryMixin) Policy() ent.Policy {
	return entprivacy.Policy{
		Query: entprivacy.QueryPolicy{
			HistoryMixinQueryFilter{},
		},
		Mutation: entprivacy.MutationPolicy{
			HistoryMixinMutationFilter{},
		},
	}
}

// HistoryMixinNotDeletedIndexAnnotation is an annotation that will create a
// partial index on the deleted_at field.
func HistoryMixinNotDeletedIndexAnnotation() *entsql.IndexAnnotation {
	return entsql.IndexWhere(fieldnames.DBColumnDeletedAt + " IS NULL")
}

// HistoryMixinResolveWithNewValues is a conflict option that will update the
// updated_at and updated_by fields with the current time and the user ID.
func HistoryMixinResolveWithNewValues(ctx context.Context) sql.ConflictOption {
	return sql.ResolveWith(func(u *sql.UpdateSet) {
		now := time.Now()
		req := request.ForContext(ctx)
		user := req.User()

		for _, c := range u.Columns() {
			u.SetExcluded(c)
		}

		u.SetIgnore(fieldnames.DBColumnCreatedAt)
		u.SetIgnore(fieldnames.DBColumnCreatedBy)

		u.Set(fieldnames.DBColumnUpdatedAt, now)
		u.Set(fieldnames.DBColumnUpdatedBy, user.ID)
	})
}

// HistoryMixinQueryFilter filters queries to only return non-deleted records.
type HistoryMixinQueryFilter struct{}

func (HistoryMixinQueryFilter) EvalQuery(ctx context.Context, q ent.Query) error {
	if feature.HasFeature(ctx, feature.FEATURE_SHOW_DELETED) {
		return entprivacy.Skip
	}

	if err := privacy.Where(q, sql.Or(
		sql.IsNull(fieldnames.DBColumnDeletedAt),
		sql.EQ(fieldnames.DBColumnDeletedAt, time.Time{}),
	)); err != nil {
		return entprivacy.Denyf("%s", err)
	}

	return entprivacy.Skip
}

// HistoryMixinMutationFilter filters mutations to only allow operations on
// non-deleted records.
type HistoryMixinMutationFilter struct{}

func (HistoryMixinMutationFilter) EvalMutation(ctx context.Context, m ent.Mutation) error {
	if err := privacy.Where(m, sql.Or(
		sql.IsNull(fieldnames.DBColumnDeletedAt),
		sql.EQ(fieldnames.DBColumnDeletedAt, time.Time{}),
	)); err != nil {
		return entprivacy.Denyf("%s", err)
	}

	return entprivacy.Skip
}
