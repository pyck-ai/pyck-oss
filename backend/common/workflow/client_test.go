package workflow_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/workflow"
)

// TestClientCache_NewClientCache verifies cache initialization
func TestClientCache_NewClientCache(t *testing.T) {
	cache := workflow.NewClientCache()

	assert.NotNil(t, cache)
	assert.Equal(t, 0, cache.Count())
}

// TestClientCache_GetSet verifies basic get/set operations
func TestClientCache_GetSet(t *testing.T) {
	cache := workflow.NewClientCache()
	namespace := "test-namespace"

	// Get from empty cache returns nil
	client := cache.Get(namespace)
	assert.Nil(t, client)

	// Set a client
	mockClient := &workflow.Client{}
	cache.Set(namespace, mockClient)

	// Get returns the cached client
	retrieved := cache.Get(namespace)
	assert.Equal(t, mockClient, retrieved)
	assert.Equal(t, 1, cache.Count())
}

// TestClientCache_MultipleNamespaces verifies multi-tenant caching
func TestClientCache_MultipleNamespaces(t *testing.T) {
	cache := workflow.NewClientCache()

	client1 := &workflow.Client{}
	client2 := &workflow.Client{}
	client3 := &workflow.Client{}

	cache.Set("namespace1", client1)
	cache.Set("namespace2", client2)
	cache.Set("namespace3", client3)

	assert.Equal(t, 3, cache.Count())
	assert.Equal(t, client1, cache.Get("namespace1"))
	assert.Equal(t, client2, cache.Get("namespace2"))
	assert.Equal(t, client3, cache.Get("namespace3"))
	assert.Nil(t, cache.Get("nonexistent"))
}

// TestClientCache_Overwrite verifies replacing an existing entry
func TestClientCache_Overwrite(t *testing.T) {
	cache := workflow.NewClientCache()
	namespace := "test-namespace"

	client1 := &workflow.Client{}
	client2 := &workflow.Client{}

	cache.Set(namespace, client1)
	assert.Equal(t, 1, cache.Count())

	// Overwrite with new client
	cache.Set(namespace, client2)
	assert.Equal(t, 1, cache.Count())
	assert.Equal(t, client2, cache.Get(namespace))
}

// TestClientCache_Close verifies cleanup behavior
func TestClientCache_Close(t *testing.T) {
	cache := workflow.NewClientCache()

	// Add multiple clients
	cache.Set("ns1", &workflow.Client{})
	cache.Set("ns2", &workflow.Client{})
	cache.Set("ns3", &workflow.Client{})

	assert.Equal(t, 3, cache.Count())

	// Close the cache
	cache.Close()

	// Verify cache is cleared
	assert.Equal(t, 0, cache.Count())
	assert.Nil(t, cache.Get("ns1"))
	assert.Nil(t, cache.Get("ns2"))
	assert.Nil(t, cache.Get("ns3"))
}

// TestClientCache_ConcurrentAccess verifies thread safety
func TestClientCache_ConcurrentAccess(t *testing.T) {
	cache := workflow.NewClientCache()
	numGoroutines := 100
	numNamespaces := 10

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			namespace := "namespace-" + string(rune('0'+id%numNamespaces))
			cache.Set(namespace, &workflow.Client{})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			namespace := "namespace-" + string(rune('0'+id%numNamespaces))
			_ = cache.Get(namespace)
		}(i)
	}

	// Concurrent counts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cache.Count()
		}()
	}

	wg.Wait()

	// Should have at most numNamespaces entries (due to overwrites)
	assert.LessOrEqual(t, cache.Count(), numNamespaces)
}

// TestClientCache_CloseDuringConcurrentAccess verifies safe shutdown
func TestClientCache_CloseDuringConcurrentAccess(t *testing.T) {
	cache := workflow.NewClientCache()

	// Pre-populate cache
	for i := 0; i < 10; i++ {
		cache.Set("namespace-"+string(rune('0'+i)), &workflow.Client{})
	}

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					namespace := "namespace-" + string(rune('0'+id%10))
					_ = cache.Get(namespace)
				}
			}
		}(i)
	}

	// Give readers time to start
	time.Sleep(10 * time.Millisecond)

	// Close while readers are active
	cache.Close()
	close(done)

	wg.Wait()

	// Verify cache is empty after close
	assert.Equal(t, 0, cache.Count())
}

// TestClientCache_NilClient verifies handling of nil client
func TestClientCache_NilClient(t *testing.T) {
	cache := workflow.NewClientCache()

	// Set nil client
	cache.Set("namespace", nil)

	// Should still be retrievable as nil
	client := cache.Get("namespace")
	assert.Nil(t, client)

	// Count includes nil entries
	assert.Equal(t, 1, cache.Count())

	// Close handles nil gracefully
	assert.NotPanics(t, func() {
		cache.Close()
	})
}

// TestNewDefaultClientFactory verifies factory initialization
func TestNewDefaultClientFactory(t *testing.T) {
	tests := []struct {
		name        string
		cache       *workflow.ClientCache
		expectCache bool
	}{
		{
			name:        "with provided cache",
			cache:       workflow.NewClientCache(),
			expectCache: true,
		},
		{
			name:        "with nil cache creates default",
			cache:       nil,
			expectCache: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := workflow.NewDefaultClientFactory(
				"localhost:7233",
				tt.cache,
			)

			assert.NotNil(t, factory)
		})
	}
}

// TestDefaultClientFactory_GetClient_CachedClient verifies cache hit
func TestDefaultClientFactory_GetClient_CachedClient(t *testing.T) {
	cache := workflow.NewClientCache()
	factory := workflow.NewDefaultClientFactory(
		"localhost:7233",
		cache,
	)

	// Pre-populate cache
	cachedClient := &workflow.Client{}
	cache.Set("test-namespace", cachedClient)

	ctx := t.Context()
	client, err := factory.GetClient(ctx, "test-namespace")

	assert.NoError(t, err)
	assert.Equal(t, cachedClient, client)
	assert.Equal(t, 1, cache.Count()) // Should not create new client
}

// TestDefaultClientFactory_Close verifies cleanup
func TestDefaultClientFactory_Close(t *testing.T) {
	cache := workflow.NewClientCache()
	factory := workflow.NewDefaultClientFactory(
		"localhost:7233",
		cache,
	)

	// Add mock clients to cache
	cache.Set("ns1", &workflow.Client{})
	cache.Set("ns2", &workflow.Client{})

	assert.Equal(t, 2, cache.Count())

	// Close factory
	factory.Close()

	// Verify cache is cleared
	assert.Equal(t, 0, cache.Count())
}

// TestDefaultClientFactory_CloseWithNilCache verifies nil safety
func TestDefaultClientFactory_CloseWithNilCache(t *testing.T) {
	factory := workflow.NewDefaultClientFactory(
		"localhost:7233",
		nil,
	)
	factory.Close()

	// Should not panic
	assert.NotPanics(t, func() {
		factory.Close()
	})
}

// TestClientCreationTimeout_Constant verifies timeout is reasonable
func TestClientCreationTimeout_Constant(t *testing.T) {
	assert.Equal(t, 30*time.Second, workflow.ClientCreationTimeout)
	assert.Greater(t, workflow.ClientCreationTimeout, 1*time.Second, "timeout should be > 1s")
	assert.Less(t, workflow.ClientCreationTimeout, 5*time.Minute, "timeout should be < 5min")
}

// TestClientCache_CountEmptyCache verifies count on empty cache
func TestClientCache_CountEmptyCache(t *testing.T) {
	cache := workflow.NewClientCache()
	assert.Equal(t, 0, cache.Count())
}

// TestClientCache_GetNonExistent verifies missing key behavior
func TestClientCache_GetNonExistent(t *testing.T) {
	cache := workflow.NewClientCache()

	client := cache.Get("does-not-exist")
	assert.Nil(t, client)
}

// TestClientCache_CloseEmptyCache verifies closing empty cache
func TestClientCache_CloseEmptyCache(t *testing.T) {
	cache := workflow.NewClientCache()

	assert.NotPanics(t, func() {
		cache.Close()
	})

	assert.Equal(t, 0, cache.Count())
}

// TestClientCache_MultipleCloseCalls verifies idempotent close
func TestClientCache_MultipleCloseCalls(t *testing.T) {
	cache := workflow.NewClientCache()
	cache.Set("ns1", &workflow.Client{})

	// First close
	cache.Close()
	assert.Equal(t, 0, cache.Count())

	// Second close should be safe
	assert.NotPanics(t, func() {
		cache.Close()
	})
	assert.Equal(t, 0, cache.Count())
}

// TestDefaultClientFactory_ConcurrentGetClient verifies concurrent access
func TestDefaultClientFactory_ConcurrentGetClient(t *testing.T) {
	cache := workflow.NewClientCache()
	factory := workflow.NewDefaultClientFactory(
		"localhost:7233",
		cache,
	)

	// Pre-populate with clients to avoid actual Temporal connections
	for i := 0; i < 10; i++ {
		cache.Set("namespace-"+string(rune('0'+i)), &workflow.Client{})
	}

	var wg sync.WaitGroup
	numGoroutines := 100
	errors := make([]error, numGoroutines)
	clients := make([]*workflow.Client, numGoroutines)

	ctx := t.Context()

	// Concurrent GetClient calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			namespace := "namespace-" + string(rune('0'+idx%10))
			client, err := factory.GetClient(ctx, namespace)
			clients[idx] = client
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// All should succeed (cache hits)
	for i := 0; i < numGoroutines; i++ {
		assert.NoError(t, errors[i], "goroutine %d should not error", i)
		assert.NotNil(t, clients[i], "goroutine %d should get client", i)
	}
}
