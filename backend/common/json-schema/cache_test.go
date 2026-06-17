package json_schema_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
)

type mockFetcher struct {
	fn func(ctx context.Context) ([]json_schema.DataType, error)
}

func (m *mockFetcher) GetDataTypes(ctx context.Context) ([]json_schema.DataType, error) {
	return m.fn(ctx)
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
	err := cache.RetrieveJsonSchemasToCache(context.Background())
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
	err := cache.RetrieveJsonSchemasToCache(context.Background())
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
	err := cache.RetrieveJsonSchemasToCache(context.Background())
	require.NoError(t, err)

	_, err = cache.ReadByID(context.Background(), uuid.New())
	assert.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}
