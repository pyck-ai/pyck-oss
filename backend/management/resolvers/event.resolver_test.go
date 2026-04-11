package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var queryEvents = resolver.ParseTemplate(`query {
	events {
		totalCount
		edges {
			node { id name description topic example }
			cursor
		}
		pageInfo {
			hasNextPage
			hasPreviousPage
			startCursor
			endCursor
		}
	}
}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type eventNode struct {
	ID          uuid.UUID
	Topic       string
	Name        string
	Description string
	Example     map[string]any
}

type queryEventsData struct {
	Events struct {
		TotalCount int
		Edges      []struct {
			Node   eventNode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestEvents_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no events", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryEventsData](te, ctx, queryEvents, nil)

		assert.Equal(t, 0, data.Events.TotalCount)
		assert.Empty(t, data.Events.Edges)
		assert.False(t, data.Events.PageInfo.HasNextPage)
		assert.False(t, data.Events.PageInfo.HasPreviousPage)
		assert.Nil(t, data.Events.PageInfo.StartCursor)
		assert.Nil(t, data.Events.PageInfo.EndCursor)

		// Queries don't emit entity events
		te.assertNoEvents(ctx)
	})

	t.Run("returns events successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		event := te.newEvent(ctx).
			Name("TestEvent").
			Description("Event description").
			Topic("management-test-topic").
			Example(map[string]any{"test": "data", "custom": []any{"test", "check"}}).
			Create()
		te.clearEvents(ctx)

		data := execOK[queryEventsData](te, ctx, queryEvents, nil)

		require.Equal(t, 1, data.Events.TotalCount)
		require.Len(t, data.Events.Edges, 1)

		node := data.Events.Edges[0].Node
		assert.Equal(t, event.ID, node.ID)
		assert.Equal(t, event.Topic, node.Topic)
		assert.Equal(t, event.Name, node.Name)
		assert.Equal(t, event.Description, node.Description)
		assert.Equal(t, event.Example, node.Example)

		assert.NotNil(t, data.Events.PageInfo.StartCursor)
		assert.NotNil(t, data.Events.PageInfo.EndCursor)
		assert.False(t, data.Events.PageInfo.HasNextPage)
		assert.False(t, data.Events.PageInfo.HasPreviousPage)

		// Queries don't emit entity events
		te.assertNoEvents(ctx)
	})
}
