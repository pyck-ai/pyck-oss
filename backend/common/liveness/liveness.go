// Package liveness holds the per-service cache of principal liveness
// verdicts ("is this tenant (later: user) still live?")
// consulted on the request path after authentication.
//
// The cache is one store for every kind of principal; keys are URNs of the
// form "<system>:<kind>:<id>" (see [Key] and the constructors) so that each
// key has exactly one source of truth: today "pyck:tenants:<uuid>", read
// from management's tenant table. The namespace is kept so a principal owned
// by another system (users, say) can be added later without two verdicts
// about the same subject sharing a key.
//
// Verdicts enter the cache only from lookups against the source
// ([Cache.LookupTenants]), guarded by the
// generation captured before the lookup started: if any lifecycle event was
// applied meanwhile the result is discarded, so a stale answer can never
// outlive a fresher event. Lifecycle events themselves only EVICT
// ([Cache.Evict], [Cache.EvictForTenantEvent]) — they never assert state. That
// is deliberate: the outbox drainer orders events within one transaction
// group only, and a failed publish is retried later, so a delayed restore
// event can arrive after a later disable event; asserting state from events
// would then serve the wrong verdict for a full TTL, while an eviction at
// worst costs one extra lookup and always converges on the source's truth.
//
// Every entry expires after the configured TTL, which bounds staleness when
// an event is lost. Errors are never cached: a source fault leaves entries
// as they were, reports the unverifiable tenants as not live, and returns
// the error for the caller to log: the fleet denies what it cannot verify
// rather than serving a possibly-deleted tenant. During a source outage
// cached verdicts (both signs) keep serving until their TTL; after that,
// tenant traffic stops until the source answers again.
//
// Because entries expire on a clock rather than on use, every request in
// flight when a hot principal's entry expires misses at the same instant.
// Lookups for the same principal are therefore collapsed into one source
// call (singleflight), so a cold or just-expired entry costs one round-trip
// under load rather than one per request.
package liveness

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/memkv"
)

// ErrMissingDeps rejects a component wired without the liveness cache or
// the tenant source its liveness check needs. Each constructor wraps it
// with the component's name, so the boot error says which wiring is wrong.
var ErrMissingDeps = errors.New("requires a liveness cache and a tenant source")

// ErrUnexpectedLookupType reports a singleflight result that is not a
// tenantLookup — impossible unless the shared lookup below is miswired.
var ErrUnexpectedLookupType = errors.New("unexpected tenant lookup result type")

// Key is the URN of a principal whose liveness is cached, "<system>:<kind>:<id>".
// Build keys with the constructors; the format is part of the cache contract
// because events and lookups must agree on it.
type Key string

const (
	tenantPrefix = "pyck:tenants:"

	// DefaultTTL is the fallback entry TTL when NewCache is given a
	// non-positive duration. Kept short on purpose: the cache trades a
	// small, bounded staleness window for removing a synchronous round-trip
	// from every request, and lifecycle events shorten it further — the TTL
	// only bounds the case where such an event is missed. A zero TTL must
	// never reach the store, where it would make entries permanent.
	DefaultTTL = time.Minute

	// defaultSourceTimeout bounds one call to a liveness source, unless
	// WithSourceTimeout overrides it. The call is
	// detached from the request that triggered it (see lookupMisses), so
	// nothing else would ever cancel it, and not every source bounds itself:
	// management's probe is an Ent query with no statement_timeout, and the
	// other services reach it through a gateway round-trip. A source that
	// accepts the connection and then never
	// answers — the classic load-balancer idle-drop — would hold that key's
	// singleflight slot forever, and every later request about the same
	// principal would attach to it and block on the auth path.
	//
	// Must stay well under the patience of the callers waiting on the auth
	// path and well above a healthy round-trip to management:
	// too high wedges requests, too low turns a slow-but-working source into
	// denied requests for every uncached tenant (unknown is not served).
	defaultSourceTimeout = 10 * time.Second

	// DefaultLookupTimeout bounds how long ONE LookupTenants call waits for a
	// verdict it does not have cached. Every caller sits on a request path
	// (the HTTP auth and acting-set middleware, the NATS auth callout, the
	// Temporal claim mapper, the public file-alias route) so a source that
	// accepts the connection and answers slowly must cost one request its
	// own patience, never the caller's whole budget: without this bound a
	// stalled source holds a goroutine per in-flight request for as long as
	// the source timeout, on paths that have no server deadline of their own.
	//
	// Timing out here denies the uncached tenants of that call, the same
	// verdict a source error produces: a tenant that cannot be verified is
	// not served. That is not the loss it looks like, because the source
	// call is detached and keeps the source timeout to itself (see lookupMisses),
	// so the answer still lands in the cache and the caller's own retry
	// (or the next request about the same tenant) is served from it.
	//
	// A caller whose context expires sooner still wins; this is a ceiling,
	// not a floor.
	DefaultLookupTimeout = time.Second
)

// Option overrides a Cache default; services build their cache from
// envconfig.LivenessConfig with NewCacheFromConfig. A non-positive duration is
// ignored, so an unset config field leaves the default in place rather
// than removing the bound.
type Option func(*Cache)

// WithLookupTimeout overrides DefaultLookupTimeout for this cache.
func WithLookupTimeout(d time.Duration) Option {
	return func(c *Cache) {
		if d > 0 {
			c.lookupTimeout = d
		}
	}
}

// WithSourceTimeout overrides defaultSourceTimeout for this cache.
func WithSourceTimeout(d time.Duration) Option {
	return func(c *Cache) {
		if d > 0 {
			c.sourceTimeout = d
		}
	}
}

// TenantKey is the liveness key of a pyck tenant: live means the tenant row
// is neither soft-deleted nor carrying an unfinished registration. Source of
// truth: management's tenant table, which is the only source this cache has.
// The key stays namespaced by the system that owns the truth so a second one
// (users, say) can be added without colliding.
func TenantKey(id uuid.UUID) Key { return Key(tenantPrefix + id.String()) }

// TenantSource answers, for a set of pyck tenant IDs, which of them must not
// be served. A tenant is served only while its row says so: in management,
// a row that exists, is not soft-deleted, and whose registration finished.
// An ID with no tenant row at all is denied like any other: the contract is
// "allow only what a live row backs", so a token naming a tenant this
// deployment does not have authenticates nothing. A non-nil error is an
// infrastructure fault (management unreachable, decode failure) and means
// unknown state, not a revocation. Two implementations ship: management
// queries its own tenant table, every other service calls management's
// system-only `disabledTenants` query through the gateway.
type TenantSource func(ctx context.Context, tenantIDs []uuid.UUID) (disabled []uuid.UUID, err error)

// Cache is the liveness verdict store. Safe for concurrent use. Construct
// once per process (in main), inject it into every consumer — the auth
// provider, the tenant middleware, the lifecycle-event subscriber — and
// Close it on shutdown.
type Cache struct {
	kv  *memkv.InMemoryKVStore
	ttl time.Duration
	// lookupTimeout and sourceTimeout bound one LookupTenants call and the
	// detached source call it starts; see DefaultLookupTimeout and
	// defaultSourceTimeout for what each one protects. Immutable after
	// construction.
	lookupTimeout time.Duration
	sourceTimeout time.Duration
	// generation counts applied lifecycle events (Evict). Lookups compare
	// it against the value captured before consulting the source and skip
	// the write on mismatch. Coarse on purpose — any concurrent event skips
	// the write at the cost of one more lookup.
	//
	// One counter serves every key, so the cost of an event is paid by every
	// principal on the replica, not only by the evicted one: while events
	// keep landing, no verdict of any kind can be stored and every
	// authenticated request pays a synchronous source round-trip on the auth
	// path. The bursts that produce this are the ones that touch many tenant
	// rows at once — the expiry sweep, a mass restore after an incident, an
	// onboarding import registering N tenants (every tenant row event evicts,
	// not only the two lifecycle transitions).
	//
	// The ceiling while a burst lasts is one source call per distinct acting
	// set, per round-trip time, per service replica: a load spike on
	// management that ends with the burst. That spike is not verdict-safe:
	// consumers of a verdict fail closed, and LookupTenants answers false for
	// every miss whose source call outruns the lookup timeout, so a burst large
	// enough to saturate management denies live tenants until it passes.
	// Accepted rather than scoped per key: a per-key generation would have to
	// be stamped into every stored value and compared on every read, which
	// buys nothing for the steady state that dominates.
	generation atomic.Uint64
	// group collapses concurrent lookups of the same tenant set into one
	// source call, keyed by that set and the generation (see missKey).
	group singleflight.Group
}

// tenantLookup is one shared source answer: the disabled set.
type tenantLookup struct {
	disabled map[uuid.UUID]struct{}
}

// NewCache returns a Cache whose entries expire after ttl (DefaultTTL when
// ttl is not positive). The TTL is the worst-case staleness when a
// lifecycle event is lost, in either direction. The two lookup timeouts
// keep their defaults unless WithLookupTimeout / WithSourceTimeout say
// otherwise.
func NewCache(ttl time.Duration, opts ...Option) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	c := &Cache{
		kv:            memkv.NewInMemoryKVStore(ttl),
		ttl:           ttl,
		lookupTimeout: DefaultLookupTimeout,
		sourceTimeout: defaultSourceTimeout,
	}
	for _, apply := range opts {
		apply(c)
	}
	return c
}

// NewCacheFromConfig returns the Cache a service builds from its
// configuration: TTL, lookup timeout and source timeout all come from cfg,
// with NewCache's defaults for any field that is not positive.
func NewCacheFromConfig(cfg envconfig.LivenessConfig) *Cache {
	return NewCache(cfg.LivenessCacheTTL,
		WithLookupTimeout(cfg.LivenessLookupTimeout),
		WithSourceTimeout(cfg.LivenessSourceTimeout))
}

// LookupTimeout is the configured ceiling of one LookupTenants call. Its
// callers sit inside budgets of their own (the NATS auth callout, the
// Temporal claim mapper) and a lookup that does not fit inside the
// caller's budget denies every uncached principal, so the caller checks
// the relation at construction rather than discovering it in production.
func (c *Cache) LookupTimeout() time.Duration { return c.lookupTimeout }

// Close stops the expiry goroutine. Idempotent.
func (c *Cache) Close() { c.kv.Close() }

// Get returns the cached verdict for key: (live, true) on a hit, (_, false)
// on a miss.
func (c *Cache) Get(key Key) (live bool, ok bool) {
	v, ok := c.kv.Get(string(key))
	if !ok {
		return false, false
	}
	live, ok = v.(bool)
	return live, ok
}

// LookupTenants returns the verdict (live = not disabled) for every tenant
// in ids: from the cache where present, otherwise from one batched source
// call for the misses. Verdicts of both signs are stored, generation-guarded
// (a source answer that overlapped a lifecycle event is dropped, so a stale
// read never overwrites a fresher event). Cached verdicts — including a
// disable applied from its event — are authoritative even when the source
// fails: on error the misses are reported NOT live (a tenant nobody can
// verify is not served, because failing open here would re-admit a deleted
// tenant for the length of a source outage) and the error is returned
// alongside for the caller to log. The error is a fault, not a revocation:
// nothing is cached from it, so service resumes with the first successful
// lookup. Concurrent lookups of the same miss set share one source call
// (see lookupMisses).
func (c *Cache) LookupTenants(ctx context.Context, ids []uuid.UUID, source TenantSource) (map[uuid.UUID]bool, error) {
	live := make(map[uuid.UUID]bool, len(ids))
	var misses []uuid.UUID
	for _, id := range ids {
		if v, ok := c.Get(TenantKey(id)); ok {
			live[id] = v
			continue
		}
		misses = append(misses, id)
	}
	if len(misses) == 0 {
		return live, nil
	}

	// Bound the wait, not the source call: lookupMisses detaches the source
	// call, so the deadline added here governs only how long this caller
	// blocks on it (see DefaultLookupTimeout).
	waitCtx, cancel := context.WithTimeout(ctx, c.lookupTimeout)
	defer cancel()

	lookup, err := c.lookupMisses(waitCtx, misses, source)
	if err != nil {
		for _, id := range misses {
			live[id] = false
		}
		return live, err
	}
	for _, id := range misses {
		_, isDisabled := lookup.disabled[id]
		live[id] = !isDisabled
	}
	return live, nil
}

// lookupMisses asks source about misses, collapsing concurrent lookups of
// the same set into one call. Callers that arrive while a call is in flight
// share its answer instead of issuing their own: entries expire on a TTL, so
// every request in flight when a hot tenant's verdict expires misses at the
// same instant, and without this each of them would open its own round-trip
// to a management that may be slow rather than down.
//
// The shared call runs with the triggering request's cancellation detached
// so that no single caller owns work the others depend on — a request that
// goes away (client disconnect, deadline) must not turn every tenant in the
// shared call into an unknown, and therefore denied, one for everybody
// sharing it. Each caller still stops
// waiting on its own context; the shared call is bounded by the cache's source timeout
// instead, and a late answer still lands in the cache for the next request.
//
// Only callers of the same generation share a call: the generation is part
// of the key, so a caller arriving after an eviction opens a fresh call
// instead of joining one that started before the event and may carry the
// pre-event verdict.
func (c *Cache) lookupMisses(ctx context.Context, misses []uuid.UUID, source TenantSource) (tenantLookup, error) {
	generation := c.generation.Load()
	shared := c.group.DoChan(missKey(misses, generation), func() (any, error) {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.sourceTimeout)
		defer cancel()
		disabled, err := source(callCtx, misses)
		if err != nil {
			return nil, err
		}
		disabledSet := make(map[uuid.UUID]struct{}, len(disabled))
		for _, id := range disabled {
			disabledSet[id] = struct{}{}
		}
		// The verdicts land HERE, inside the shared call, not in the
		// callers: every caller can stop waiting (deadline, disconnect)
		// before the source answers, and the answer must still warm the
		// cache; otherwise a source slower than the fastest caller's
		// patience never populates anything, and a caller with a short
		// deadline (the NATS callout's bounded lookup) is denied on every
		// retry for as long as the source stays merely slow.
		for _, id := range misses {
			_, isDisabled := disabledSet[id]
			c.setFromLookup(generation, TenantKey(id), !isDisabled)
		}
		return tenantLookup{disabled: disabledSet}, nil
	})

	value, err := awaitShared(ctx, shared)
	if err != nil {
		return tenantLookup{}, err
	}
	lookup, ok := value.(tenantLookup)
	if !ok {
		return tenantLookup{}, ErrUnexpectedLookupType
	}
	return lookup, nil
}

// awaitShared waits for a shared source call to answer, or for the caller's
// own context to end — never for another caller's, which is what makes the
// detached call above safe to share.
func awaitShared(ctx context.Context, shared <-chan singleflight.Result) (any, error) {
	select {
	case result := <-shared:
		return result.Val, result.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// missKey is the singleflight key of a tenant set at a generation: the IDs
// sorted and joined, so two requests asking about the same tenants share one
// call whatever order their headers named them in. Keying by the whole set
// rather than by single tenants keeps the source call batched (one
// round-trip for the set, not one per tenant) and collapses the case that
// actually stampedes, many requests resolving the same acting set at once.
// The generation suffix keeps callers on either side of an eviction apart
// (see lookupMisses).
func missKey(ids []uuid.UUID, generation uint64) string {
	sorted := make([]string, len(ids))
	for i, id := range ids {
		sorted[i] = id.String()
	}
	slices.Sort(sorted)
	// Namespaced like the cache keys, so a singleflight slot cannot collide
	// with one of a future principal kind sharing the group.
	return tenantPrefix + strings.Join(sorted, ",") + "@" + strconv.FormatUint(generation, 10)
}

// setFromLookup stores a verdict obtained from a source, unless a lifecycle
// event was applied since gen was captured — then the answer may predate the
// event and is dropped (the next request looks up again). The generation is
// re-checked after the write and the entry removed on mismatch, so an
// eviction that lands between the check and the write cannot be undone by
// it: either the eviction runs after our write (and deletes it), or we see
// the bumped generation and delete it ourselves.
//
// Dropping a stale answer before the write, rather than writing and deleting
// it again, is what keeps the entry a post-event lookup already stored: that
// verdict is fresher than ours, and erasing it would cost every request until
// the next lookup a source round-trip on the auth path.
func (c *Cache) setFromLookup(gen uint64, key Key, live bool) {
	if c.generation.Load() != gen {
		return
	}
	c.kv.Set(string(key), live, c.ttl)
	if c.generation.Load() != gen {
		c.kv.Delete(string(key))
	}
}

// Evict drops the verdict for key so the next request consults the source.
// The generation bump does the rest: a caller arriving after the eviction
// starts a fresh call instead of attaching to one that began before it (see
// lookupMisses), and setFromLookup drops every verdict an in-flight call
// would write. The callers already sharing such a call still get its
// pre-eviction answer for that one request; nothing stale outlives it.
func (c *Cache) Evict(key Key) {
	c.generation.Add(1)
	c.kv.Delete(string(key))
}

// EvictForTenantEvent applies a tenant lifecycle transition (disable,
// restore, or a registration reaching its terminal status): the tenant's
// verdict is dropped so the next request re-reads the row it was derived
// from.
func (c *Cache) EvictForTenantEvent(tenantID uuid.UUID) {
	c.Evict(TenantKey(tenantID))
}
