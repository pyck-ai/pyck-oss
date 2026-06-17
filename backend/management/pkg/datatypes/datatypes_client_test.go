//nolint:testpackage // in-package test required: exercises unexported dataTypeClient + dataTypesFetcher (narrow interface kept internal so it doesn't leak as public API).
package datatypes

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
