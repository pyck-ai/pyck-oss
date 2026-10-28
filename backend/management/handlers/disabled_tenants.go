package handlers

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/log"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

// DisabledTenants returns the subset of tenantIDs that must not be served:
// the liveness.TenantSource backing the home-tenant check in the auth path
// and the acting-tenant check in the tenant middleware, for management
// itself in-process and, through the `disabledTenants` GraphQL query, for
// every other service.
//
// A tenant is served only while its row says so: the row exists, is not
// soft-deleted, and its registration finished. Every other case is denied.
//
//   - soft-deleted: the operator's off switch;
//   - status other than done: the tenant is half-built. A pending row is
//     still being provisioned; an error row stopped part-way with an
//     organization and an admin user but, depending on where it stopped, no
//     default datatypes, no Temporal namespace and no worker. Its admin can
//     authenticate the moment the user import lands, so admitting it would
//     hand out a tenant whose every workflow-backed operation fails. Rows
//     that predate the status column read as done (the column defaults to
//     it), so no tenant registered before this check existed is affected;
//   - no row at all: the token names a tenant this deployment does not
//     have. It is denied and logged: the two ways to reach it are a
//     registration that never produced a row and a token minted for an
//     organization that is not a tenant, and both are worth an operator's
//     attention. Callers cache the verdict, so a client retrying with the
//     same token logs once per cache TTL per replica, not once per request.
//
// System callers never reach here: they bypass the liveness axis entirely,
// which is what keeps deleteTenant and restoreTenant able to retire a row
// this query denies.
//
// The query runs as the system user with FEATURE_SHOW_DELETED, the only way
// to see soft-deleted rows past the row-level filter; without it a
// soft-deleted row would be indistinguishable from an absent one and would
// be reported under the wrong reason.
//
// The rows are read from the writer (db.WithWriterReads; plain queries go to
// the reader pool): a lifecycle event evicts the cached verdict as soon as
// its write commits, and a lookup answered by a replica that has not
// replayed that write yet would cache the stale verdict again for the whole
// TTL. Each chunk is one plain query, not a transaction: the lookup sits on
// every cache miss of the auth path, and a BEGIN/COMMIT round trip around
// it costs more than the query. On the writer pool's SERIALIZABLE default
// each query is a one-statement serializable transaction that is not READ
// ONLY, so Postgres can abort the lookup itself with a serialization
// failure while a tenant lifecycle write commits. That failure is not
// retried: the request is denied (401) and the verdict is not cached.
//
// The input is queried in chunks of at most mixin.Limit IDs: the tenant
// entity carries LimitMixin, whose interceptor rejects an explicit limit
// above the cap instead of truncating, and an acting set larger than the
// cap (an operator granted into hundreds of tenants) must still get an
// answer: the fleet denies tenants it cannot verify, so an error here
// would lock that caller out of every uncached tenant.
func DisabledTenants(ctx context.Context, client *ent.Client, tenantIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(tenantIDs) == 0 {
		return nil, nil
	}
	sysCtx := authn.Context(ctx, authn.SystemUser())
	sysCtx = feature.Context(sysCtx, feature.FEATURE_SHOW_DELETED)
	sysCtx = db.WithWriterReads(sysCtx)

	var denied, unknown []uuid.UUID
	for chunk := range slices.Chunk(tenantIDs, mixin.Limit) {
		// At most one row per requested ID can match, so the chunk length
		// is an exact bound.
		rows, err := client.Tenant.Query().
			Where(enttenant.IDIn(chunk...)).
			Select(enttenant.FieldID, enttenant.FieldDeletedAt, enttenant.FieldStatus).
			Limit(len(chunk)).
			All(sysCtx)
		if err != nil {
			return nil, fmt.Errorf("query tenant liveness: %w", err)
		}

		chunkDenied, chunkUnknown := classifyChunk(rows, chunk)
		denied = append(denied, chunkDenied...)
		unknown = append(unknown, chunkUnknown...)
	}

	if len(unknown) > 0 {
		ids := make([]string, len(unknown))
		for i, id := range unknown {
			ids[i] = id.String()
		}
		log.ForContext(ctx).Warn().
			Strs("tenant_ids", ids).
			Msg("liveness asked about tenants with no row; denying: a registration that never completed, or a token issued for an organization that is not a tenant")
	}
	return denied, nil
}

// classifyChunk sorts one queried chunk of tenant IDs into the ones to
// deny: every row that is soft-deleted or not done, and every requested ID
// with no row at all, which also comes back as unknown so the caller can
// log it.
func classifyChunk(rows []*ent.Tenant, chunk []uuid.UUID) (denied, unknown []uuid.UUID) {
	found := make(map[uuid.UUID]struct{}, len(rows))
	for _, row := range rows {
		found[row.ID] = struct{}{}
		// deleted_at is nullable and scans into a non-pointer time.Time,
		// so the zero value is "not soft-deleted".
		if row.DeletedAt.IsZero() && row.Status == enttenant.StatusDone {
			continue
		}
		denied = append(denied, row.ID)
	}
	for _, id := range chunk {
		if _, ok := found[id]; !ok {
			unknown = append(unknown, id)
			denied = append(denied, id)
		}
	}
	return denied, unknown
}
