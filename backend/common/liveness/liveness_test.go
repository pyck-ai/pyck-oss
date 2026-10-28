package liveness_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
)

func newCache(t *testing.T, ttl time.Duration) *liveness.Cache {
	t.Helper()
	c := liveness.NewCache(ttl)
	t.Cleanup(c.Close)
	return c
}

// Keys are URNs namespaced by the system that owns the truth, so a tenant
// and its Zitadel org never collide.
func TestKeys(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("019a0f5e-4d3c-7a2b-8f10-1234567890ab")
	assert.Equal(t, liveness.Key("pyck:tenants:019a0f5e-4d3c-7a2b-8f10-1234567890ab"), liveness.TenantKey(id))
	assert.True(t, strings.HasPrefix(string(liveness.TenantKey(id)), "pyck:tenants:"),
		"the key stays namespaced by its source system so a later principal kind cannot collide with it")
}

func TestCache_LookupTenants(t *testing.T) {
	t.Parallel()

	t.Run("caches both signs; only misses reach the source", func(t *testing.T) {
		t.Parallel()
		c := newCache(t, time.Hour)
		on, off, fresh := uuid.New(), uuid.New(), uuid.New()
		src := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{off: {}}}

		live, err := c.LookupTenants(t.Context(), []uuid.UUID{on, off}, src.Lookup)
		require.NoError(t, err)
		assert.Equal(t, map[uuid.UUID]bool{on: true, off: false}, live)

		live, err = c.LookupTenants(t.Context(), []uuid.UUID{on, off, fresh}, src.Lookup)
		require.NoError(t, err)
		assert.Equal(t, map[uuid.UUID]bool{on: true, off: false, fresh: true}, live)
		require.Equal(t, 2, src.Calls())
		assert.Equal(t, []uuid.UUID{fresh}, src.Batches[1], "only the never-seen tenant goes to the source")

		v, ok := c.Get(liveness.TenantKey(off))
		require.True(t, ok)
		assert.False(t, v, "verdicts are stored under the tenant URN")
	})

	t.Run("source error keeps cached verdicts and denies the misses", func(t *testing.T) {
		t.Parallel()
		c := newCache(t, time.Hour)
		off, on, fresh := uuid.New(), uuid.New(), uuid.New()
		src := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{off: {}}}
		_, err := c.LookupTenants(t.Context(), []uuid.UUID{off, on}, src.Lookup)
		require.NoError(t, err)

		src.Err = errors.New("management unreachable")
		live, err := c.LookupTenants(t.Context(), []uuid.UUID{off, on, fresh}, src.Lookup)
		require.ErrorIs(t, err, src.Err, "the fault is reported for logging")
		assert.Equal(t, map[uuid.UUID]bool{off: false, on: true, fresh: false}, live,
			"cached verdicts of both signs keep serving; the unverifiable one is denied")

		src.Err = nil
		live, err = c.LookupTenants(t.Context(), []uuid.UUID{fresh}, src.Lookup)
		require.NoError(t, err)
		assert.True(t, live[fresh], "service resumes with the first successful lookup")
		assert.Equal(t, 3, src.Calls(), "nothing was cached for the miss during the fault")
	})

	t.Run("a late answer from an abandoned lookup still warms the cache", func(t *testing.T) {
		t.Parallel()
		c := newCache(t, time.Hour)
		id := uuid.New()

		// The source answers correctly but only after the caller gave up:
		// the shape of a degraded-but-working management under a
		// short-deadline caller like the NATS callout. It waits for the
		// caller's return rather than sleeping, so a stalled runner cannot
		// make the answer and the deadline ready together.
		var calls atomic.Int32
		callerGaveUp := make(chan struct{})
		slow := func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
			calls.Add(1)
			<-callerGaveUp
			return nil, nil
		}

		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		live, err := c.LookupTenants(ctx, []uuid.UUID{id}, slow)
		close(callerGaveUp)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, live[id], "an abandoned lookup denies the unverified tenant")

		// The detached call must land its verdict even though nobody
		// waited for it; otherwise a caller with a short deadline is
		// denied on every retry for as long as the source stays slow.
		require.Eventually(t, func() bool {
			_, ok := c.Get(liveness.TenantKey(id))
			return ok
		}, time.Second, 5*time.Millisecond, "the late answer must warm the cache")

		failing := &livenessmocks.Source{Err: errors.New("management unreachable")}
		live, err = c.LookupTenants(t.Context(), []uuid.UUID{id}, failing.Lookup)
		require.NoError(t, err, "a cache hit never touches the source")
		assert.True(t, live[id], "the next request is served from the warmed cache")
		assert.Zero(t, failing.Calls())
		assert.Equal(t, int32(1), calls.Load(), "the slow source was asked exactly once")
	})

	t.Run("cached verdict expires mid-outage and the tenant is then denied", func(t *testing.T) {
		t.Parallel()
		// Long enough that the cache hit below lands inside the TTL on a
		// stalled runner; the expiry is then waited for, not slept for.
		c := newCache(t, 250*time.Millisecond)
		id := uuid.New()
		src := &livenessmocks.Source{}
		_, err := c.LookupTenants(t.Context(), []uuid.UUID{id}, src.Lookup)
		require.NoError(t, err)

		src.Err = errors.New("management unreachable")
		live, err := c.LookupTenants(t.Context(), []uuid.UUID{id}, src.Lookup)
		require.NoError(t, err, "a cache hit never touches the source")
		assert.True(t, live[id], "a verified tenant keeps serving through the fault for its TTL")

		// Past the TTL the verdict is gone and the outage is still on:
		// this is the full stop; the TTL bounds how long cached trust
		// carries the fleet, the outage bounds how long the denial lasts.
		require.Eventually(t, func() bool {
			_, ok := c.Get(liveness.TenantKey(id))
			return !ok
		}, 5*time.Second, 5*time.Millisecond, "the verdict must expire after its TTL")
		live, err = c.LookupTenants(t.Context(), []uuid.UUID{id}, src.Lookup)
		require.ErrorIs(t, err, src.Err)
		assert.False(t, live[id], "once the verdict expires mid-outage the tenant is denied")
	})

	t.Run("a lookup overlapping an event is dropped", func(t *testing.T) {
		t.Parallel()
		c := newCache(t, time.Hour)
		id := uuid.New()
		src := &livenessmocks.Source{}
		racing := func(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
			out, err := src.Lookup(ctx, ids)
			c.EvictForTenantEvent(id) // deleteTenant commits while the lookup is out
			return out, err
		}
		live, err := c.LookupTenants(t.Context(), []uuid.UUID{id}, racing)
		require.NoError(t, err)
		assert.True(t, live[id], "the in-flight answer itself is what the source said at read time")
		_, ok := c.Get(liveness.TenantKey(id))
		assert.False(t, ok, "but it must not have been stored: the next request re-checks")
	})
}

// A tenant event evicts the tenant verdict and its org verdict — never
// asserts state — so the next request re-reads both sources.
func TestCache_EvictForTenantEvent(t *testing.T) {
	t.Parallel()

	c := newCache(t, time.Hour)
	tenant := uuid.New()
	src := &livenessmocks.Source{}
	_, err := c.LookupTenants(t.Context(), []uuid.UUID{tenant}, src.Lookup)
	require.NoError(t, err)
	_, ok := c.Get(liveness.TenantKey(tenant))
	require.True(t, ok, "the verdict must be cached, or the eviction proves nothing")

	c.EvictForTenantEvent(tenant)

	_, ok = c.Get(liveness.TenantKey(tenant))
	assert.False(t, ok, "tenant verdict evicted")
}

// Verdicts of either sign expire after the TTL — the fallback that bounds
// staleness when an event is lost.
func TestCache_VerdictsExpire(t *testing.T) {
	t.Parallel()

	c := newCache(t, 20*time.Millisecond)
	on, off := uuid.New(), uuid.New()
	src := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{off: {}}}
	_, err := c.LookupTenants(t.Context(), []uuid.UUID{on, off}, src.Lookup)
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond)
	_, ok := c.Get(liveness.TenantKey(on))
	assert.False(t, ok)
	_, ok = c.Get(liveness.TenantKey(off))
	assert.False(t, ok)
}

// A non-positive TTL falls back to DefaultTTL rather than reaching the store,
// where zero would make entries permanent.
func TestNewCache_ZeroTTLFallsBackToDefault(t *testing.T) {
	t.Parallel()

	require.Positive(t, liveness.DefaultTTL)
	c := newCache(t, 0)
	id := uuid.New()
	_, err := c.LookupTenants(t.Context(), []uuid.UUID{id}, (&livenessmocks.Source{}).Lookup)
	require.NoError(t, err)
	_, ok := c.Get(liveness.TenantKey(id))
	assert.True(t, ok, "entry must be stored under the default TTL")
}

// waitFor receives from ch, failing the test rather than hanging the suite
// when the expected concurrency never materializes.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestCache_LookupTenants_Concurrency(t *testing.T) {
	t.Parallel()

	t.Run("concurrent misses for one set share a single source call", func(t *testing.T) {
		t.Parallel()

		c := newCache(t, time.Hour)
		alive, dead := uuid.New(), uuid.New()
		ids := []uuid.UUID{alive, dead}

		const goroutines = 32
		entered := make(chan struct{}, goroutines)
		release := make(chan struct{})
		var source liveness.TenantSource = func(_ context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
			entered <- struct{}{}
			<-release
			return []uuid.UUID{dead}, nil
		}

		var started, done sync.WaitGroup
		started.Add(goroutines)
		done.Add(goroutines)
		verdicts := make([]map[uuid.UUID]bool, goroutines)
		errs := make([]error, goroutines)
		for i := range goroutines {
			go func() {
				defer done.Done()
				started.Done()
				verdicts[i], errs[i] = c.LookupTenants(t.Context(), ids, source)
			}()
		}
		started.Wait()
		waitFor(t, entered, "the first lookup to reach the source")
		// The source is still blocked, so every other lookup of this set is
		// necessarily concurrent with it. A second arrival here is the
		// per-request fan-out: a cold or just-expired entry under load must
		// cost one round-trip, not one per in-flight request.
		select {
		case <-entered:
			t.Fatal("a second lookup reached the source while one was already in flight")
		case <-time.After(200 * time.Millisecond):
		}
		close(release)
		done.Wait()

		for i := range goroutines {
			require.NoError(t, errs[i])
			assert.True(t, verdicts[i][alive])
			assert.False(t, verdicts[i][dead])
		}
	})

	t.Run("different tenant sets are looked up independently", func(t *testing.T) {
		t.Parallel()

		c := newCache(t, time.Hour)
		first, second := uuid.New(), uuid.New()

		var calls atomic.Int64
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		var source liveness.TenantSource = func(_ context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
			calls.Add(1)
			entered <- struct{}{}
			<-release
			return nil, nil
		}

		var done sync.WaitGroup
		for _, id := range []uuid.UUID{first, second} {
			done.Add(1)
			go func() {
				defer done.Done()
				_, err := c.LookupTenants(t.Context(), []uuid.UUID{id}, source)
				assert.NoError(t, err)
			}()
		}
		waitFor(t, entered, "the first set to reach the source")
		waitFor(t, entered, "the second set to reach the source; collapsing distinct sets would answer one from the other")
		close(release)
		done.Wait()

		assert.Equal(t, int64(2), calls.Load())
	})

	t.Run("a cancelled caller does not cancel the lookup others share", func(t *testing.T) {
		t.Parallel()

		c := newCache(t, time.Hour)
		dead := uuid.New()
		ids := []uuid.UUID{dead}

		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		var source liveness.TenantSource = func(ctx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return []uuid.UUID{dead}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		var leaderErr error
		var done sync.WaitGroup
		done.Add(1)
		go func() {
			defer done.Done()
			_, leaderErr = c.LookupTenants(leaderCtx, ids, source)
		}()
		waitFor(t, entered, "the leader to reach the source")

		var verdicts map[uuid.UUID]bool
		var err error
		done.Add(1)
		go func() {
			defer done.Done()
			verdicts, err = c.LookupTenants(t.Context(), ids, source)
		}()

		cancelLeader()
		close(release)
		done.Wait()

		require.ErrorIs(t, leaderErr, context.Canceled, "the leader stops waiting on its own cancellation")
		require.NoError(t, err, "the follower's own context was never cancelled")
		assert.False(t, verdicts[dead],
			"a caller abandoning the shared lookup must not turn a disabled tenant into an unknown one")
	})
}

// blockingOrgLookup answers active after release is closed, signalling on
// entered when it reaches the source. Used to hold one lookup in flight
// while a lifecycle event is applied underneath it.
func blockingOrgLookup(entered chan<- struct{}, release <-chan struct{}, active bool) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return active, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// blockingTenantLookup is the [liveness.TenantSource] counterpart of
// blockingOrgLookup: it reports disabled after release is closed, signalling
// on entered when it reaches the source.
func blockingTenantLookup(entered chan<- struct{}, release <-chan struct{}, disabled []uuid.UUID) liveness.TenantSource {
	return func(ctx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return disabled, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// A caller arriving after Evict must not join a lookup that started before
// it: that call may carry the pre-eviction verdict. The generation in the
// singleflight key keeps them apart for every acting set, the single tenant
// the auth path sends and the batched set of several X-Pyck-Tenant-Id
// headers, the "all" expansion and the claim mapper's roles map alike.
func TestCache_Evict_PostEvictionCallerOpensAFreshLookup(t *testing.T) {
	t.Parallel()

	for _, size := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d tenants", size), func(t *testing.T) {
			t.Parallel()

			c := newCache(t, time.Hour)
			ids := make([]uuid.UUID, size)
			for i := range ids {
				ids[i] = uuid.New()
			}
			evicted := ids[0]

			// The pre-eviction call reports every tenant live; after the
			// eviction the source reports the evicted one disabled.
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var leaderLive map[uuid.UUID]bool
			var leaderDone sync.WaitGroup
			leaderDone.Add(1)
			go func() {
				defer leaderDone.Done()
				live, err := c.LookupTenants(t.Context(), ids, blockingTenantLookup(entered, release, nil))
				assert.NoError(t, err)
				leaderLive = live
			}()
			waitFor(t, entered, "the pre-eviction lookup to reach the source")

			c.Evict(liveness.TenantKey(evicted))

			var secondCalls atomic.Int64
			got := make(chan map[uuid.UUID]bool, 1)
			go func() {
				live, err := c.LookupTenants(t.Context(), ids, func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
					secondCalls.Add(1)
					return []uuid.UUID{evicted}, nil
				})
				assert.NoError(t, err)
				got <- live
			}()

			select {
			case live := <-got:
				assert.False(t, live[evicted], "the post-eviction caller must get its own lookup's answer, not the pre-eviction one")
			case <-time.After(5 * time.Second):
				t.Fatal("the post-eviction caller joined the pre-eviction call and blocked on it")
			}
			assert.Equal(t, int64(1), secondCalls.Load(), "the post-eviction caller must reach the source itself")

			close(release)
			leaderDone.Wait()
			assert.True(t, leaderLive[evicted], "a caller already sharing the pre-eviction call keeps its answer for that one request")

			// The overlapping call's verdicts were dropped, the fresh call's
			// were stored: the evicted tenant stays disabled in the cache.
			v, ok := c.Get(liveness.TenantKey(evicted))
			assert.True(t, ok, "the post-eviction verdict must be cached")
			assert.False(t, v, "the pre-eviction call must not overwrite the post-eviction verdict")
		})
	}
}

// The source call is detached from the request that triggered it, so nothing
// else can ever cancel it: it must carry a deadline of its own. Without one,
// a source that accepts the connection and never answers holds that key's
// singleflight slot forever and wedges every later request about the same
// principal.
func TestCache_SourceCallsCarryTheirOwnDeadline(t *testing.T) {
	t.Parallel()

	// A request context with no deadline of its own, so any deadline the
	// source sees was put there by the cache.
	ctx := t.Context()

	t.Run("tenant lookup", func(t *testing.T) {
		t.Parallel()
		c := newCache(t, time.Hour)
		var deadline time.Time
		var ok bool
		_, err := c.LookupTenants(ctx, []uuid.UUID{uuid.New()}, func(callCtx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
			deadline, ok = callCtx.Deadline()
			return nil, nil
		})
		require.NoError(t, err)
		require.True(t, ok, "the detached tenant call must be bounded")
		assertAuthPathDeadline(t, deadline)
	})
}

// assertAuthPathDeadline checks a source call's deadline is short enough to
// be useful on the auth path: a bound longer than the patience of the
// requests waiting on it is the same wedge as no bound at all.
func assertAuthPathDeadline(t *testing.T, deadline time.Time) {
	t.Helper()
	remaining := time.Until(deadline)
	assert.Positive(t, remaining)
	assert.Less(t, remaining, time.Minute, "a source bound must stay well under the auth path's patience")
}

// A source answer that predates a lifecycle event is dropped before it is
// written, not written and then removed: the entry it would land on may
// already hold the verdict a lookup started AFTER the event stored. Writing
// and deleting would erase that fresher verdict, so every request until the
// next lookup pays a source round-trip on the auth path — and for the length
// of the write the readers see the pre-event answer.
func TestCache_SetFromLookup_LeavesAFresherVerdictInPlace(t *testing.T) {
	t.Parallel()

	c := newCache(t, time.Hour)
	tenant := uuid.New()
	key := liveness.TenantKey(tenant)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	var staleDone sync.WaitGroup
	staleDone.Add(1)
	go func() {
		defer staleDone.Done()
		live, err := c.LookupTenants(t.Context(), []uuid.UUID{tenant}, blockingTenantLookup(entered, release, nil))
		assert.NoError(t, err)
		assert.True(t, live[tenant])
	}()
	waitFor(t, entered, "the pre-eviction lookup to reach the source")

	// The lifecycle event lands while that probe is still out.
	c.Evict(key)

	// A lookup started after the event stores the current verdict. Evict
	// forgot the in-flight call, so this one reaches the source itself.
	live, err := c.LookupTenants(t.Context(), []uuid.UUID{tenant}, livenessmocks.NoDisabledTenants)
	require.NoError(t, err)
	require.True(t, live[tenant])
	stored, ok := c.Get(key)
	require.True(t, ok, "the post-eviction lookup must have stored its verdict")
	require.True(t, stored)

	// Only now does the pre-event probe answer.
	close(release)
	staleDone.Wait()

	stored, ok = c.Get(key)
	assert.True(t, ok, "an answer predating the event must not remove the verdict stored after it")
	assert.True(t, stored)
}

// A source that accepts the call and then stalls must cost one caller its
// own patience, not the source's whole budget. Every LookupTenants caller
// sits on a request path with no server deadline of its own (the HTTP auth
// and acting-set middleware, the NATS callout, the Temporal claim mapper,
// the public file-alias route) so an unbounded wait holds a goroutine per
// in-flight request for as long as sourceTimeout.
//
// The verdict is still not lost: the source call is detached and keeps its
// own longer bound, so its answer warms the cache and the next request about
// that tenant is served from it.
func TestCache_LookupTenants_StalledSourceBoundsTheCallerAndStillWarmsTheCache(t *testing.T) {
	t.Parallel()

	c := newCache(t, time.Hour)
	tenant := uuid.New()

	release := make(chan struct{})
	var calls atomic.Int32
	source := func(callCtx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
		calls.Add(1)
		select {
		case <-release:
			return nil, nil // nothing disabled: the tenant is live
		case <-callCtx.Done():
			return nil, callCtx.Err()
		}
	}

	start := time.Now()
	live, err := c.LookupTenants(t.Context(), []uuid.UUID{tenant}, source)
	waited := time.Since(start)

	require.Error(t, err, "a caller that stopped waiting must report the fault")
	assert.False(t, live[tenant], "a tenant that could not be verified is not served")
	assert.GreaterOrEqual(t, waited, liveness.DefaultLookupTimeout, "the caller must give the source its bounded wait")
	assert.Less(t, waited, 5*time.Second,
		"the caller must not wait for the source's own budget; that is the wedge this bound exists to prevent")

	// The detached call outlived the caller. Once it answers, its verdict
	// lands in the cache without any caller waiting on it.
	close(release)
	require.Eventually(t, func() bool {
		v, ok := c.Get(liveness.TenantKey(tenant))
		return ok && v
	}, 3*time.Second, 10*time.Millisecond, "the detached source answer must still warm the cache")

	// So the next request is served from the cache and never reaches the source.
	before := calls.Load()
	live, err = c.LookupTenants(t.Context(), []uuid.UUID{tenant}, source)
	require.NoError(t, err)
	assert.True(t, live[tenant], "the warmed verdict serves the next request")
	assert.Equal(t, before, calls.Load(), "a cached verdict must not reach the source again")
}
