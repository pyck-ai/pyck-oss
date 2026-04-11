package resolvers_test

import (
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var queryTransactions = resolver.ParseTemplate(`query {
	transactions {
		totalCount
		edges {
			node {
				id
				repositoryID
				itemID
				type
				quantity
				createdAt
				createdBy
				updatedAt
				updatedBy
			}
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
