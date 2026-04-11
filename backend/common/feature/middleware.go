package feature

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	FeatureHeader = "X-Pyck-Feature"
)

func HTTPMiddleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return &Middleware{next: next}
	}
}

type Middleware struct {
	next http.Handler
}

var _ http.Handler = (*Middleware)(nil)

func (mw *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	features, err := mw.GetFeatures(r)
	if err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("failed to get features")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx = Context(r.Context(), features...)

	mw.next.ServeHTTP(w, r.WithContext(ctx))
}

func (mw *Middleware) GetFeatures(r *http.Request) ([]Feature, error) {
	featureHeaders := r.Header.Values(FeatureHeader)
	seenFeatures := make(map[Feature]struct{}, len(featureHeaders)*2)
	features := make([]Feature, 0)

	for _, featureHeaderValue := range featureHeaders {
		for featureStr := range strings.SplitSeq(featureHeaderValue, ",") {
			featureStr = strings.TrimSpace(featureStr)
			featureStr = strings.ToLower(featureStr)

			if featureStr == "" {
				continue
			}

			feature, err := FeatureString(featureStr)
			if err != nil {
				return nil, fmt.Errorf("%w %q", ErrInvalidFeature, featureStr)
			}

			if _, ok := seenFeatures[feature]; !ok {
				seenFeatures[feature] = struct{}{}
				features = append(features, feature)
			}
		}
	}

	return features, nil
}
