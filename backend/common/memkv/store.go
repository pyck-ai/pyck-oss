package memkv

import (
	"sync"
	"time"
)

type entry struct {
	value        any
	expiration   int64
	secondaryKey string
}

// InMemoryKVStore is a simple in memory key/value store with optional TTL support
type InMemoryKVStore struct {
	store          map[string]entry
	secondaryIndex map[string]map[string]struct{}
	mu             sync.RWMutex
	ticker         *time.Ticker
	stop           chan struct{}
	stopOnce       sync.Once
}

func NewInMemoryKVStore(cleanupInterval time.Duration) *InMemoryKVStore {
	kv := &InMemoryKVStore{
		store:          make(map[string]entry),
		secondaryIndex: make(map[string]map[string]struct{}),
	}
	if cleanupInterval > 0 {
		kv.ticker = time.NewTicker(cleanupInterval)
		kv.stop = make(chan struct{})
		go kv.cleanupExpiredEntries()
	}
	return kv
}

// Close stops the cleanup ticker and goroutine. Safe on stores
// constructed without a ticker (cleanupInterval == 0) and idempotent.
func (kv *InMemoryKVStore) Close() {
	kv.stopOnce.Do(func() {
		if kv.ticker != nil {
			kv.ticker.Stop()
		}
		if kv.stop != nil {
			close(kv.stop)
		}
	})
}

func (kv *InMemoryKVStore) Set(key string, value any, ttl time.Duration) {
	// Negative TTL is already expired; drop it rather than store it as the ttl==0 "never expires" sentinel (#1169).
	if ttl < 0 {
		return
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()

	// Drop any prior secondary-index link so DeleteBySecondaryKey
	// can't later resurface a key that has been rebound.
	if old, ok := kv.store[key]; ok && old.secondaryKey != "" {
		kv.unindexLocked(key, old.secondaryKey)
	}

	expiration := int64(0)
	if ttl > 0 {
		expiration = time.Now().Add(ttl).UnixNano()
	}

	kv.store[key] = entry{
		value:      value,
		expiration: expiration,
	}
}

func (kv *InMemoryKVStore) Get(key string) (any, bool) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()

	entry, ok := kv.store[key]
	if !ok || (entry.expiration != 0 && time.Now().UnixNano() > entry.expiration) {
		return nil, false
	}
	return entry.value, true
}

func (kv *InMemoryKVStore) Delete(key string) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.deleteLocked(key)
}

// SetWithSecondaryKey stores value under key and registers it in the
// secondary index. DeleteBySecondaryKey then evicts every entry sharing
// that secondary key in O(k) instead of O(N). Empty secondaryKey is
// equivalent to Set. Overwriting an existing key moves the index link.
func (kv *InMemoryKVStore) SetWithSecondaryKey(key string, value any, ttl time.Duration, secondaryKey string) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	if old, ok := kv.store[key]; ok && old.secondaryKey != "" {
		kv.unindexLocked(key, old.secondaryKey)
	}

	expiration := int64(0)
	if ttl > 0 {
		expiration = time.Now().Add(ttl).UnixNano()
	}

	kv.store[key] = entry{
		value:        value,
		expiration:   expiration,
		secondaryKey: secondaryKey,
	}

	if secondaryKey != "" {
		bucket, ok := kv.secondaryIndex[secondaryKey]
		if !ok {
			bucket = make(map[string]struct{})
			kv.secondaryIndex[secondaryKey] = bucket
		}
		bucket[key] = struct{}{}
	}
}

// DeleteBySecondaryKey removes every entry registered under
// secondaryKey via SetWithSecondaryKey and returns the count deleted.
// Lock window is O(k) over the bucket; the rest of the cache is not
// scanned, so concurrent Get on unrelated keys is not held off.
func (kv *InMemoryKVStore) DeleteBySecondaryKey(secondaryKey string) int {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	bucket, ok := kv.secondaryIndex[secondaryKey]
	if !ok {
		return 0
	}
	n := 0
	for key := range bucket {
		delete(kv.store, key)
		n++
	}
	delete(kv.secondaryIndex, secondaryKey)
	return n
}

// deleteLocked removes a key from store and any secondary index link.
// Caller must hold the write lock.
func (kv *InMemoryKVStore) deleteLocked(key string) {
	if e, ok := kv.store[key]; ok && e.secondaryKey != "" {
		kv.unindexLocked(key, e.secondaryKey)
	}
	delete(kv.store, key)
}

// unindexLocked removes one (secondaryKey → key) link and drops the
// bucket once empty so the index doesn't grow unbounded. Caller must
// hold the write lock.
func (kv *InMemoryKVStore) unindexLocked(key, secondaryKey string) {
	bucket, ok := kv.secondaryIndex[secondaryKey]
	if !ok {
		return
	}
	delete(bucket, key)
	if len(bucket) == 0 {
		delete(kv.secondaryIndex, secondaryKey)
	}
}

func (kv *InMemoryKVStore) cleanupExpiredEntries() {
	for {
		select {
		case <-kv.stop:
			return
		case <-kv.ticker.C:
			kv.mu.Lock()
			now := time.Now().UnixNano()
			for key, e := range kv.store {
				if e.expiration != 0 && now > e.expiration {
					kv.deleteLocked(key)
				}
			}
			kv.mu.Unlock()
		}
	}
}

func (kv *InMemoryKVStore) ForEach(f func(key string, value any)) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()

	for key, entry := range kv.store {
		if entry.expiration != 0 && time.Now().UnixNano() > entry.expiration {
			continue
		}
		f(key, entry.value)
	}
}

// DeleteWhere removes all entries for which pred returns true and
// returns the count deleted. Holds the write lock for one pass; the
// predicate must not block or call back into the store.
//
// Prefer DeleteBySecondaryKey when the eviction set shares a known
// secondary key — that path is O(k); this one is O(N).
func (kv *InMemoryKVStore) DeleteWhere(pred func(key string, value any) bool) int {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	n := 0
	for key, e := range kv.store {
		if pred(key, e.value) {
			kv.deleteLocked(key)
			n++
		}
	}
	return n
}
