package resolvers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRepositoryDataContainsBoolRefusesWrongShape pins that the repository's
// DataContainsBool filter refuses an input it cannot use instead of dropping
// the predicate, which returned the caller's whole table. A well-formed filter
// still applies: no repository holds the flag, so none is returned.
func TestRepositoryDataContainsBoolRefusesWrongShape(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)
	te.newRepository(ctx, userA).Name("data-filter-1").Create()
	te.newRepository(ctx, userA).Name("data-filter-2").Create()

	for _, where := range []string{
		`{DataContainsBool: {key: "active"}}`,
		`{DataContainsBool: {value: true}}`,
		`{DataContainsBool: {}}`,
		`{DataContainsBool: {key: "", value: true}}`,
	} {
		execErr(te, ctx, queryRepositories, map[string]any{"Where": where}, "invalid JSON data filter")
	}

	data := execOK[queryRepositoriesData](te, ctx, queryRepositories, map[string]any{
		"Where": `{DataContainsBool: {key: "no-such-flag", value: true}}`,
	})
	assert.Equal(t, 0, data.Repositories.TotalCount, "a well-formed filter must still narrow the result")
}
