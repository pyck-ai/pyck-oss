package dataindex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
)

func TestPoolFor(t *testing.T) {
	t.Parallel()

	t.Run("a declared entity carries its slots", func(t *testing.T) {
		t.Parallel()
		pool, ok := dataindex.PoolFor(dataindex.EntityPickingOrder)
		require.True(t, ok)
		assert.True(t, pool.Has("data_ix_list1"))
		assert.False(t, pool.Has("data_ix_list9"), "pool must not claim a slot the table lacks")
	})

	t.Run("an entity with no slots is reported as such", func(t *testing.T) {
		t.Parallel()
		_, ok := dataindex.PoolFor("something_else")
		assert.False(t, ok)
	})

	t.Run("the pool matches the mixin the schema embeds", func(t *testing.T) {
		t.Parallel()
		// One source of truth: schema and validation cannot disagree on the slots.
		pool, _ := dataindex.PoolFor(dataindex.EntityPickingOrder)
		assert.Equal(t, dataindex.NewSlotPool(dataindex.PoolMixin(dataindex.EntityPickingOrder)), pool)
	})
}

func TestSchemaTypes(t *testing.T) {
	t.Parallel()

	typeAt, err := dataindex.SchemaTypes(`{
	  "properties": {
	    "customerRef":   { "type": "string" },
	    "serialNumbers": { "type": "array" }
	  }
	}`)
	require.NoError(t, err)

	assert.Equal(t, "string", typeAt("/customerRef"))
	assert.Equal(t, "array", typeAt("/serialNumbers"))
	assert.Empty(t, typeAt("/unknown"))
	assert.Empty(t, typeAt("/outer/nested"), "a nested path is left unchecked, not wrongly rejected")
}
