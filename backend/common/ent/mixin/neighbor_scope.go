package mixin

import (
	"context"
	"time"

	"entgo.io/ent/dialect/sql"
	entprivacy "entgo.io/ent/privacy"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/internal/fieldnames"
	"github.com/pyck-ai/pyck/backend/common/request"
)

// ScopeNeighborToTenants narrows the sub-select that a generated relation
// predicate (Has<Edge>With, and the plain Has<Edge>()) builds for a
// tenant-scoped neighbour to the tenants the caller may read.
//
// TenantIDQueryFilter only reaches the root table of a query. The EXISTS/IN
// sub-select that a relation filter compiles to is assembled by the generated
// predicate and never passes through the privacy policy, so without this a
// where-input could evaluate its predicates against rows of another tenant
// whenever a row of the acting tenant references them. The generated where.go
// applies this predicate in both relation predicates whenever their target
// carries tenant_id (see the haswith and has templates under
// common/cmd/entc/templates).
//
// It mirrors the root filter except that an Allow privacy decision does NOT
// lift it. Only the system user is not narrowed; a context without an
// authenticated user, or without a readable tenant, matches nothing rather
// than everything. Code under Allow may scope the root by hand (TenantIDEQ),
// and an unscoped sub-select would then let a client relation filter test the
// rows of another tenant (a cross-tenant oracle), so cross-tenant code must
// run as the system user.
func ScopeNeighborToTenants(s *sql.Selector) {
	req := request.ForContext(s.Context())
	user := req.User()
	if user.IsSystemUser() {
		return
	}
	if !user.IsAuthenticated() {
		s.Where(sql.False())
		return
	}

	tenantIDs := req.TenantIDs()
	readable := make([]any, 0, len(tenantIDs))
	for _, tenantID := range tenantIDs {
		if user.HasRole(authn.ROLE_READER, tenantID) {
			readable = append(readable, tenantID)
		}
	}
	if len(readable) == 0 {
		s.Where(sql.False())
		return
	}

	s.Where(sql.In(s.C(TenantFieldTenantID), readable...))
}

// ScopeNeighborToLive drops soft-deleted rows from the sub-select that a
// generated relation predicate (Has<Edge>With, and the plain Has<Edge>())
// builds for a neighbour carrying deleted_at. It mirrors
// HistoryMixinQueryFilter, which, like the tenant filter, only reaches the
// root table: FEATURE_SHOW_DELETED and an Allow privacy decision lift it, and
// it applies to the system user too.
func ScopeNeighborToLive(s *sql.Selector) {
	ctx := s.Context()
	if decidedAllow(ctx) || feature.HasFeature(ctx, feature.FEATURE_SHOW_DELETED) {
		return
	}

	s.Where(sql.Or(
		sql.IsNull(s.C(fieldnames.DBColumnDeletedAt)),
		sql.EQ(s.C(fieldnames.DBColumnDeletedAt), time.Time{}),
	))
}

// decidedAllow reports whether ctx carries an entprivacy Allow decision. ent
// then skips every privacy policy, the root soft-delete filter included, so
// the soft-delete neighbour scope steps aside too; otherwise a relation filter
// would match nothing on a query whose root rows it returns.
func decidedAllow(ctx context.Context) bool {
	decision, ok := entprivacy.DecisionFromContext(ctx)
	return ok && decision == nil
}
