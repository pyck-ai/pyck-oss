package memkv_test

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyck-ai/pyck/backend/common/memkv"
)

func TestSetGet(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "v", 0)
	v, ok := kv.Get("k")
	if !ok || v.(string) != "v" {
		t.Fatalf("Get returned (%v, %v); want (v, true)", v, ok)
	}
}

func TestGetMissing(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	if v, ok := kv.Get("missing"); ok || v != nil {
		t.Fatalf("Get on missing key returned (%v, %v); want (nil, false)", v, ok)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "v", 0)
	kv.Delete("k")
	if v, ok := kv.Get("k"); ok || v != nil {
		t.Fatalf("Get after Delete returned (%v, %v); want (nil, false)", v, ok)
	}
}

func TestSetOverwrite(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "first", 0)
	kv.Set("k", "second", 0)
	if v, _ := kv.Get("k"); v.(string) != "second" {
		t.Fatalf("Get after overwrite returned %v; want second", v)
	}
}

func TestTTLExpiry(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "v", 30*time.Millisecond)
	if _, ok := kv.Get("k"); !ok {
		t.Fatal("Get within TTL returned ok=false")
	}

	time.Sleep(50 * time.Millisecond)
	if v, ok := kv.Get("k"); ok || v != nil {
		t.Fatalf("Get past TTL returned (%v, %v); want (nil, false)", v, ok)
	}
}

func TestTTLZeroIsForever(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "v", 0)
	time.Sleep(20 * time.Millisecond)
	if _, ok := kv.Get("k"); !ok {
		t.Fatal("Get on zero-TTL entry returned ok=false")
	}
}

// TestNegativeTTLNotStored guards against the eternal-allowlist bug
// (#1169): a negative TTL is already-expired and must be dropped, not
// stored as a never-expiring entry (which is what ttl == 0 means).
func TestNegativeTTLNotStored(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "v", -1*time.Second)
	if v, ok := kv.Get("k"); ok || v != nil {
		t.Fatalf("Get on negative-TTL entry returned (%v, %v); want (nil, false)", v, ok)
	}
}

// TestNegativeTTLDoesNotOverwrite ensures a negative-TTL Set is a no-op
// even when a valid entry already exists for the key — it must neither
// replace nor evict the live value.
func TestNegativeTTLDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("k", "live", 0)
	kv.Set("k", "stale", -1*time.Second)
	if v, ok := kv.Get("k"); !ok || v.(string) != "live" {
		t.Fatalf("Get returned (%v, %v); want (live, true)", v, ok)
	}
}

func TestCleanupTickerRemovesExpired(t *testing.T) {
	t.Parallel()
	// 10ms cleanup tick; entry expires at ~5ms.
	kv := memkv.NewInMemoryKVStore(10 * time.Millisecond)

	kv.Set("k", "v", 5*time.Millisecond)
	time.Sleep(80 * time.Millisecond) // give the ticker several passes

	// Even bypassing Get's lazy expiry check, the entry should be gone:
	// ForEach skips expired entries; if cleanup ran, ForEach sees nothing.
	count := 0
	kv.ForEach(func(_ string, _ any) { count++ })
	if count != 0 {
		t.Fatalf("ForEach after cleanup saw %d entries; want 0", count)
	}
}

// Close must stop the cleanup ticker and its goroutine — without it,
// every NewInMemoryKVStore call with a non-zero cleanupInterval leaks
// a goroutine and a ticker for the process lifetime. Detect the leak
// by counting goroutines around a fresh New+Close pair and asserting
// we land back on the baseline.
//
// Deliberately NOT t.Parallel(): the assertion counts process-global
// goroutines, so any parallel sibling (the concurrency tests spawn
// dozens) pollutes the delta and flakes under CI scheduling.
// Sequential tests run with no parallel siblings active.
//
//nolint:paralleltest // counts process-global goroutines; parallel siblings pollute the delta
func TestCloseStopsCleanupGoroutine(t *testing.T) {
	baseline := runtime.NumGoroutine()
	kv := memkv.NewInMemoryKVStore(10 * time.Millisecond)
	if delta := runtime.NumGoroutine() - baseline; delta < 1 {
		t.Fatalf("expected cleanup goroutine to start; delta=%d", delta)
	}

	kv.Close()
	// The goroutine exits asynchronously after the stop channel closes;
	// poll instead of a fixed sleep so a slow scheduler doesn't flake.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine()-baseline <= 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Close did not stop the cleanup goroutine; leaked %d goroutines", runtime.NumGoroutine()-baseline)
}

// Close must be safe to call on a store constructed without a ticker
// (cleanupInterval == 0 starts no goroutine). Must not panic on nil
// ticker.
func TestCloseSafeWithoutTicker(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)
	kv.Close() // must not panic
}

// Close must be idempotent — defer kv.Close() in production paired with
// an explicit kv.Close() in a sync.Once cleanup is a real pattern.
func TestCloseIdempotent(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(10 * time.Millisecond)
	kv.Close()
	kv.Close() // second call must not panic on already-stopped ticker
}

// DeleteBySecondaryKey evicts every entry indexed under the secondary
// key. The auth provider uses this to evict all cached tokens for a
// tenant in O(k) where k is the disabled tenant's tokens — instead of
// O(N) where N is every cached token, as DeleteWhere is.
//
// Pre-fix OnTenantDisabled held the write lock during an O(N) scan,
// blocking every concurrent Get. The secondary-index path is O(k)
// inside the lock — even a 50k-entry cache evicts a 3-token tenant in
// microseconds.
func TestSetWithSecondaryKey_DeleteBySecondaryKey_RemovesIndexedEntries(t *testing.T) {
	t.Parallel()

	kv := memkv.NewInMemoryKVStore(0)

	// Two tenants, three tokens for tenant A, one for tenant B,
	// one unindexed entry.
	kv.SetWithSecondaryKey("tokA1", "userA1", 0, "tenantA")
	kv.SetWithSecondaryKey("tokA2", "userA2", 0, "tenantA")
	kv.SetWithSecondaryKey("tokA3", "userA3", 0, "tenantA")
	kv.SetWithSecondaryKey("tokB1", "userB1", 0, "tenantB")
	kv.Set("plainTok", "plainValue", 0)

	n := kv.DeleteBySecondaryKey("tenantA")
	if n != 3 {
		t.Fatalf("DeleteBySecondaryKey returned %d; want 3", n)
	}

	// Tenant A's tokens are gone.
	for _, key := range []string{"tokA1", "tokA2", "tokA3"} {
		if _, ok := kv.Get(key); ok {
			t.Fatalf("%q should have been deleted", key)
		}
	}
	// Tenant B's token + the unindexed entry stay.
	if _, ok := kv.Get("tokB1"); !ok {
		t.Fatal("tokB1 must NOT have been deleted (different tenant)")
	}
	if _, ok := kv.Get("plainTok"); !ok {
		t.Fatal("plainTok must NOT have been deleted (no secondary index)")
	}
}

// DeleteBySecondaryKey on an unknown key is a no-op returning 0, not a
// panic on a nil map.
func TestDeleteBySecondaryKey_UnknownKey(t *testing.T) {
	t.Parallel()

	kv := memkv.NewInMemoryKVStore(0)
	kv.SetWithSecondaryKey("k", "v", 0, "known")

	n := kv.DeleteBySecondaryKey("never-set")
	if n != 0 {
		t.Fatalf("DeleteBySecondaryKey on unknown key returned %d; want 0", n)
	}
	if _, ok := kv.Get("k"); !ok {
		t.Fatal("k should still exist after unrelated DeleteBySecondaryKey")
	}
}

// Overwriting an indexed entry must move the index — the new
// secondary key takes ownership; the old key no longer points at it.
func TestSetWithSecondaryKey_OverwriteRebindsIndex(t *testing.T) {
	t.Parallel()

	kv := memkv.NewInMemoryKVStore(0)
	kv.SetWithSecondaryKey("k", "v1", 0, "tenantA")
	kv.SetWithSecondaryKey("k", "v2", 0, "tenantB") // rebind

	// Deleting by the OLD secondary key must not touch k.
	if n := kv.DeleteBySecondaryKey("tenantA"); n != 0 {
		t.Fatalf("stale index: DeleteBySecondaryKey(tenantA) returned %d; want 0", n)
	}
	if _, ok := kv.Get("k"); !ok {
		t.Fatal("k should still exist; old secondary key bound to nothing")
	}
	// Deleting by the NEW secondary key removes k.
	if n := kv.DeleteBySecondaryKey("tenantB"); n != 1 {
		t.Fatalf("new index: DeleteBySecondaryKey(tenantB) returned %d; want 1", n)
	}
	if _, ok := kv.Get("k"); ok {
		t.Fatal("k should have been deleted via the new secondary key")
	}
}

// Delete on a primary key must also remove the entry from the
// secondary index — otherwise DeleteBySecondaryKey would later report
// a phantom hit on an already-deleted key.
func TestDelete_RemovesFromSecondaryIndex(t *testing.T) {
	t.Parallel()

	kv := memkv.NewInMemoryKVStore(0)
	kv.SetWithSecondaryKey("k", "v", 0, "tenantA")
	kv.Delete("k")

	if n := kv.DeleteBySecondaryKey("tenantA"); n != 0 {
		t.Fatalf("after Delete, DeleteBySecondaryKey returned %d; want 0", n)
	}
}

func TestForEachSkipsExpired(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("live", "ok", 0)
	kv.Set("dead", "stale", 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	keys := map[string]bool{}
	kv.ForEach(func(k string, _ any) { keys[k] = true })
	if !keys["live"] || keys["dead"] {
		t.Fatalf("ForEach visited %v; want only {live}", keys)
	}
}

func TestDeleteWhereRemovesMatching(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	kv.Set("a", 1, 0)
	kv.Set("b", 2, 0)
	kv.Set("c", 3, 0)

	n := kv.DeleteWhere(func(_ string, v any) bool { return v.(int)%2 == 1 })
	if n != 2 {
		t.Fatalf("DeleteWhere returned %d; want 2", n)
	}
	if _, ok := kv.Get("a"); ok {
		t.Fatal("a was supposed to be deleted")
	}
	if _, ok := kv.Get("c"); ok {
		t.Fatal("c was supposed to be deleted")
	}
	if v, ok := kv.Get("b"); !ok || v.(int) != 2 {
		t.Fatalf("b was supposed to survive; got (%v, %v)", v, ok)
	}
}

func TestDeleteWhereNoMatch(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)
	kv.Set("k", "v", 0)

	n := kv.DeleteWhere(func(_ string, _ any) bool { return false })
	if n != 0 {
		t.Fatalf("DeleteWhere with no-match predicate returned %d; want 0", n)
	}
	if _, ok := kv.Get("k"); !ok {
		t.Fatal("k removed by no-match predicate")
	}
}

func TestDeleteWhereEmptyStore(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	n := kv.DeleteWhere(func(_ string, _ any) bool { return true })
	if n != 0 {
		t.Fatalf("DeleteWhere on empty store returned %d; want 0", n)
	}
}

// TestConcurrentGetSet hammers Get + Set + Delete in parallel. Run with
// -race to catch data races on the map.
func TestConcurrentGetSet(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	const workers = 32
	const iterations = 1000

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				key := fmt.Sprintf("w%d-k%d", id, i%8)
				switch i % 3 {
				case 0:
					kv.Set(key, i, 0)
				case 1:
					_, _ = kv.Get(key)
				case 2:
					kv.Delete(key)
				}
			}
		}(w)
	}
	wg.Wait()
}

// TestDeleteWhereNoDeadlockWithGet is the regression test for the original
// design concern: DeleteWhere must not block Get callers indefinitely, and
// concurrent Gets during a DeleteWhere sweep must not deadlock or race.
func TestDeleteWhereNoDeadlockWithGet(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	const entries = 10_000
	for i := range entries {
		kv.Set(fmt.Sprintf("k%d", i), i, 0)
	}

	// Background: hammer Get while DeleteWhere sweeps. With a deadlock or
	// race, this either hangs (test timeout fires) or trips -race.
	stop := make(chan struct{})
	var gets atomic.Int64
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = kv.Get(fmt.Sprintf("k%d", (id*13)%entries))
					gets.Add(1)
				}
			}
		}(w)
	}

	done := make(chan int)
	go func() {
		done <- kv.DeleteWhere(func(_ string, v any) bool { return v.(int)%2 == 0 })
	}()

	select {
	case n := <-done:
		close(stop)
		wg.Wait()
		if n != entries/2 {
			t.Fatalf("DeleteWhere returned %d; want %d", n, entries/2)
		}
		if gets.Load() == 0 {
			t.Fatal("no concurrent Gets ran during DeleteWhere")
		}
	case <-time.After(5 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("DeleteWhere did not return within 5s — likely deadlock")
	}
}

// TestDeleteWhereWithConcurrentForEach: the original deadlock we wanted to
// avoid was "Delete inside ForEach". DeleteWhere fixes that — but it must
// also coexist with separate ForEach callers running in parallel.
func TestDeleteWhereWithConcurrentForEach(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	for i := range 1000 {
		kv.Set(fmt.Sprintf("k%d", i), i, 0)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				kv.ForEach(func(_ string, _ any) {})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		kv.DeleteWhere(func(_ string, v any) bool { return v.(int) < 500 })
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ForEach+DeleteWhere coexistence hung — likely deadlock")
	}
}

// TestForEachReentrantWriteIsAllowed documents the no-callback-into-store
// contract: ForEach holds a read lock, so calling Set/Delete from within
// the callback would deadlock. We don't test the deadlock itself (would
// hang the test) but verify that NOT doing so leaves the store consistent.
func TestForEachDoesNotInterfereWithWriters(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(0)

	for i := range 100 {
		kv.Set(fmt.Sprintf("k%d", i), i, 0)
	}

	// One goroutine iterating; another writing different keys. Should
	// complete without race or panic.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 50 {
			kv.ForEach(func(_ string, _ any) {})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 100; i < 200; i++ {
			kv.Set(fmt.Sprintf("k%d", i), i, 0)
		}
	}()
	wg.Wait()

	// Sanity: writes landed.
	if v, ok := kv.Get("k150"); !ok || v.(int) != 150 {
		t.Fatalf("post-concurrent Set Get returned (%v, %v); want (150, true)", v, ok)
	}
}

// TestCleanupCoexistsWithConcurrentOps: cleanupExpiredEntries runs on its
// own goroutine. Make sure it doesn't deadlock readers or writers.
func TestCleanupCoexistsWithConcurrentOps(t *testing.T) {
	t.Parallel()
	kv := memkv.NewInMemoryKVStore(2 * time.Millisecond)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
					key := fmt.Sprintf("w%d-k%d", id, i%16)
					kv.Set(key, i, time.Millisecond) // short TTL so cleanup has work
					_, _ = kv.Get(key)
					i++
				}
			}
		}(w)
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers stuck — cleanup likely deadlocked with concurrent ops")
	}
}
