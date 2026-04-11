package feature_test

import (
	"testing"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContext(t *testing.T) {
	t.Run("adds single feature to context", func(t *testing.T) {
		ctx := t.Context()
		feat := feature.FEATURE_SHOW_DELETED

		newCtx := feature.Context(ctx, feat)

		assert.NotNil(t, newCtx)
		assert.NotEqual(t, ctx, newCtx)

		// Verify the feature was added
		features := feature.ForContext(newCtx)
		require.Len(t, features, 1)
		assert.Equal(t, feat, features[0])
	})

	t.Run("adds multiple features to context", func(t *testing.T) {
		ctx := t.Context()
		feature1 := feature.FEATURE_SHOW_DELETED
		feature2 := feature.Feature(100) // Custom feature value
		feature3 := feature.Feature(200) // Another custom feature

		newCtx := feature.Context(ctx, feature1, feature2, feature3)

		assert.NotNil(t, newCtx)

		// Verify all features were added
		features := feature.ForContext(newCtx)
		require.Len(t, features, 3)
		assert.Equal(t, feature1, features[0])
		assert.Equal(t, feature2, features[1])
		assert.Equal(t, feature3, features[2])
	})

	t.Run("adds empty features list to context", func(t *testing.T) {
		ctx := t.Context()

		newCtx := feature.Context(ctx)

		assert.NotNil(t, newCtx)

		// Verify empty features list
		features := feature.ForContext(newCtx)
		assert.Empty(t, features)
	})

	t.Run("extends existing features in context", func(t *testing.T) {
		ctx := t.Context()
		feature1 := feature.FEATURE_SHOW_DELETED
		feature2 := feature.Feature(100)

		// Add initial features
		ctx = feature.Context(ctx, feature1)
		ctx = feature.Context(ctx, feature2)

		// Should only have the new feature
		features := feature.ForContext(ctx)
		require.Len(t, features, 2)
		assert.True(t, feature.HasFeature(ctx, feature1))
		assert.True(t, feature.HasFeature(ctx, feature2))
	})
}

func TestForContext(t *testing.T) {
	t.Run("retrieves features from context", func(t *testing.T) {
		ctx := t.Context()
		feature1 := feature.FEATURE_SHOW_DELETED
		feature2 := feature.Feature(100)

		ctxWithFeatures := feature.Context(ctx, feature1, feature2)

		features := feature.ForContext(ctxWithFeatures)

		require.Len(t, features, 2)
		assert.Equal(t, feature1, features[0])
		assert.Equal(t, feature2, features[1])
	})

	t.Run("returns empty slice when no features in context", func(t *testing.T) {
		ctx := t.Context()

		features := feature.ForContext(ctx)

		assert.NotNil(t, features)
		assert.Empty(t, features)
	})

	t.Run("preserves feature order", func(t *testing.T) {
		ctx := t.Context()
		features := []feature.Feature{feature.Feature(3), feature.Feature(1), feature.Feature(2), feature.FEATURE_SHOW_DELETED}

		ctxWithFeatures := feature.Context(ctx, features...)

		retrieved := feature.ForContext(ctxWithFeatures)

		require.Len(t, retrieved, len(features))
		for i, f := range features {
			assert.Equal(t, f, retrieved[i])
		}
	})
}

func TestHasFeature(t *testing.T) {
	t.Run("returns true when feature exists", func(t *testing.T) {
		ctx := t.Context()
		targetFeature := feature.FEATURE_SHOW_DELETED
		otherFeature := feature.Feature(100)

		ctxWithFeatures := feature.Context(ctx, otherFeature, targetFeature)

		hasFeature := feature.HasFeature(ctxWithFeatures, targetFeature)

		assert.True(t, hasFeature)
	})

	t.Run("returns false when feature doesn't exist", func(t *testing.T) {
		ctx := t.Context()
		feature1 := feature.FEATURE_SHOW_DELETED
		feature2 := feature.Feature(100)
		searchFeature := feature.Feature(200)

		ctxWithFeatures := feature.Context(ctx, feature1, feature2)

		hasFeature := feature.HasFeature(ctxWithFeatures, searchFeature)

		assert.False(t, hasFeature)
	})

	t.Run("returns false for empty context", func(t *testing.T) {
		ctx := t.Context()

		hasFeature := feature.HasFeature(ctx, feature.FEATURE_SHOW_DELETED)

		assert.False(t, hasFeature)
	})

	t.Run("returns false for context with empty features", func(t *testing.T) {
		ctx := t.Context()
		ctxWithEmptyFeatures := feature.Context(ctx)

		hasFeature := feature.HasFeature(ctxWithEmptyFeatures, feature.FEATURE_SHOW_DELETED)

		assert.False(t, hasFeature)
	})

	t.Run("handles multiple identical features correctly", func(t *testing.T) {
		ctx := t.Context()
		feat := feature.FEATURE_SHOW_DELETED

		// Add the same feature multiple times
		ctxWithFeatures := feature.Context(ctx, feat, feat, feat)

		hasFeature := feature.HasFeature(ctxWithFeatures, feat)

		assert.True(t, hasFeature)
	})

	t.Run("finds feature among many", func(t *testing.T) {
		ctx := t.Context()
		features := make([]feature.Feature, 100)
		for i := range features {
			features[i] = feature.Feature(i)
		}
		targetFeature := feature.Feature(50)

		ctxWithFeatures := feature.Context(ctx, features...)

		hasFeature := feature.HasFeature(ctxWithFeatures, targetFeature)

		assert.True(t, hasFeature)
	})
}

func BenchmarkContext(b *testing.B) {
	ctx := b.Context()
	feat := feature.FEATURE_SHOW_DELETED

	b.ResetTimer()
	for b.Loop() {
		_ = feature.Context(ctx, feat)
	}
}

func BenchmarkForContext(b *testing.B) {
	ctx := feature.Context(b.Context(), feature.FEATURE_SHOW_DELETED, feature.Feature(100), feature.Feature(200))

	b.ResetTimer()
	for b.Loop() {
		_ = feature.ForContext(ctx)
	}
}

func BenchmarkHasFeature(b *testing.B) {
	features := make([]feature.Feature, 100)
	for i := range features {
		features[i] = feature.Feature(i)
	}
	ctx := feature.Context(b.Context(), features...)
	searchFeature := feature.Feature(50)

	b.ResetTimer()
	for b.Loop() {
		_ = feature.HasFeature(ctx, searchFeature)
	}
}
