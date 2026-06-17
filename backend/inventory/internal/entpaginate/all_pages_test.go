package entpaginate_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
)

// TestAllPages_EmitsOrderBy is a regression test for issue #1161.
//
// The generated AllPages helper paginates with LIMIT/OFFSET. Postgres
// guarantees no row order without ORDER BY, so back-to-back page reads
// can return overlapping or missing rows depending on the chosen plan.
// The fix is to bake an ORDER BY into the generator template.
//
// This test captures the SQL emitted by AllPages via ent's debug logger
// and asserts every paginated SELECT carries an ORDER BY clause.
func TestAllPages_EmitsOrderBy(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		queries []string
	)
	capture := func(args ...any) {
		var b strings.Builder
		for i, a := range args {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprint(&b, a)
		}
		mu.Lock()
		queries = append(queries, b.String())
		mu.Unlock()
	}

	client := enttest.Open(t,
		dialect.SQLite,
		testresolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(capture)),
	).Debug()
	defer func() { _ = client.Close() }()

	ctx := entprivacy.DecisionContext(context.Background(), entprivacy.Allow)
	_, err := client.Stock.Query().AllPages(ctx, mixin.Limit)
	require.NoError(t, err)

	mu.Lock()
	captured := append([]string(nil), queries...)
	mu.Unlock()

	var paginated []string
	for _, q := range captured {
		upper := strings.ToUpper(q)
		if strings.Contains(upper, "FROM `STOCKS`") && strings.Contains(upper, "LIMIT 200") {
			paginated = append(paginated, q)
		}
	}

	require.NotEmpty(t, paginated,
		"expected at least one LIMIT-bounded SELECT from AllPages; captured: %v", captured)

	for _, q := range paginated {
		require.Contains(t, strings.ToUpper(q), "ORDER BY",
			"AllPages emitted LIMIT/OFFSET without ORDER BY (#1161): %s", q)
	}
}
