package main

import (
	"io/fs"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/server/schema"

	"github.com/pyck-ai/pyck/backend/common/workflow"
)

// Pins MaxStoredIDLength to the columns of the postgres12 store we run, so a
// Temporal upgrade that widens or narrows them fails here.
func TestMaxStoredIDLength_MatchesPostgresSchema(t *testing.T) {
	t.Parallel()

	for file, columns := range map[string][]string{
		"postgresql/v12/temporal/schema.sql":   {"workflow_id", "task_queue_name"},
		"postgresql/v12/visibility/schema.sql": {"workflow_id", "workflow_type_name", "task_queue"},
	} {
		sql, err := fs.ReadFile(schema.Assets(), file)
		require.NoError(t, err)

		for _, column := range columns {
			matches := regexp.MustCompile(`(?m)^\s*`+column+`\s+VARCHAR\((\d+)\)`).FindAllSubmatch(sql, -1)
			require.NotEmpty(t, matches, "%s: no VARCHAR column %s", file, column)
			for _, m := range matches {
				n, err := strconv.Atoi(string(m[1]))
				require.NoError(t, err)
				require.Equal(t, workflow.MaxStoredIDLength, n, "%s: %s", file, column)
			}
		}
	}
}
