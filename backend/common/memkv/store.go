package memkv

import (
	"sync"
	"time"
)

type entry struct {
	value      interface{}
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

func (kv *InMemoryKVStore) Set(key string, value interface{}, ttl time.Duration) {
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

func (kv *InMemoryKVStore) Get(key string) (interface{}, bool) {
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

func (kv *InMemoryKVStore) ForEach(f func(key string, value interface{})) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()

	for key, entry := range kv.store {
		if entry.expiration != 0 && time.Now().UnixNano() > entry.expiration {
			continue
		}
		f(key, entry.value)
	}
}
