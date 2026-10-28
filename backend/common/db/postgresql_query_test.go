package db_test

import (
	"context"
	"testing"

	"entgo.io/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// entQueryContext marks ctx the way an Ent query builder does before it
// calls the driver, which is what pgMultiDriver.Query routes on.
func entQueryContext(ctx context.Context) context.Context {
	return ent.NewQueryContext(ctx, &ent.QueryContext{Type: "Tenant", Op: ent.OpQueryAll})
}

// pgMultiDriver.Query sends Ent queries to the reader pool unless the
// context carries db.WithWriterReads, and every query that is not an Ent
// query (raw SQL, migrations) to the writer. A read that must see every
// committed write (the tenant liveness lookup) uses the marker to reach
// the writer without opening a transaction.
func TestQuery_RoutesByEntQueryAndTheWriterReadsMarker(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		ctx        func(context.Context) context.Context
		wantReader int
		wantWriter int
	}{
		{"ent query", entQueryContext, 1, 0},
		{"ent query marked WithWriterReads", func(ctx context.Context) context.Context {
			return db.WithWriterReads(entQueryContext(ctx))
		}, 0, 1},
		{"marker set before the ent query context", func(ctx context.Context) context.Context {
			return entQueryContext(db.WithWriterReads(ctx))
		}, 0, 1},
		{"raw query", func(ctx context.Context) context.Context { return ctx }, 0, 1},
		{"raw query marked WithWriterReads", db.WithWriterReads, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &fakeDriver{label: "reader"}
			writer := &fakeDriver{label: "writer"}
			driver := db.NewMultiDriverWithDrivers(reader, writer)
			t.Cleanup(func() { assert.NoError(t, driver.Close()) })

			require.NoError(t, driver.Query(tc.ctx(t.Context()), "SELECT 1", []any{}, nil))

			assert.Equal(t, tc.wantReader, reader.queryCalled, "reader queries")
			assert.Equal(t, tc.wantWriter, writer.queryCalled, "writer queries")
			assert.Zero(t, reader.beginTxN+writer.beginTxN+reader.txCalled+writer.txCalled, "a query opens no transaction")
		})
	}
}

// The marker is a plain context value: absent, it reads false; set, it
// reads true and survives derived contexts.
func TestReadsFromWriter_ReportsTheMarker(t *testing.T) {
	t.Parallel()

	assert.False(t, db.ReadsFromWriter(t.Context()))
	marked := db.WithWriterReads(t.Context())
	assert.True(t, db.ReadsFromWriter(marked))
	derived, cancel := context.WithCancel(marked)
	defer cancel()
	assert.True(t, db.ReadsFromWriter(derived))
}
