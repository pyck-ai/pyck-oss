package importexport_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

func newTestRegistry(t *testing.T) *importexport.Registry {
	t.Helper()

	reg := importexport.NewRegistry()
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{
		TypeName:      "Location",
		Service:       "management",
		IdentityField: "name",
		List: func(_ context.Context, _ *string, _ *int, where map[string]any) (importexport.ListResult, error) {
			// Simulate finding "Building-A" with ID "loc-123".
			if where["name"] == "Building-A" {
				return importexport.ListResult{
					Nodes: []map[string]any{{"id": "loc-123", "name": "Building-A"}},
				}, nil
			}
			return importexport.ListResult{}, nil
		},
	}))
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{
		TypeName:      "Repository",
		Service:       "inventory",
		IdentityField: "name",
		List: func(_ context.Context, _ *string, _ *int, where map[string]any) (importexport.ListResult, error) {
			if where["name"] == "Warehouse-A" {
				return importexport.ListResult{
					Nodes: []map[string]any{{"id": "repo-456", "name": "Warehouse-A"}},
				}, nil
			}
			return importexport.ListResult{}, nil
		},
	}))
	return reg
}

func TestResolveRefs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("resolves single $ref", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"name": "Shelf-1",
			"locationID": map[string]any{
				"$ref": map[string]any{
					"__typename": "Location",
					"name":       "Building-A",
				},
			},
		}

		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "loc-123", data["locationID"])
	})

	t.Run("resolves multiple $refs", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"name": "Shelf-1",
			"locationID": map[string]any{
				"$ref": map[string]any{
					"__typename": "Location",
					"name":       "Building-A",
				},
			},
			"parentID": map[string]any{
				"$ref": map[string]any{
					"__typename": "Repository",
					"name":       "Warehouse-A",
				},
			},
		}

		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "loc-123", data["locationID"])
		assert.Equal(t, "repo-456", data["parentID"])
	})

	t.Run("uses cache for repeated refs", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		reg2 := importexport.NewRegistry()
		require.NoError(t, reg2.Register(&importexport.EntityDescriptor{
			TypeName:      "Location",
			Service:       "management",
			IdentityField: "name",
			List: func(_ context.Context, _ *string, _ *int, _ map[string]any) (importexport.ListResult, error) {
				callCount++
				return importexport.ListResult{
					Nodes: []map[string]any{{"id": "loc-123", "name": "Building-A"}},
				}, nil
			},
		}))
		r := importexport.NewRefResolver(reg2)

		for range 3 {
			data := map[string]any{
				"locationID": map[string]any{
					"$ref": map[string]any{"__typename": "Location", "name": "Building-A"},
				},
			}
			require.NoError(t, r.ResolveRefs(ctx, data))
		}
		assert.Equal(t, 1, callCount, "should only query API once, then use cache")
	})

	t.Run("Track populates cache", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		r := importexport.NewRefResolver(reg)
		r.Track("Location", map[string]any{"name": "New-Location"}, "loc-new")

		data := map[string]any{
			"locationID": map[string]any{
				"$ref": map[string]any{"__typename": "Location", "name": "New-Location"},
			},
		}
		require.NoError(t, r.ResolveRefs(ctx, data))
		assert.Equal(t, "loc-new", data["locationID"])
	})

	t.Run("error on not found", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"locationID": map[string]any{
				"$ref": map[string]any{"__typename": "Location", "name": "Unknown"},
			},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.Contains(t, err.Error(), "Unknown")
	})

	t.Run("error on unknown type", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"fooID": map[string]any{
				"$ref": map[string]any{"__typename": "NonExistent", "name": "x"},
			},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown type")
	})

	t.Run("resolves $ref in arrays", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"itemIDs": []any{
				map[string]any{
					"$ref": map[string]any{"__typename": "Location", "name": "Building-A"},
				},
			},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "loc-123", data["itemIDs"].([]any)[0])
	})

	t.Run("leaves non-ref maps untouched", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"data": map[string]any{"zone": "A1", "weight": float64(5)},
			"name": "test",
		}
		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "A1", data["data"].(map[string]any)["zone"])
	})

	t.Run("resolves string $ref via alias", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		require.NoError(t, resolver.TrackAlias("customer-acme", "cust-789"))

		data := map[string]any{
			"customerID": map[string]any{"$ref": "customer-acme"},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "cust-789", data["customerID"])
	})

	t.Run("resolves string $ref in arrays", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		require.NoError(t, resolver.TrackAlias("item-1", "id-aaa"))
		require.NoError(t, resolver.TrackAlias("item-2", "id-bbb"))

		data := map[string]any{
			"itemIDs": []any{
				map[string]any{"$ref": "item-1"},
				map[string]any{"$ref": "item-2"},
			},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "id-aaa", data["itemIDs"].([]any)[0])
		assert.Equal(t, "id-bbb", data["itemIDs"].([]any)[1])
	})

	t.Run("error on unknown alias", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		data := map[string]any{
			"orderID": map[string]any{"$ref": "nonexistent-alias"},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.ErrorIs(t, err, importexport.ErrUnknownAlias)
		assert.Contains(t, err.Error(), "nonexistent-alias")
	})

	t.Run("error on duplicate alias", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		require.NoError(t, resolver.TrackAlias("my-alias", "id-1"))
		err := resolver.TrackAlias("my-alias", "id-2")
		require.ErrorIs(t, err, importexport.ErrDuplicateAlias)
		assert.Contains(t, err.Error(), "my-alias")
	})

	t.Run("mixes object and string $ref", func(t *testing.T) {
		t.Parallel()

		reg := newTestRegistry(t)
		resolver := importexport.NewRefResolver(reg)

		require.NoError(t, resolver.TrackAlias("customer-acme", "cust-789"))

		data := map[string]any{
			"customerID": map[string]any{"$ref": "customer-acme"},
			"locationID": map[string]any{
				"$ref": map[string]any{"__typename": "Location", "name": "Building-A"},
			},
		}
		err := resolver.ResolveRefs(ctx, data)
		require.NoError(t, err)
		assert.Equal(t, "cust-789", data["customerID"])
		assert.Equal(t, "loc-123", data["locationID"])
	})
}
