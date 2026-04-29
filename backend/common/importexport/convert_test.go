package importexport_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

type testInput struct {
	Name     string         `json:"name"`
	Data     map[string]any `json:"data,omitempty"`
	ParentID *string        `json:"parentID,omitempty"`
	Default  *bool          `json:"default,omitempty"`
	Count    int            `json:"count"`
}

func TestMapToStruct(t *testing.T) {
	t.Parallel()

	t.Run("basic fields", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"name":  "test-location",
			"count": float64(42), // JSON numbers are float64
		}
		result, err := importexport.MapToStruct[testInput](m)
		require.NoError(t, err)
		assert.Equal(t, "test-location", result.Name)
		assert.Equal(t, 42, result.Count)
		assert.Nil(t, result.ParentID)
	})

	t.Run("pointer fields from non-nil values", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"name":     "test",
			"parentID": "some-uuid",
			"default":  true,
			"count":    float64(0),
		}
		result, err := importexport.MapToStruct[testInput](m)
		require.NoError(t, err)
		require.NotNil(t, result.ParentID)
		assert.Equal(t, "some-uuid", *result.ParentID)
		require.NotNil(t, result.Default)
		assert.True(t, *result.Default)
	})

	t.Run("nested map data", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"name":  "test",
			"data":  map[string]any{"zone": "A1", "weight": float64(5.5)},
			"count": float64(1),
		}
		result, err := importexport.MapToStruct[testInput](m)
		require.NoError(t, err)
		assert.Equal(t, "A1", result.Data["zone"])
		assert.InDelta(t, 5.5, result.Data["weight"], 1e-9)
	})

	t.Run("nil map", func(t *testing.T) {
		t.Parallel()

		result, err := importexport.MapToStruct[testInput](nil)
		require.NoError(t, err)
		assert.Empty(t, result.Name)
	})
}

func TestStructToMap(t *testing.T) {
	t.Parallel()

	t.Run("basic conversion", func(t *testing.T) {
		t.Parallel()
		parentID := "uuid-123"
		input := testInput{
			Name:     "test",
			ParentID: &parentID,
			Count:    10,
		}
		m, err := importexport.StructToMap(input)
		require.NoError(t, err)
		assert.Equal(t, "test", m["name"])
		assert.Equal(t, "uuid-123", m["parentID"])
		assert.InDelta(t, float64(10), m["count"], 1e-9) // JSON numbers are float64
	})

	t.Run("omitempty fields", func(t *testing.T) {
		t.Parallel()
		input := testInput{
			Name:  "test",
			Count: 0,
		}
		m, err := importexport.StructToMap(input)
		require.NoError(t, err)
		_, hasData := m["data"]
		assert.False(t, hasData, "data with omitempty should be absent when nil")
		_, hasParentID := m["parentID"]
		assert.False(t, hasParentID, "parentID with omitempty should be absent when nil")
	})
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	original := map[string]any{
		"name":     "round-trip-test",
		"parentID": "uuid-456",
		"default":  true,
		"data":     map[string]any{"key": "value"},
		"count":    float64(7),
	}

	// map -> struct -> map
	s, err := importexport.MapToStruct[testInput](original)
	require.NoError(t, err)

	result, err := importexport.StructToMap(s)
	require.NoError(t, err)

	assert.Equal(t, original["name"], result["name"])
	assert.Equal(t, original["parentID"], result["parentID"])
	assert.Equal(t, original["default"], result["default"])
	assert.Equal(t, original["count"], result["count"])
}
