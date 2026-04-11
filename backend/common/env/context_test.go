package env_test

import (
	"context"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/env"
	"github.com/stretchr/testify/assert"
)

type TestConfig struct {
	Name    string
	Value   int
	Enabled bool
}

type AnotherTestConfig struct {
	Host string
	Port int
}

func TestContext(t *testing.T) {
	t.Parallel()

	t.Run("stores and retrieves config from context", func(t *testing.T) {
		t.Parallel()

		config := &TestConfig{
			Name:    "test-config",
			Value:   42,
			Enabled: true,
		}

		ctx := t.Context()
		ctx = env.Context(ctx, config)

		// Verify context is not nil
		assert.NotNil(t, ctx)

		// Retrieve the config
		retrieved := env.FromContext[TestConfig](ctx)
		assert.Equal(t, config.Name, retrieved.Name)
		assert.Equal(t, config.Value, retrieved.Value)
		assert.Equal(t, config.Enabled, retrieved.Enabled)
	})

	t.Run("stores nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ctx = env.Context[TestConfig](ctx, nil)

		assert.NotNil(t, ctx)

		// This should panic when trying to retrieve nil config
		assert.Panics(t, func() {
			env.FromContext[TestConfig](ctx)
		})
	})

	t.Run("updates existing config in context", func(t *testing.T) {
		t.Parallel()

		config1 := &TestConfig{
			Name:    "first-config",
			Value:   1,
			Enabled: false,
		}

		config2 := &TestConfig{
			Name:    "second-config",
			Value:   2,
			Enabled: true,
		}

		ctx := t.Context()
		ctx = env.Context(ctx, config1)

		// Verify first config
		retrieved1 := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "first-config", retrieved1.Name)

		// Update with second config
		ctx = env.Context(ctx, config2)

		// Verify second config replaced first
		retrieved2 := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "second-config", retrieved2.Name)
		assert.Equal(t, 2, retrieved2.Value)
	})
}

func TestFromContext(t *testing.T) {
	t.Parallel()

	t.Run("retrieves correct config", func(t *testing.T) {
		t.Parallel()

		config := &TestConfig{
			Name:    "retrieve-test",
			Value:   100,
			Enabled: true,
		}

		ctx := t.Context()
		ctx = env.Context(ctx, config)

		retrieved := env.FromContext[TestConfig](ctx)
		assert.Equal(t, *config, retrieved)
	})

	t.Run("panics when config not in context", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		assert.PanicsWithValue(t, "failed to retrieve config from context", func() {
			env.FromContext[TestConfig](ctx)
		})
	})

	t.Run("panics when wrong type in context", func(t *testing.T) {
		t.Parallel()

		config := &TestConfig{
			Name: "test",
		}

		ctx := t.Context()
		ctx = env.Context(ctx, config)

		// Try to retrieve a different type
		assert.PanicsWithValue(t, "failed to retrieve config from context", func() {
			env.FromContext[AnotherTestConfig](ctx)
		})
	})

	t.Run("panics when nil value stored", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		// Directly use context.WithValue to simulate nil storage
		ctx = context.WithValue(ctx, struct{ field string }{field: "*env_test.TestConfig"}, nil)

		assert.PanicsWithValue(t, "failed to retrieve config from context", func() {
			env.FromContext[TestConfig](ctx)
		})
	})
}

func TestMultipleTypesInContext(t *testing.T) {
	t.Parallel()

	t.Run("stores multiple config types independently", func(t *testing.T) {
		t.Parallel()

		testConfig := &TestConfig{
			Name:    "test",
			Value:   42,
			Enabled: true,
		}

		anotherConfig := &AnotherTestConfig{
			Host: "localhost",
			Port: 8080,
		}

		ctx := t.Context()
		ctx = env.Context(ctx, testConfig)
		ctx = env.Context(ctx, anotherConfig)

		// Retrieve both configs
		retrievedTest := env.FromContext[TestConfig](ctx)
		retrievedAnother := env.FromContext[AnotherTestConfig](ctx)

		// Verify both are correct
		assert.Equal(t, *testConfig, retrievedTest)
		assert.Equal(t, *anotherConfig, retrievedAnother)
	})

	t.Run("different types don't interfere", func(t *testing.T) {
		t.Parallel()

		config1 := &TestConfig{Name: "config1"}
		config2 := &AnotherTestConfig{Host: "host2"}

		ctx := t.Context()

		// Add first type
		ctx = env.Context(ctx, config1)
		retrieved1 := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "config1", retrieved1.Name)

		// Add second type
		ctx = env.Context(ctx, config2)

		// First type should still be accessible
		retrieved1Again := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "config1", retrieved1Again.Name)

		// Second type should be accessible
		retrieved2 := env.FromContext[AnotherTestConfig](ctx)
		assert.Equal(t, "host2", retrieved2.Host)
	})
}

func TestContextKeyFor(t *testing.T) {
	t.Parallel()

	t.Run("generates consistent keys for same type", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		config1 := &TestConfig{Name: "first"}
		config2 := &TestConfig{Name: "second"}

		// Add first config
		ctx = env.Context(ctx, config1)

		// Override with second config (same type)
		ctx = env.Context(ctx, config2)

		// Should get the second config since keys are the same
		retrieved := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "second", retrieved.Name)
	})

	t.Run("generates different keys for different types", func(t *testing.T) {
		t.Parallel()

		// This test verifies that different types can coexist
		ctx := t.Context()

		testConfig := &TestConfig{Name: "test"}
		anotherConfig := &AnotherTestConfig{Host: "host"}

		ctx = env.Context(ctx, testConfig)
		ctx = env.Context(ctx, anotherConfig)

		// Both should be retrievable
		assert.NotPanics(t, func() {
			env.FromContext[TestConfig](ctx)
			env.FromContext[AnotherTestConfig](ctx)
		})
	})
}

func TestContextChaining(t *testing.T) {
	t.Parallel()

	t.Run("maintains parent context values", func(t *testing.T) {
		t.Parallel()

		type parentKey string
		parentValue := "parent-value"

		parentCtx := context.WithValue(t.Context(), parentKey("key"), parentValue)

		config := &TestConfig{Name: "child-config"}
		childCtx := env.Context(parentCtx, config)

		// Parent value should still be accessible
		assert.Equal(t, parentValue, childCtx.Value(parentKey("key")))

		// Config should be accessible
		retrieved := env.FromContext[TestConfig](childCtx)
		assert.Equal(t, "child-config", retrieved.Name)
	})

	t.Run("child context cancellation", func(t *testing.T) {
		t.Parallel()

		parentCtx, cancel := context.WithCancel(t.Context())
		defer cancel()

		config := &TestConfig{Name: "test"}
		childCtx := env.Context(parentCtx, config)

		// Config should be accessible before cancellation
		retrieved := env.FromContext[TestConfig](childCtx)
		assert.Equal(t, "test", retrieved.Name)

		// Cancel parent
		cancel()

		// Child context should be cancelled
		select {
		case <-childCtx.Done():
			// Expected
		default:
			t.Error("child context should be cancelled when parent is cancelled")
		}

		// Config should still be retrievable even after cancellation
		retrievedAfter := env.FromContext[TestConfig](childCtx)
		assert.Equal(t, "test", retrievedAfter.Name)
	})
}

func TestPointerSemantics(t *testing.T) {
	t.Parallel()

	t.Run("modifying original config affects retrieved value", func(t *testing.T) {
		t.Parallel()

		config := &TestConfig{
			Name:    "original",
			Value:   1,
			Enabled: false,
		}

		ctx := t.Context()
		ctx = env.Context(ctx, config)

		// Modify original config
		config.Name = "modified"
		config.Value = 2
		config.Enabled = true

		// Retrieved config should reflect changes since it's the same underlying pointer
		retrieved := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "modified", retrieved.Name)
		assert.Equal(t, 2, retrieved.Value)
		assert.True(t, retrieved.Enabled)
	})

	t.Run("returns copy not pointer", func(t *testing.T) {
		t.Parallel()

		config := &TestConfig{
			Name:    "original",
			Value:   1,
			Enabled: false,
		}

		ctx := t.Context()
		ctx = env.Context(ctx, config)

		// Get the config
		retrieved := env.FromContext[TestConfig](ctx)

		// Modify the retrieved copy
		retrieved.Name = "modified-copy"

		// Original should not be affected
		assert.Equal(t, "original", config.Name)

		// Verify the modification to retrieved actually happened
		assert.Equal(t, "modified-copy", retrieved.Name)

		// Getting it again should return original value
		retrieved2 := env.FromContext[TestConfig](ctx)
		assert.Equal(t, "original", retrieved2.Name)
	})
}

func BenchmarkContext(b *testing.B) {
	config := &TestConfig{
		Name:    "benchmark",
		Value:   42,
		Enabled: true,
	}

	ctx := b.Context()

	for b.Loop() {
		_ = env.Context(ctx, config)
	}
}

func BenchmarkFromContext(b *testing.B) {
	config := &TestConfig{
		Name:    "benchmark",
		Value:   42,
		Enabled: true,
	}

	ctx := b.Context()
	ctx = env.Context(ctx, config)

	for b.Loop() {
		_ = env.FromContext[TestConfig](ctx)
	}
}

func BenchmarkMultipleTypes(b *testing.B) {
	testConfig := &TestConfig{
		Name:    "test",
		Value:   42,
		Enabled: true,
	}

	anotherConfig := &AnotherTestConfig{
		Host: "localhost",
		Port: 8080,
	}

	ctx := b.Context()
	ctx = env.Context(ctx, testConfig)
	ctx = env.Context(ctx, anotherConfig)

	for b.Loop() {
		_ = env.FromContext[TestConfig](ctx)
		_ = env.FromContext[AnotherTestConfig](ctx)
	}
}

// TestFromContextConcurrent verifies thread safety
func TestFromContextConcurrent(t *testing.T) {
	t.Parallel()

	config := &TestConfig{
		Name:    "concurrent",
		Value:   42,
		Enabled: true,
	}

	ctx := t.Context()
	ctx = env.Context(ctx, config)

	// Run multiple goroutines accessing the context
	const numGoroutines = 100
	done := make(chan bool, numGoroutines)

	for range numGoroutines {
		go func() {
			defer func() { done <- true }()

			retrieved := env.FromContext[TestConfig](ctx)
			assert.Equal(t, "concurrent", retrieved.Name)
			assert.Equal(t, 42, retrieved.Value)
			assert.True(t, retrieved.Enabled)
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}
