package feature

import (
	"context"
	"slices"
)

type contextKeyFeature struct{}

// Context adds features to the context.
func Context(ctx context.Context, features ...Feature) context.Context {
	features = append([]Feature{}, features...)     // copy to avoid external modification
	features = append(features, ForContext(ctx)...) // append existing features

	return context.WithValue(ctx, contextKeyFeature{}, &features)
}

// ForContext retrieves features from the context.
//
// Returns an empty feature list if no features are found.
func ForContext(ctx context.Context) []Feature {
	if features, ok := ctx.Value(contextKeyFeature{}).(*[]Feature); ok {
		return *features
	}

	return []Feature{}
}

func HasFeature(ctx context.Context, feature Feature) bool {
	features := ForContext(ctx)
	return slices.Contains(features, feature)
}
