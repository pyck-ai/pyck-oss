package resolvers

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/feature"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	entdatatype "github.com/pyck-ai/pyck/backend/management/ent/gen/datatype"
)

// DataType request errors.
var (
	// ErrDataTypeSlugRequired rejects a create that supplies neither a slug
	// nor a name to derive one from — the (tenant, slug) family is a
	// DataType's identity, so there is nothing to file the row under.
	ErrDataTypeSlugRequired = errors.New("data type slug or name is required")
	// ErrDataTypeNotFound is returned when no live DataType with the requested
	// id exists in the caller's tenant. Soft-deleted and foreign-tenant rows
	// are reported identically, so callers cannot probe for either.
	ErrDataTypeNotFound = errors.New("data type not found")
)

// Version-policy errors. Surfaced by reconcileExplicitVersion for
// client-pinned versions (the import path).
var (
	// ErrDataTypeVersionTooLow rejects a pinned version that is neither a
	// re-import of an existing version nor a new one above the current MAX.
	// DataType is append-only: a number at or below MAX is either a backfill
	// into the middle (forbidden) or a reuse of a tombstoned number (which the
	// non-partial unique index would reject anyway).
	ErrDataTypeVersionTooLow = errors.New("data type version must be greater than the current maximum version")
	// ErrDataTypeImmutableChange rejects a re-import of an existing
	// (slug, version) whose immutable fields differ from the stored row.
	ErrDataTypeImmutableChange = errors.New("data type version already exists with different immutable fields")
)

// reconcileExplicitVersion enforces the version policy for a client-pinned
// version (the import path): re-import idempotency + append-only ordering.
//
// Returns (row, handled, err):
//   - handled == true: the create is fully resolved — `row` is the existing
//     version (re-import no-op) or the in-place name update. The caller
//     returns it without inserting.
//   - handled == false, err == nil: a brand-new version that cleared the
//     append check; the caller proceeds with the normal insert.
//
// Re-import (an existing LIVE (slug, version) row): immutable fields must
// match (else ErrDataTypeImmutableChange); only `name` may differ, in which
// case it is updated in place; otherwise it is a no-op. A genuinely new
// version must be ABOVE MAX(version) across the family (soft-deleted rows
// included, matching nextDataTypeVersion) — gaps are allowed so import can
// preserve a source's exact version numbers — or it is rejected with
// ErrDataTypeVersionTooLow.
func reconcileExplicitVersion(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, input ent.CreateDataTypeInput) (*ent.DataType, bool, error) {
	slug, version := *input.Slug, *input.Version

	existing, err := tx.DataType.Query().
		Where(
			entdatatype.TenantID(tenantID),
			entdatatype.Slug(slug),
			entdatatype.Version(version),
		).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, false, fmt.Errorf("querying existing datatype version: %w", err)
	}

	if existing != nil {
		if immutableDataTypeFieldsDiffer(existing, input) {
			return nil, false, ErrDataTypeImmutableChange
		}
		// Only name may differ across a re-import.
		if input.Name != nil && *input.Name != existing.Name {
			updated, err := tx.DataType.UpdateOneID(existing.ID).SetName(*input.Name).Save(ctx)
			if err != nil {
				return nil, false, err
			}
			return updated, true, nil
		}
		return existing, true, nil
	}

	// Brand-new version: append-only, so it must be above the current MAX.
	// Gaps are allowed on purpose — import preserves a source's exact version
	// numbers, and the live sequence can legitimately have holes (e.g. a
	// soft-deleted middle version, or delete-latest-then-replace). The version
	// is the portable identity together with the slug (entity dataTypeID FKs
	// re-resolve via (slug, version) on import), so it must survive a
	// round-trip unchanged — it cannot be renumbered to stay dense.
	//
	// TODO(jan): revisit sparse versions once import/export settles on its
	// portable key. Keeping (slug, version) forces gaps to be legal; moving
	// to client-settable UUID PKs would drop the gap requirement but trade
	// slug+version uniqueness for UUID-collision handling.
	//
	// nextDataTypeVersion returns MAX+1 across soft-deleted rows, so MAX = next-1.
	next, err := nextDataTypeVersion(ctx, tx, tenantID, slug)
	if err != nil {
		return nil, false, err
	}
	if version < next {
		return nil, false, fmt.Errorf("%w: got %d, current maximum is %d", ErrDataTypeVersionTooLow, version, next-1)
	}
	return nil, false, nil
}

// immutableDataTypeFieldsDiffer reports whether any immutable field supplied in
// input differs from the stored row. Fields omitted from input (nil optionals)
// are treated as matching — import always supplies the full set, so a nil means
// "unspecified", not "cleared". slug + version are excluded: they are the
// identity the row was looked up by.
func immutableDataTypeFieldsDiffer(existing *ent.DataType, input ent.CreateDataTypeInput) bool {
	if input.Description != nil && *input.Description != existing.Description {
		return true
	}
	if input.JSONSchema != existing.JSONSchema {
		return true
	}
	if input.FrontendSchema != nil && *input.FrontendSchema != existing.FrontendSchema {
		return true
	}
	if input.Entity != existing.Entity {
		return true
	}
	if input.Default != nil && *input.Default != existing.Default {
		return true
	}
	return false
}

// dataTypeVersionUniqueIndex is the unique index enforcing one version number
// per (tenant_id, slug) family. A concurrent createDataType for the same slug
// races on this constraint; the loser's INSERT raises 23505, which
// datatypeVersionConflict translates into the OCC sentinel. Pinned as a
// constant so the constraint-name match is exact — matching by SQLSTATE alone
// would catch unrelated unique violations.
const dataTypeVersionUniqueIndex = "datatype_tenant_id_slug_version_uniq"

// nextDataTypeVersion returns the next version number for the (tenant, slug)
// DataType family: MAX(version)+1 across ALL rows (including soft-deleted — a
// version number is never reused, matching the deliberately non-partial
// (tenant_id, slug, version) unique index), or 1 when the family is new.
//
// No lock is taken. Concurrent createDataType calls for the same slug may read
// the same MAX and collide on the unique index; the loser's INSERT raises a
// 23505 that datatypeVersionConflict translates to db.ErrOCCConflict, and the
// gqltx retry middleware re-runs the whole mutation (the inventory-stocks OCC
// pattern — backend/inventory/service/stock/errors.go). The retry re-reads the
// now-higher MAX and converges, so no advisory lock is needed.
func nextDataTypeVersion(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, slug string) (int, error) {
	// Read across soft-deleted rows: the HistoryMixin interceptor hides them
	// by default, but a deleted version still occupies its number (the unique
	// index is not partial), so the next version must clear the true MAX.
	queryCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
	latest, err := tx.DataType.Query().
		Where(
			entdatatype.TenantID(tenantID),
			entdatatype.Slug(slug),
		).
		Order(ent.Desc(entdatatype.FieldVersion)).
		First(queryCtx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("querying max datatype version: %w", err)
	}

	return latest.Version + 1, nil
}

// datatypeVersionConflict translates a Postgres unique-violation (SQLSTATE
// 23505) on the (tenant_id, slug, version) index into db.ErrOCCConflict, which
// the gqltx retry middleware recognizes and retries. Any other error (including
// 23505s on unrelated constraints) passes through unchanged.
//
// Mirrors inventory stock's wrapOCCConflict, including the dual driver-type
// check: lib/pq is the driver registered under "postgres"
// (otelsql.Register(dialect.Postgres) in common/db/postgresql.go), so in
// production the 23505 arrives as *pq.Error; tests and pgx paths surface
// *pgconn.PgError. Without both branches the OCC sentinel never fires and the
// losing transaction surfaces a raw duplicate-key error instead of retrying.
func datatypeVersionConflict(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == dataTypeVersionUniqueIndex {
		return db.ErrOCCConflict
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == pgerrcode.UniqueViolation && pqErr.Constraint == dataTypeVersionUniqueIndex {
		return db.ErrOCCConflict
	}
	return err
}
