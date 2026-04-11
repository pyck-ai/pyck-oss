package feature_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPMiddleware(t *testing.T) {
	// Create a test handler that captures the context
	var capturedFeatures []feature.Feature
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedFeatures = feature.ForContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	middleware := feature.HTTPMiddleware()(testHandler)

	t.Run("no feature headers", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		assert.Empty(t, capturedFeatures)
	})

	t.Run("single feature header", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("multiple features in single header", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted,showdeleted")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1) // Deduplication should occur
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("multiple feature headers", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(feature.FeatureHeader, "showdeleted")
		req.Header.Add(feature.FeatureHeader, "showdeleted")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1) // Deduplication should occur
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("feature with spaces", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "  showdeleted  ")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("feature with uppercase", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "SHOWDELETED")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("feature with mixed case", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "ShowDeleted")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("invalid feature", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "invalid_feature")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid feature")
		assert.Contains(t, rec.Body.String(), "invalid_feature")
	})

	t.Run("mix of valid and invalid features", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted,invalid_feature")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid feature")
		assert.Contains(t, rec.Body.String(), "invalid_feature")
	})

	t.Run("empty feature in list", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted,,showdeleted")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		require.Len(t, capturedFeatures, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, capturedFeatures[0])
	})

	t.Run("only commas", func(t *testing.T) {
		capturedFeatures = nil

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, ",,,")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
		assert.Empty(t, capturedFeatures)
	})
}

func TestMiddleware_getFeatures(t *testing.T) {
	mw := &feature.Middleware{}

	t.Run("no headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		assert.Empty(t, features)
	})

	t.Run("single feature", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted")

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		require.Len(t, features, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
	})

	t.Run("multiple features comma separated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted,showdeleted")

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		require.Len(t, features, 1) // Should deduplicate
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
	})

	t.Run("multiple headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(feature.FeatureHeader, "showdeleted")
		req.Header.Add(feature.FeatureHeader, "showdeleted")

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		require.Len(t, features, 1) // Should deduplicate
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
	})

	t.Run("whitespace handling", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, " showdeleted , showdeleted ")

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		require.Len(t, features, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
	})

	t.Run("case insensitive", func(t *testing.T) {
		testCases := []string{
			"SHOWDELETED",
			"ShowDeleted",
			"showDeleted",
			"showdeleted",
		}

		for _, tc := range testCases {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(feature.FeatureHeader, tc)

			features, err := mw.GetFeatures(req)

			assert.NoError(t, err, "failed for input: %s", tc)
			require.Len(t, features, 1)
			assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
		}
	})

	t.Run("invalid feature", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "nonexistent")

		features, err := mw.GetFeatures(req)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrInvalidFeature))
		assert.Contains(t, err.Error(), "nonexistent")
		assert.Nil(t, features)
	})

	t.Run("empty strings are ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, ",,showdeleted,,")

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		require.Len(t, features, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
	})

	t.Run("preserves order of unique features", func(t *testing.T) {
		// Note: This test would be more meaningful with multiple different features
		// For now, we can only test with the one available feature
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(feature.FeatureHeader, "showdeleted")

		features, err := mw.GetFeatures(req)

		assert.NoError(t, err)
		require.Len(t, features, 1)
		assert.Equal(t, feature.FEATURE_SHOW_DELETED, features[0])
	})
}

func BenchmarkMiddleware_getFeatures(b *testing.B) {
	mw := &feature.Middleware{}

	b.Run("single feature", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted")

		b.ResetTimer()
		for b.Loop() {
			_, _ = mw.GetFeatures(req)
		}
	})

	b.Run("multiple features", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted,showdeleted,showdeleted")

		b.ResetTimer()
		for b.Loop() {
			_, _ = mw.GetFeatures(req)
		}
	})

	b.Run("multiple headers", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(feature.FeatureHeader, "showdeleted")
		req.Header.Add(feature.FeatureHeader, "showdeleted")
		req.Header.Add(feature.FeatureHeader, "showdeleted")

		b.ResetTimer()
		for b.Loop() {
			_, _ = mw.GetFeatures(req)
		}
	})
}

func BenchmarkHTTPMiddleware(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := feature.HTTPMiddleware()(handler)

	b.Run("no features", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		b.ResetTimer()
		for b.Loop() {
			rec.Body.Reset()
			middleware.ServeHTTP(rec, req)
		}
	})

	b.Run("with features", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(feature.FeatureHeader, "showdeleted")
		rec := httptest.NewRecorder()

		b.ResetTimer()
		for b.Loop() {
			rec.Body.Reset()
			middleware.ServeHTTP(rec, req)
		}
	})
}
