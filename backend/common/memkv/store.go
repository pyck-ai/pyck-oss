package memkv

import (
	"sync"
	"time"
)

type entry struct {
	value      any
	expiration int64
}

// InMemoryKVStore is a simple in memory key/value store with optional TTL support
type InMemoryKVStore struct {
	store  map[string]entry
	mu     sync.RWMutex
	ticker *time.Ticker
}

func NewInMemoryKVStore(cleanupInterval time.Duration) *InMemoryKVStore {
	kv := &InMemoryKVStore{
		store: make(map[string]entry),
	}
	if cleanupInterval > 0 {
		kv.ticker = time.NewTicker(cleanupInterval)
		go kv.cleanupExpiredEntries()
	}
	return kv
}

func (kv *InMemoryKVStore) Set(key string, value any, ttl time.Duration) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

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
	delete(kv.store, key)
}

func (kv *InMemoryKVStore) cleanupExpiredEntries() {
	for range kv.ticker.C {
		kv.mu.Lock()
		for key, entry := range kv.store {
			if entry.expiration != 0 && time.Now().UnixNano() > entry.expiration {
				delete(kv.store, key)
			}
		}
		kv.mu.Unlock()
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

// DeleteWhere removes all entries for which pred returns true and returns
// the count deleted. Holds the write lock for one pass; the predicate must
// not block or call back into the store. Safe to delete during range in Go.
func (kv *InMemoryKVStore) DeleteWhere(pred func(key string, value any) bool) int {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	n := 0
	for key, entry := range kv.store {
		if pred(key, entry.value) {
			delete(kv.store, key)
			n++
		}
	}
	return n
}
