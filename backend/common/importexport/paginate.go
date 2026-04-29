package importexport

import "context"

const defaultPageSize = 100

// paginateAll fetches all pages from a List function and returns all nodes.
func paginateAll(ctx context.Context, desc *EntityDescriptor, where map[string]any) ([]map[string]any, error) {
	var all []map[string]any
	var after *string
	pageSize := defaultPageSize

	for {
		result, err := desc.List(ctx, after, &pageSize, where)
		if err != nil {
			return nil, err
		}

		all = append(all, result.Nodes...)

		if !result.HasNextPage {
			break
		}
		after = result.EndCursor
	}

	return all, nil
}
