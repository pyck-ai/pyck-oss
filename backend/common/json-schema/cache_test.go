package json_schema_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
)

// mutationCtx returns a request context carrying exactly one tenant, the
// shape ReadByID's fetcher fallback requires (it runs inside entity
// mutations, which always have an unambiguous mutation tenant).
func mutationCtx(tenantID uuid.UUID) context.Context {
	return request.Context(context.Background(), &authn.User{ID: uuid.New(), TenantID: tenantID}, tenantID)
}

type mockFetcher struct {
	fn       func(ctx context.Context) ([]json_schema.DataType, error)
	bySlugFn func(ctx context.Context, slug string, tenantID uuid.UUID) (*json_schema.DataType, error)
	byIDFn   func(ctx context.Context, id uuid.UUID, tenantID uuid.UUID) (*json_schema.DataType, error)
}

func (m *mockFetcher) GetDataTypes(ctx context.Context) ([]json_schema.DataType, error) {
	return m.fn(ctx)
}

func (m *mockFetcher) GetDataTypeBySlug(ctx context.Context, slug string, tenantID uuid.UUID) (*json_schema.DataType, error) {
	if m.bySlugFn == nil {
		return nil, json_schema.ErrDataTypeNotFound
	}
	return m.bySlugFn(ctx, slug, tenantID)
}

func (m *mockFetcher) GetDataTypeByID(ctx context.Context, id uuid.UUID, tenantID uuid.UUID) (*json_schema.DataType, error) {
	if m.byIDFn == nil {
		return nil, json_schema.ErrDataTypeNotFound
	}
	return m.byIDFn(ctx, id, tenantID)
}

func newTestCache(t *testing.T, fetcher json_schema.DataTypesClient) *json_schema.DataTypesCache {
	t.Helper()

	cache, err := json_schema.NewDataTypesCache(context.Background(), nil, json_schema.DataTypesCacheOptions{
		Fetcher:     fetcher,
		Consumer:    &nats.ConsumerInfo{},
		ServiceName: "test",
	})
	require.NoError(t, err)

	return cache
}

func TestRetrieveJsonSchemasToCache_SinglePage(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()

	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) {
			return []json_schema.DataType{
				{ID: id1, JsonSchema: `{"type":"object"}`, Slug: "test-slug", TenantID: tenantID},
				{ID: id2, JsonSchema: `{"type":"array"}`, Slug: "other-slug", TenantID: tenantID},
			}, nil
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.NoError(t, err)

	dt1, err := cache.ReadByID(context.Background(), id1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, dt1.JsonSchema)
	assert.Equal(t, "test-slug", dt1.Slug)
	assert.Equal(t, tenantID, dt1.TenantID)

	dt2, err := cache.ReadByID(context.Background(), id2)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"array"}`, dt2.JsonSchema)
	assert.Equal(t, "other-slug", dt2.Slug)
}

func TestRetrieveJsonSchemasToCache_Error(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) {
			return nil, errors.New("connection refused")
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestRetrieveJsonSchemasToCache_EmptyResult(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) {
			return nil, nil
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.NoError(t, err)

	_, err = cache.ReadByID(context.Background(), uuid.New())
	assert.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}

// Update must reject soft-deleted rows even when management emits them by
// accident. Slug stays unset, ID stays absent.
func TestUpdate_SkipsSoftDeletedRows(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t, &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
	})

	deletedAt := time.Now().UTC()
	dt := json_schema.DataType{
		ID:         uuid.New(),
		Slug:       "ghost",
		TenantID:   uuid.New(),
		JsonSchema: `{}`,
		CreatedAt:  time.Now().UTC(),
		DeletedAt:  &deletedAt,
	}
	cache.Update(dt.ID, dt)

	_, err := cache.ReadByID(context.Background(), dt.ID)
	assert.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}

// Two versions of the same slug, inserted out of order (newer first). The
// cache tiebreaks on version (not CreatedAt), so each version stays in its own
// ID slot and a later-arriving lower version doesn't clobber the higher one.
// (The slug-slot side of this tiebreak is exercised by
// TestUpdate_RenameOlderVersionKeepsNewerIntact.)
func TestUpdate_OutOfOrderVersionsKeepOwnSlots(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t, &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
	})

	tenantID := uuid.New()
	older := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantID, Version: 1,
		JsonSchema: `{"v":1}`,
		CreatedAt:  time.Now().UTC().Add(-time.Hour),
	}
	newer := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantID, Version: 2,
		JsonSchema: `{"v":2}`,
		CreatedAt:  time.Now().UTC(),
	}

	// Out-of-order insertion: newer (v2) first, then older (v1).
	cache.Update(newer.ID, newer)
	cache.Update(older.ID, older)

	// Both ID slots still resolve to their own version.
	gotNew, err := cache.ReadByID(context.Background(), newer.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":2}`, gotNew.JsonSchema)
	gotOld, err := cache.ReadByID(context.Background(), older.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1}`, gotOld.JsonSchema)
}

// A rename (same version, new name) must be reflected on the cached copy —
// this is the path the "update" event handler drives (name is the one mutable
// DataType field under #990). The ID slot picks up the new name.
func TestUpdate_ReflectsRename(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t, &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
	})

	dt := json_schema.DataType{
		ID: uuid.New(), Name: "Original", Slug: "customer", TenantID: uuid.New(),
		Version: 1, JsonSchema: `{}`, CreatedAt: time.Now().UTC(),
	}
	cache.Update(dt.ID, dt)

	// Rename: same ID, same version, new name (what updateDataType emits).
	renamed := dt
	renamed.Name = "Renamed"
	cache.Update(dt.ID, renamed)

	got, err := cache.ReadByID(context.Background(), dt.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Name, "rename must be reflected on the cached row")
	assert.Equal(t, 1, got.Version, "rename leaves the version unchanged")
}

// Renaming an OLDER version must not move the slug slot off the newer version:
// the version tiebreak keeps a newer slug slot intact while the older ID slot
// still picks up its new name.
func TestUpdate_RenameOlderVersionKeepsNewerIntact(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t, &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
	})

	tenantID := uuid.New()
	older := json_schema.DataType{
		ID: uuid.New(), Name: "Old v1", Slug: "customer", TenantID: tenantID,
		Version: 1, JsonSchema: `{"v":1}`, CreatedAt: time.Now().UTC().Add(-time.Hour),
	}
	newer := json_schema.DataType{
		ID: uuid.New(), Name: "New v2", Slug: "customer", TenantID: tenantID,
		Version: 2, JsonSchema: `{"v":2}`, CreatedAt: time.Now().UTC(),
	}
	cache.Update(older.ID, older)
	cache.Update(newer.ID, newer)

	// Rename the older version.
	renamedOlder := older
	renamedOlder.Name = "Renamed v1"
	cache.Update(older.ID, renamedOlder)

	gotOld, err := cache.ReadByID(context.Background(), older.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed v1", gotOld.Name, "older ID slot picks up the new name")

	gotNew, err := cache.ReadByID(context.Background(), newer.ID)
	require.NoError(t, err)
	assert.Equal(t, "New v2", gotNew.Name, "newer version is untouched by the older rename")
	assert.JSONEq(t, `{"v":2}`, gotNew.JsonSchema)
}

// Delete must promote the next-newest non-deleted version into the slug
// slot if the fetcher knows about one. ID slot is dropped either way.
func TestDelete_PromotesNextNewestVersionForSlug(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	older := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantID,
		JsonSchema: `{"v":1}`,
		CreatedAt:  time.Now().UTC().Add(-time.Hour),
	}
	newer := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantID,
		JsonSchema: `{"v":2}`,
		CreatedAt:  time.Now().UTC(),
	}

	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) {
			// Initial load returns both, ASC by CreatedAt.
			return []json_schema.DataType{older, newer}, nil
		},
		bySlugFn: func(_ context.Context, slug string, _ uuid.UUID) (*json_schema.DataType, error) {
			if slug == "customer" {
				// After deleting newer, the surviving latest is older.
				return &older, nil
			}
			return nil, json_schema.ErrDataTypeNotFound
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.NoError(t, err)

	// Delete the latest version.
	cache.Delete(context.Background(), newer)

	// newer ID is gone.
	_, err = cache.ReadByID(context.Background(), newer.ID)
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
	// older ID stays resolvable.
	got, err := cache.ReadByID(context.Background(), older.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1}`, got.JsonSchema)
}

// Delete must scope the successor lookup to the DELETING tenant. Slugs are not
// unique across tenants, and the promotion fetch runs under a system token (no
// request-scoped tenant), so an unscoped query would resolve another tenant's
// higher-version row and leave the deleting tenant's own successor unpromoted.
func TestDelete_SuccessorLookupIsTenantScoped(t *testing.T) {
	t.Parallel()

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Tenant A: "customer" v1 (survivor) + v2 (latest, being deleted).
	aSurvivor := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantA, Version: 1,
		JsonSchema: `{"t":"A","v":1}`, CreatedAt: time.Now().UTC().Add(-time.Hour),
	}
	aLatest := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantA, Version: 2,
		JsonSchema: `{"t":"A","v":2}`, CreatedAt: time.Now().UTC(),
	}
	// Tenant B: same slug, a HIGHER version number than anything A has — this is
	// what an unscoped (global MAX) query would wrongly return.
	bHigher := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantB, Version: 9,
		JsonSchema: `{"t":"B","v":9}`, CreatedAt: time.Now().UTC(),
	}

	var gotTenant uuid.UUID
	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) {
			return []json_schema.DataType{aSurvivor, aLatest, bHigher}, nil
		},
		// Mimic the management query's tenant-scoped WhereInput: highest-version
		// row FOR THE GIVEN TENANT only.
		bySlugFn: func(_ context.Context, slug string, tenantID uuid.UUID) (*json_schema.DataType, error) {
			gotTenant = tenantID
			if slug != "customer" {
				return nil, json_schema.ErrDataTypeNotFound
			}
			switch tenantID {
			case tenantA:
				return &aSurvivor, nil
			case tenantB:
				return &bHigher, nil
			}
			return nil, json_schema.ErrDataTypeNotFound
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.NoError(t, err)

	// Tenant A deletes its latest "customer".
	cache.Delete(context.Background(), aLatest)

	// The successor lookup must have been scoped to tenant A, not the global MAX
	// (tenant B's v9).
	assert.Equal(t, tenantA, gotTenant, "successor lookup must be scoped to the deleting tenant")

	// Tenant A's own prior version is the promoted survivor.
	got, err := cache.ReadByID(context.Background(), aSurvivor.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"t":"A","v":1}`, got.JsonSchema)
}

// Delete when no successor exists must drop the slug entry entirely so
// ReadBySlug eventually returns ErrDataTypeNotFound (slug lookup tested
// indirectly via the ID lookup below).
func TestDelete_DropsSlugWhenNoSuccessor(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	only := json_schema.DataType{
		ID: uuid.New(), Slug: "lonely", TenantID: tenantID,
		JsonSchema: `{}`,
		CreatedAt:  time.Now().UTC(),
	}

	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) {
			return []json_schema.DataType{only}, nil
		},
		bySlugFn: func(_ context.Context, _ string, _ uuid.UUID) (*json_schema.DataType, error) {
			return nil, json_schema.ErrDataTypeNotFound
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.NoError(t, err)

	cache.Delete(context.Background(), only)

	_, err = cache.ReadByID(context.Background(), only.ID)
	assert.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}

// On a cold cache the deleted row is already soft-deleted
// in management, so GetDataTypeByID returns NotFound. Delete must still
// correct the slug index using the slug carried in the event payload — never
// re-read the dead row by ID. Here the cache is empty, byIDFn always fails,
// yet a successor exists, and Delete must promote it.
func TestDelete_ColdCachePromotesSuccessorFromEventSlug(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	older := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantID,
		JsonSchema: `{"v":1}`,
		CreatedAt:  time.Now().UTC().Add(-time.Hour),
	}
	deleted := json_schema.DataType{
		ID: uuid.New(), Slug: "customer", TenantID: tenantID,
		JsonSchema: `{"v":2}`,
		CreatedAt:  time.Now().UTC(),
	}

	var byIDCalls int
	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
		byIDFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*json_schema.DataType, error) {
			// Soft-deleted rows are invisible by ID — Delete must not rely on this.
			byIDCalls++
			return nil, json_schema.ErrDataTypeNotFound
		},
		bySlugFn: func(_ context.Context, slug string, _ uuid.UUID) (*json_schema.DataType, error) {
			if slug == "customer" {
				return &older, nil
			}
			return nil, json_schema.ErrDataTypeNotFound
		},
	}

	cache := newTestCache(t, fetcher)

	// Cache is cold: deleted.ID was never loaded. Delete carries the slug.
	cache.Delete(context.Background(), deleted)

	assert.Zero(t, byIDCalls, "Delete must not re-read the soft-deleted row by ID")

	// The surviving older version was promoted into the cache.
	got, err := cache.ReadByID(context.Background(), older.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1}`, got.JsonSchema)
}

// On cache miss, ReadByID must fall back to the management fetcher and
// populate the cache.
func TestReadByID_CacheMissFallsBackToFetcher(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	want := json_schema.DataType{
		ID:         uuid.New(),
		Slug:       "fresh",
		TenantID:   tenantID,
		JsonSchema: `{"type":"object"}`,
		CreatedAt:  time.Now().UTC(),
	}

	var fetchCalls int
	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
		byIDFn: func(_ context.Context, id uuid.UUID, fetchTenant uuid.UUID) (*json_schema.DataType, error) {
			fetchCalls++
			// The fallback must scope the fetch to the caller's mutation
			// tenant — management skips its tenant filter for the system
			// token this client runs under.
			assert.Equal(t, tenantID, fetchTenant, "fallback must pass the mutation tenant")
			if id == want.ID {
				return &want, nil
			}
			return nil, json_schema.ErrDataTypeNotFound
		},
	}

	cache := newTestCache(t, fetcher)
	ctx := mutationCtx(tenantID)
	// Cache is cold (no RetrieveJsonSchemasToCache call); first lookup
	// triggers a fetch.
	got, err := cache.ReadByID(ctx, want.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, 1, fetchCalls, "first lookup must hit the fetcher")

	// Second lookup hits the cache — fetcher not called again.
	got2, err := cache.ReadByID(ctx, want.ID)
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, 1, fetchCalls, "second lookup must be served from cache")

	// A cache hit needs no tenant in the context (read paths).
	got3, err := cache.ReadByID(context.Background(), want.ID)
	require.NoError(t, err)
	require.NotNil(t, got3)

	// Without an unambiguous tenant the fallback is skipped entirely
	// rather than issuing an unscoped fetch.
	_, err = cache.ReadByID(context.Background(), uuid.New())
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
	assert.Equal(t, 1, fetchCalls, "no-tenant context must not trigger a fetch")
}

// A cache-miss fetch that returns a soft-deleted row must still surface as
// ErrDataTypeNotFound — never populate the cache with a deleted row.
func TestReadByID_FetcherReturnsDeleted_TreatedAsNotFound(t *testing.T) {
	t.Parallel()

	deletedAt := time.Now().UTC()
	zombie := json_schema.DataType{
		ID:        uuid.New(),
		Slug:      "zombie",
		TenantID:  uuid.New(),
		CreatedAt: time.Now().UTC(),
		DeletedAt: &deletedAt,
	}

	fetcher := &mockFetcher{
		fn: func(_ context.Context) ([]json_schema.DataType, error) { return nil, nil },
		byIDFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*json_schema.DataType, error) {
			return &zombie, nil
		},
	}

	cache := newTestCache(t, fetcher)
	_, err := cache.ReadByID(mutationCtx(zombie.TenantID), zombie.ID)
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}
