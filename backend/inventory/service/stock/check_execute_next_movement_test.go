//nolint:testpackage // in-package test required: checkExecuteNextMovementByPosition is a private *service method.
package stock

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestCheckExecuteNextMovementByPosition_IndexBoundedExistProbes pins Step
// 9.5 / FINDINGS §3.7. Before this change checkExecuteNextMovementByPosition
// fetched every non-deleted movement in the collection via two All() scans
// and iterated in Go to find any with a smaller position. With the
// (collection_id, position) composite indexes added in Phases 1.2 / 1.4
// already in place, the cheaper shape is an Exist() probe bounded by
// PositionLT(position): an index range scan that stops at the first row.
//
// This test seeds a collection with N item movements at positions 0..N-1
// (all non-executed) and asserts:
//
//   - The probe at position 0 (no earlier rows can exist) returns nil.
//   - The probe at position N-1 returns errOutOfOrderExecution because the
//     N-1 earlier rows are still pending.
//   - Each invocation emits exactly two SELECTs (one per movements table),
//     and each SELECT carries a LIMIT 1 — i.e. the implementation never
//     fans out to All().
//
// The capture path uses ent's debug logger (the same pattern as
// internal/entpaginate.TestAllPages_EmitsOrderBy).
func TestCheckExecuteNextMovementByPosition_IndexBoundedExistProbes(t *testing.T) {
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
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	mkRepo := func(name string, parent uuid.UUID) uuid.UUID {
		t.Helper()
		b := client.Repository.Create().
			SetTenantID(tenantID).
			SetName(name).
			SetType(entrepository.TypeStatic).
			SetVirtualRepo(false)
		if parent != uuid.Nil {
			b.SetParentID(parent)
		}
		repo, err := b.Save(ctx)
		require.NoError(t, err)
		return repo.ID
	}

	rootID := mkRepo("root", uuid.Nil)
	fromID := mkRepo("from", rootID)
	toID := mkRepo("to", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("CHECK-EXECUTE-NEXT-INDEX").
		Save(ctx)
	require.NoError(t, err)

	collectionID := uuid.New()
	const n = 5

	// Seed N non-executed item movements at positions 0..N-1, all sharing
	// the same collectionID so the (collection_id, position) probe has
	// real rows to range over.
	for i := range n {
		_, err = client.ItemMovement.Create().
			SetTenantID(tenantID).
			SetItemID(item.ID).
			SetFromID(fromID).
			SetToID(toID).
			SetHandler("test").
			SetQuantity(1).
			SetExecuted(false).
			SetCollectionID(collectionID).
			SetPosition(i).
			Save(ctx)
		require.NoError(t, err)
	}

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := &service{}

	// Reset capture so we only see queries from the probe under test.
	resetCapture := func() {
		mu.Lock()
		queries = queries[:0]
		mu.Unlock()
	}
	// The debug-driver capture entries look like:
	//   Tx(<id>).Query: query=SELECT ... args=[...]
	// We only care about SELECTs against the two movements tables.
	collectMovementSelects := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, q := range queries {
			upper := strings.ToUpper(q)
			if !strings.Contains(upper, "QUERY=SELECT") {
				continue
			}
			if strings.Contains(upper, `"ITEM_MOVEMENTS"`) ||
				strings.Contains(upper, "`ITEM_MOVEMENTS`") ||
				strings.Contains(upper, `"REPOSITORY_MOVEMENTS"`) ||
				strings.Contains(upper, "`REPOSITORY_MOVEMENTS`") {
				out = append(out, q)
			}
		}
		return out
	}

	// Probe at position 0: no earlier movements can exist, so the call
	// must return nil and emit exactly two SELECTs (one per table), each
	// with LIMIT 1.
	resetCapture()
	require.NoError(t, svc.checkExecuteNextMovementByPosition(ctx, tx, collectionID, 0))
	gotZero := collectMovementSelects()
	require.Len(t, gotZero, 2,
		"position-0 probe must emit exactly 2 SELECTs (one per movements table); got: %v", gotZero)
	for _, q := range gotZero {
		require.Contains(t, strings.ToUpper(q), "LIMIT 1",
			"probe SELECT must be LIMIT 1 (Exist), not an All() scan: %s", q)
	}

	// Probe at position N-1: there are N-1 earlier non-executed rows, so
	// the call must return errOutOfOrderExecution. The query budget is
	// the same: at most 2 SELECTs, each with LIMIT 1. Using "at most"
	// here covers the implementation's short-circuit on the first table.
	resetCapture()
	err = svc.checkExecuteNextMovementByPosition(ctx, tx, collectionID, n-1)
	require.ErrorIs(t, err, errOutOfOrderExecution,
		"probe with pending earlier rows must return errOutOfOrderExecution")
	gotLast := collectMovementSelects()
	require.NotEmpty(t, gotLast,
		"position-(N-1) probe must emit at least 1 SELECT")
	require.LessOrEqual(t, len(gotLast), 2,
		"position-(N-1) probe must emit at most 2 SELECTs (no All() fan-out); got: %v", gotLast)
	for _, q := range gotLast {
		require.Contains(t, strings.ToUpper(q), "LIMIT 1",
			"probe SELECT must be LIMIT 1 (Exist), not an All() scan: %s", q)
	}

	// Pin the predicate shape: every probe SELECT must filter on
	// collection_id (the leading column of the composite index added in
	// Phases 1.2 / 1.4) and on position (the second column). Without both
	// the planner cannot execute this as an index range probe.
	for _, q := range append(append([]string(nil), gotZero...), gotLast...) {
		require.Contains(t, q, entitemmovement.FieldCollectionID,
			"probe SELECT must filter on collection_id: %s", q)
		require.Contains(t, q, entitemmovement.FieldPosition,
			"probe SELECT must filter on position: %s", q)
	}
}
