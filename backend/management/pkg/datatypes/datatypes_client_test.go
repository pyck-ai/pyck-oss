//nolint:testpackage // in-package test required: exercises unexported dataTypeClient + dataTypesFetcher (narrow interface kept internal so it doesn't leak as public API).
package datatypes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"
)

// fakeFetcher is a stub implementation of dataTypesFetcher that returns
// pre-recorded pages in order and captures each call's pagination args.
type fakeFetcher struct {
	pages []*managementapi.GetDataTypes
	err   error

	calls []managementapi.GetDataTypesArgs
}

func (f *fakeFetcher) GetDataTypes(_ context.Context, input managementapi.GetDataTypesArgs) (*managementapi.GetDataTypes, error) {
	f.calls = append(f.calls, input)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.calls) > len(f.pages) {
		return nil, errors.New("fakeFetcher: more calls than pages")
	}
	return f.pages[len(f.calls)-1], nil
}

func strPtr(s string) *string { return &s }

func makePage(nodes []managementapi.GetDataTypes_DataTypes_Edges_Node, nextCursor *string) *managementapi.GetDataTypes {
	edges := make([]*managementapi.GetDataTypes_DataTypes_Edges, 0, len(nodes))
	for i := range nodes {
		edges = append(edges, &managementapi.GetDataTypes_DataTypes_Edges{Node: &nodes[i]})
	}
	return &managementapi.GetDataTypes{
		DataTypes: managementapi.GetDataTypes_DataTypes{
			Edges: edges,
			PageInfo: managementapi.GetDataTypes_DataTypes_PageInfo{
				HasNextPage: nextCursor != nil,
				EndCursor:   nextCursor,
			},
		},
	}
}

func TestGetDataTypes_PaginatesAcrossPages(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	cursor := "page-2-cursor"

	fake := &fakeFetcher{
		pages: []*managementapi.GetDataTypes{
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: id1.String(), JSONSchema: `{"type":"object"}`, Slug: strPtr("a"), TenantID: tenantID},
				{ID: id2.String(), JSONSchema: `{"type":"array"}`, Slug: strPtr("b"), TenantID: tenantID},
			}, &cursor),
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: id3.String(), JSONSchema: `{"type":"string"}`, Slug: strPtr("c"), TenantID: tenantID},
			}, nil),
		},
	}

	c := &dataTypeClient{client: fake}
	got, err := c.GetDataTypes(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 3)
	assert.Equal(t, []uuid.UUID{id1, id2, id3}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID})
	assert.Equal(t, []string{"a", "b", "c"}, []string{got[0].Slug, got[1].Slug, got[2].Slug})

	require.Len(t, fake.calls, 2)
	assert.Nil(t, fake.calls[0].After, "first page must be requested without cursor")
	require.NotNil(t, fake.calls[1].After)
	assert.Equal(t, cursor, *fake.calls[1].After, "second page must use end cursor from first page")
}

func TestGetDataTypes_NullableSlug(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	fake := &fakeFetcher{
		pages: []*managementapi.GetDataTypes{
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: id.String(), JSONSchema: `{}`, Slug: nil, TenantID: uuid.New()},
			}, nil),
		},
	}

	c := &dataTypeClient{client: fake}
	got, err := c.GetDataTypes(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Slug)
}

func TestGetDataTypes_InvalidIDReturnsError(t *testing.T) {
	t.Parallel()

	fake := &fakeFetcher{
		pages: []*managementapi.GetDataTypes{
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: "not-a-uuid", JSONSchema: `{}`, TenantID: uuid.New()},
			}, nil),
		},
	}

	c := &dataTypeClient{client: fake}
	_, err := c.GetDataTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-uuid")
}

func TestGetDataTypes_FetchError(t *testing.T) {
	t.Parallel()

	fake := &fakeFetcher{err: errors.New("upstream gone")}
	c := &dataTypeClient{client: fake}

	_, err := c.GetDataTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream gone")
}

func TestGetDataTypeBySlug_ReturnsLatestOrderedDesc(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	fake := &fakeFetcher{
		pages: []*managementapi.GetDataTypes{
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: id.String(), JSONSchema: `{"type":"object"}`, Slug: strPtr("widget"), Version: 3, TenantID: uuid.New()},
			}, nil),
		},
	}

	tenantID := uuid.New()
	c := &dataTypeClient{client: fake}
	got, err := c.GetDataTypeBySlug(context.Background(), "widget", tenantID)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, 3, got.Version, "version must be mapped from the node")

	// Verify the query asks for the newest single row by version, scoped to the
	// given tenant (slugs are not unique across tenants).
	require.Len(t, fake.calls, 1)
	call := fake.calls[0]
	require.NotNil(t, call.First)
	assert.Equal(t, 1, *call.First)
	require.NotNil(t, call.Where)
	require.NotNil(t, call.Where.Slug)
	assert.Equal(t, "widget", *call.Where.Slug)
	require.NotNil(t, call.Where.TenantID)
	assert.Equal(t, tenantID, *call.Where.TenantID)
	require.NotNil(t, call.OrderBy)
	assert.Equal(t, managementapi.OrderDirectionDesc, call.OrderBy.Direction)
	require.NotNil(t, call.OrderBy.Field)
	assert.Equal(t, managementapi.DataTypeOrderFieldVersion, *call.OrderBy.Field)
}

func TestGetDataTypeBySlug_NotFound(t *testing.T) {
	t.Parallel()

	fake := &fakeFetcher{pages: []*managementapi.GetDataTypes{makePage(nil, nil)}}
	c := &dataTypeClient{client: fake}

	_, err := c.GetDataTypeBySlug(context.Background(), "missing", uuid.New())
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}

func TestGetDataTypeBySlug_DeletedTreatedAsNotFound(t *testing.T) {
	t.Parallel()

	deletedAt := time.Now().UTC()
	fake := &fakeFetcher{
		pages: []*managementapi.GetDataTypes{
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: uuid.New().String(), JSONSchema: `{}`, Slug: strPtr("widget"), Version: 2, DeletedAt: &deletedAt, TenantID: uuid.New()},
			}, nil),
		},
	}
	c := &dataTypeClient{client: fake}

	_, err := c.GetDataTypeBySlug(context.Background(), "widget", uuid.New())
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}

func TestGetDataTypeByID_FiltersByIDAndMapsVersion(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	fake := &fakeFetcher{
		pages: []*managementapi.GetDataTypes{
			makePage([]managementapi.GetDataTypes_DataTypes_Edges_Node{
				{ID: id.String(), JSONSchema: `{}`, Slug: strPtr("widget"), Version: 7, TenantID: uuid.New()},
			}, nil),
		},
	}

	c := &dataTypeClient{client: fake}
	tenantID := uuid.New()
	got, err := c.GetDataTypeByID(context.Background(), id, tenantID)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, 7, got.Version)

	require.Len(t, fake.calls, 1)
	require.NotNil(t, fake.calls[0].Where)
	require.NotNil(t, fake.calls[0].Where.ID)
	assert.Equal(t, id.String(), *fake.calls[0].Where.ID)
	// The tenant must be an explicit predicate: this client runs under a
	// system token, for which management's tenant privacy filter is skipped.
	require.NotNil(t, fake.calls[0].Where.TenantID, "WhereInput must carry TenantID")
	assert.Equal(t, tenantID, *fake.calls[0].Where.TenantID)
}

func TestGetDataTypeByID_NotFound(t *testing.T) {
	t.Parallel()

	fake := &fakeFetcher{pages: []*managementapi.GetDataTypes{makePage(nil, nil)}}
	c := &dataTypeClient{client: fake}

	_, err := c.GetDataTypeByID(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, json_schema.ErrDataTypeNotFound)
}

func TestMapNode_MapsVersion(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	dt, err := mapNode(&managementapi.GetDataTypes_DataTypes_Edges_Node{
		ID: id.String(), JSONSchema: `{}`, Slug: strPtr("widget"), Version: 4, TenantID: uuid.New(),
	})
	require.NoError(t, err)
	assert.Equal(t, 4, dt.Version, "version maps through from the node")
	assert.Equal(t, "widget", dt.Slug)
}
