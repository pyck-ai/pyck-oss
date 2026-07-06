package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/mocks"
	"github.com/pyck-ai/pyck/backend/common/env/config"
)

// Stop() must Close() the auth provider so its memkv cleanup goroutine
// and ticker are released. Pre-fix Stop() only stopped revocationCC /
// pgAdapter / server, and the provider's goroutine leaked for the
// process lifetime — the exact leak ZitadelAuthProvider.Close() was
// added to prevent. Detect it by counting goroutines around a
// construct+Stop pair.
//
// Not t.Parallel(): the assertion counts process-global goroutines.
// This package has no parallel siblings, but the directive keeps the
// invariant explicit (and satisfies the paralleltest linter).
//
//nolint:paralleltest // counts process-global goroutines; parallel siblings pollute the delta
func TestTemporalServer_StopClosesAuthProvider(t *testing.T) {
	baseline := runtime.NumGoroutine()

	provider := authn.NewZitadelAuthProvider(
		mocks.NewMockZitadelClient(),
		config.ZitadelConfig{ZitadelPATCacheTTL: 10 * time.Millisecond},
		func(context.Context, string) (bool, error) { return true, nil },
	)
	if delta := runtime.NumGoroutine() - baseline; delta < 1 {
		t.Fatalf("expected the provider cache cleanup goroutine to start; delta=%d", delta)
	}

	s := &temporalServer{authProvider: provider}
	require.NoError(t, s.Stop())

	// The goroutine exits asynchronously after Close() stops the ticker;
	// poll rather than sleep a fixed interval.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine()-baseline <= 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if delta := runtime.NumGoroutine() - baseline; delta > 0 {
		t.Fatalf("Stop did not close the auth provider; leaked %d goroutines", delta)
	}

	// Stop is idempotent — a second call (e.g. runServer's
	// cleanup-after-failed-start path followed by the deferred Stop)
	// must not panic on the already-closed provider.
	require.NoError(t, s.Stop())
}
