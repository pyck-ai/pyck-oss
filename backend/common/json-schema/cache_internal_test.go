package json_schema

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/memkv"
	"github.com/pyck-ai/pyck/backend/common/request"
)

// The ent row behind a datatype event serialises deleted_at as the zero time
// for a live row (it is a plain time.Time, not a pointer), so the decoded
// DeletedAt must read as nil -- otherwise Update skips every create/update
// event as if it were a tombstone.
func TestDataTypeFromEvent_ZeroDeletedAtIsLive(t *testing.T) {
	t.Parallel()

	id, tenant := uuid.New(), uuid.New()
	dt, ok := dataTypeFromEvent(events.MutationEventMessage{
		ID:       id,
		TenantID: tenant,
		DataAfter: map[string]any{
			"id":          id.String(),
			"slug":        "item",
			"version":     2,
			"tenant_id":   tenant.String(),
			"json_schema": `{}`,
			"deleted_at":  "0001-01-01T00:00:00Z",
		},
	})
	require.True(t, ok)
	assert.Nil(t, dt.DeletedAt, "zero deleted_at is a live row")
	assert.Equal(t, 2, dt.Version)
	assert.Equal(t, "item", dt.Slug)
}

func TestDataTypeFromEvent_RealDeletedAtIsKept(t *testing.T) {
	t.Parallel()

	deletedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	dt, ok := dataTypeFromEvent(events.MutationEventMessage{
		ID: uuid.New(),
		DataAfter: map[string]any{
			"slug":        "item",
			"version":     1,
			"json_schema": `{}`,
			"deleted_at":  deletedAt.Format(time.RFC3339Nano),
		},
	})
	require.True(t, ok)
	require.NotNil(t, dt.DeletedAt)
	assert.True(t, dt.DeletedAt.Equal(deletedAt))
}

// A new version arriving as an event must move the slug slot, with no
// cache-miss fetch involved: ReadBySlug has no fallback, so this is the path a
// slug-addressed query depends on.
func TestEventCreate_PromotesSlugSlotWithoutFetch(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	dc := &DataTypesCache{memStore: memkv.NewInMemoryKVStore(0)}
	event := func(id uuid.UUID, version int, schema string) events.MutationEventMessage {
		return events.MutationEventMessage{
			ID:        id,
			TenantID:  tenant,
			Operation: "create",
			DataAfter: map[string]any{
				"id":          id.String(),
				"slug":        "item",
				"version":     version,
				"tenant_id":   tenant.String(),
				"json_schema": schema,
				"deleted_at":  "0001-01-01T00:00:00Z",
			},
		}
	}
	v1, v2 := uuid.New(), uuid.New()
	for _, ev := range []events.MutationEventMessage{event(v1, 1, `{"v":1}`), event(v2, 2, `{"v":2}`)} {
		dt, ok := dataTypeFromEvent(ev)
		require.True(t, ok)
		dc.Update(ev.ID, dt)
	}

	got, ok := dc.memStore.Get(dc.getSlugCacheKey("item", &tenant))
	require.True(t, ok, "slug slot must be populated from events alone")
	dt, ok := got.(DataType)
	require.True(t, ok)
	assert.Equal(t, v2, dt.ID)
	assert.Equal(t, 2, dt.Version)

	_, err := dc.ReadByID(context.Background(), v1)
	assert.NoError(t, err, "older version keeps its ID slot")
}

// stubFetcher lets internal tests drive Delete's successor fetch directly.
type stubFetcher struct {
	bySlugFn func(ctx context.Context, slug string, tenantID uuid.UUID) (*DataType, error)
	byIDFn   func(ctx context.Context, id uuid.UUID, tenantID uuid.UUID) (*DataType, error)
}

func (s *stubFetcher) GetDataTypes(context.Context) ([]DataType, error) { return nil, nil }

func (s *stubFetcher) GetDataTypeByID(ctx context.Context, id uuid.UUID, tenantID uuid.UUID) (*DataType, error) {
	if s.byIDFn == nil {
		return nil, ErrDataTypeNotFound
	}
	return s.byIDFn(ctx, id, tenantID)
}

func (s *stubFetcher) GetDataTypeBySlug(ctx context.Context, slug string, tenantID uuid.UUID) (*DataType, error) {
	return s.bySlugFn(ctx, slug, tenantID)
}

// A transient fetch failure during delete-promotion must NOT leave the slug
// slot empty: DataType is append-only, so for a stable slug no later create
// event will ever repopulate the slot — a delete-then-fetch order turns one
// failed round-trip into a permanent per-process cache hole. The slot keeps
// its entry, stamped with the tombstone so slug-addressed writes fail with
// ErrDataTypeDeleted rather than validating against a live-looking copy of
// the deleted row.
func TestDelete_TransportErrorKeepsTombstonedSlugSlot(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	deletedAt := time.Now().UTC().Add(-time.Minute)
	deleted := DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 2,
		JsonSchema: `{}`, DeletedAt: &deletedAt,
	}
	live := deleted
	live.DeletedAt = nil

	dc := &DataTypesCache{
		memStore: memkv.NewInMemoryKVStore(0),
		fetcher: &stubFetcher{
			bySlugFn: func(context.Context, string, uuid.UUID) (*DataType, error) {
				return nil, context.DeadlineExceeded // transient transport failure
			},
		},
	}
	dc.Update(live.ID, live)

	dc.Delete(context.Background(), deleted)

	got, ok := dc.memStore.Get(dc.getSlugCacheKey("customer", &tenant))
	require.True(t, ok, "slug slot must survive a transient successor-fetch failure")
	cur, ok := got.(DataType)
	require.True(t, ok)
	assert.Equal(t, deleted.ID, cur.ID)
	require.NotNil(t, cur.DeletedAt, "kept entry must carry the tombstone")
	assert.Equal(t, deletedAt, *cur.DeletedAt)
}

// A leftover occupant from a LOST earlier delete event (lower version,
// different row) must not survive a later delete that finds no live
// successor — only a higher-versioned occupant (concurrent create) may stay.
func TestDelete_NoSuccessorDropsStaleLowerVersionOccupant(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	stale := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 1, JsonSchema: `{}`}
	deleted := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 2, JsonSchema: `{}`}

	dc := &DataTypesCache{
		memStore: memkv.NewInMemoryKVStore(0),
		fetcher: &stubFetcher{
			bySlugFn: func(context.Context, string, uuid.UUID) (*DataType, error) {
				return nil, ErrDataTypeNotFound // no live successor
			},
		},
	}
	// Slot still points at v1 because v1's delete event was lost.
	dc.Update(stale.ID, stale)

	dc.Delete(context.Background(), deleted)

	_, ok := dc.memStore.Get(dc.getSlugCacheKey("customer", &tenant))
	assert.False(t, ok, "stale lower-version occupant must be dropped with the deleted row")
}

// A create event that lands while the successor fetch is in flight must win
// over the (older) fetched successor: promotion has to go through Update's
// version tiebreak, not write the slug slot directly.
func TestDelete_PromotionRespectsConcurrentNewerVersion(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	survivor := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 1, JsonSchema: `{"v":1}`}
	deleted := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 2, JsonSchema: `{"v":2}`}
	concurrent := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 3, JsonSchema: `{"v":3}`}

	dc := &DataTypesCache{memStore: memkv.NewInMemoryKVStore(0)}
	dc.fetcher = &stubFetcher{
		bySlugFn: func(context.Context, string, uuid.UUID) (*DataType, error) {
			// A concurrent create event for v3 arrives mid-fetch; management
			// answered before v3 committed, so it reports v1 as successor.
			dc.Update(concurrent.ID, concurrent)
			return &survivor, nil
		},
	}
	dc.Update(deleted.ID, deleted)

	dc.Delete(context.Background(), deleted)

	got, ok := dc.memStore.Get(dc.getSlugCacheKey("customer", &tenant))
	require.True(t, ok, "slug slot must not end up empty")
	cur, ok := got.(DataType)
	require.True(t, ok)
	assert.Equal(t, concurrent.ID, cur.ID,
		"the concurrently created v3 must win over the fetched v1 successor")
	// The fetched survivor must still be resolvable by ID.
	_, ok = dc.memStore.Get(survivor.ID.String())
	assert.True(t, ok, "promoted successor must land in the ID slot")
}

// A version is deleted exactly once and never comes back: DataType rows are
// immutable, so any later event carrying that ID describes the pre-delete
// state. JetStream redelivers a message whose Ack failed, so a create event
// can legitimately arrive AFTER the delete event for the same row was
// processed — the cache must refuse to reinstall it as live, otherwise
// writes validate against a deleted version for the process lifetime (no
// later event corrects it in an append-only domain).
func TestUpdate_RefusesToResurrectDeletedVersion(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	live := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 2, JsonSchema: `{"v":2}`}
	deletedAt := time.Now().UTC()
	tombstone := live
	tombstone.DeletedAt = &deletedAt

	dc := &DataTypesCache{
		memStore: memkv.NewInMemoryKVStore(0),
		fetcher: &stubFetcher{
			bySlugFn: func(context.Context, string, uuid.UUID) (*DataType, error) {
				return nil, ErrDataTypeNotFound // no successor
			},
		},
	}
	dc.Update(live.ID, live)
	dc.Delete(context.Background(), tombstone)

	// Redelivered create event for the same (now deleted) row.
	dc.Update(live.ID, live)

	got, err := dc.ReadByID(mutationCtxInternal(tenant), live.ID)
	require.ErrorIs(t, err, ErrDataTypeNotFound, "deleted version must not be resolvable, got %+v", got)

	slug, ok := dc.memStore.Get(dc.getSlugCacheKey("customer", &tenant))
	if ok {
		cur, isDT := slug.(DataType)
		require.True(t, isDT)
		assert.NotEqual(t, live.ID, cur.ID, "deleted version must not reoccupy the slug slot")
	}
}

// A tombstoned ID slot answers from cache: the row is known deleted, so the
// management round-trip would only confirm it (and returns NotFound anyway,
// since soft-deleted rows are filtered server-side).
func TestReadByID_TombstonedEntrySkipsFetcher(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	live := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenant, Version: 1, JsonSchema: `{}`}
	deletedAt := time.Now().UTC()
	tombstone := live
	tombstone.DeletedAt = &deletedAt

	var byIDCalls int
	dc := &DataTypesCache{
		memStore: memkv.NewInMemoryKVStore(0),
		fetcher: &stubFetcher{
			bySlugFn: func(context.Context, string, uuid.UUID) (*DataType, error) {
				return nil, ErrDataTypeNotFound
			},
			byIDFn: func(context.Context, uuid.UUID, uuid.UUID) (*DataType, error) {
				byIDCalls++
				return nil, ErrDataTypeNotFound
			},
		},
	}
	dc.Update(live.ID, live)
	dc.Delete(context.Background(), tombstone)

	_, err := dc.ReadByID(mutationCtxInternal(tenant), live.ID)
	require.ErrorIs(t, err, ErrDataTypeNotFound)
	assert.Zero(t, byIDCalls, "a known-deleted id must not be re-fetched from management")
}

// The slug slot's version tiebreak must be atomic: concurrent Update calls
// (the NATS consumer and a ReadByID cache-miss fallback are separate
// goroutines) must not let a lower version overwrite a higher one that
// landed between a get and a set.
func TestUpdate_SlugTiebreakIsAtomicUnderContention(t *testing.T) {
	t.Parallel()

	tenant := uuid.New()
	dc := &DataTypesCache{memStore: memkv.NewInMemoryKVStore(0)}

	const versions = 32
	var wg sync.WaitGroup
	wg.Add(versions)
	for v := 1; v <= versions; v++ {
		go func(version int) {
			defer wg.Done()
			dc.Update(uuid.New(), DataType{
				ID: uuid.New(), Slug: "customer", TenantID: tenant,
				Version: version, JsonSchema: `{}`,
			})
		}(v)
	}
	wg.Wait()

	got, ok := dc.memStore.Get(dc.getSlugCacheKey("customer", &tenant))
	require.True(t, ok)
	cur, isDT := got.(DataType)
	require.True(t, isDT)
	assert.Equal(t, versions, cur.Version, "slug slot regressed below the highest version written")
}

// mutationCtxInternal builds a single-tenant request context, the shape
// ReadByID's fetcher fallback requires.
func mutationCtxInternal(tenantID uuid.UUID) context.Context {
	return request.Context(context.Background(), &authn.User{ID: uuid.New(), TenantID: tenantID}, tenantID)
}

// The cache holds every tenant's DataTypes, so an ID hit must be
// tenant-checked: a caller that reads by id without going through the
// validator (a read-path resolver) would otherwise resolve a foreign row.
func TestReadByID_CacheHitIsTenantChecked(t *testing.T) {
	t.Parallel()

	tenantA, tenantB := uuid.New(), uuid.New()
	foreign := DataType{ID: uuid.New(), Slug: "customer", TenantID: tenantB, Version: 1, JsonSchema: `{}`}

	dc := &DataTypesCache{memStore: memkv.NewInMemoryKVStore(0)}
	dc.Update(foreign.ID, foreign)

	_, err := dc.ReadByID(mutationCtxInternal(tenantA), foreign.ID)
	require.ErrorIs(t, err, ErrDataTypeNotFound, "tenant A must not resolve tenant B's row by id")

	got, err := dc.ReadByID(mutationCtxInternal(tenantB), foreign.ID)
	require.NoError(t, err, "the owning tenant still resolves it")
	assert.Equal(t, foreign.ID, got.ID)
}

// A publisher bug can put something other than the row into data_after (a
// bulk mutation's affected-row count arrives as a JSON number). The decoder
// must fall back to data_before for ANY non-map data_after, not only null —
// for a delete event, data_before still carries the slug the promotion
// logic needs, so one bad publisher shape must not kill delete-promotion.
func TestDataTypeFromEvent_NonMapDataAfterFallsBackToDataBefore(t *testing.T) {
	t.Parallel()

	id, tenant := uuid.New(), uuid.New()
	dt, ok := dataTypeFromEvent(events.MutationEventMessage{
		ID:        id,
		TenantID:  tenant,
		DataAfter: float64(1), // affected-row count, not the row
		DataBefore: map[string]any{
			"id":          id.String(),
			"slug":        "customer",
			"version":     2,
			"tenant_id":   tenant.String(),
			"json_schema": `{}`,
			"deleted_at":  "0001-01-01T00:00:00Z",
		},
	})
	require.True(t, ok, "data_before carries a decodable row")
	assert.Equal(t, "customer", dt.Slug)
	assert.Equal(t, 2, dt.Version)
}
