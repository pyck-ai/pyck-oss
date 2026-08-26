package workflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeTemporal overrides Close on the (otherwise unused) Temporal client
// interface so factory lifecycle tests can count closes without a server.
type fakeTemporal struct {
	temporalclient.Client
	closed *atomic.Int64
}

func (f fakeTemporal) Close() { f.closed.Add(1) }

// newFakeRoot returns an injectable root client whose Close calls are counted
// in the returned counter.
func newFakeRoot() (fakeTemporal, *atomic.Int64) {
	closed := &atomic.Int64{}
	return fakeTemporal{closed: closed}, closed
}

// stubbedFactory returns a factory over a fake root whose miss path is the
// given constructor instead of the real namespace setup.
func stubbedFactory(newClient func(ctx context.Context, namespace string) (*Client, error)) *DefaultClientFactory {
	root, _ := newFakeRoot()
	f := NewDefaultClientFactory(context.Background(), root, "localhost:7233", DefaultClientCreationTimeout)
	f.newClient = newClient
	return f
}

// TestGetClient_MemoizedPerNamespace verifies the constructor runs once per
// namespace and repeated calls return the identical client.
func TestGetClient_MemoizedPerNamespace(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	f := stubbedFactory(func(_ context.Context, namespace string) (*Client, error) {
		calls.Add(1)
		return &Client{namespace: namespace}, nil
	})

	ctx := t.Context()
	first, err := f.GetClient(ctx, "tenant-a")
	require.NoError(t, err)
	second, err := f.GetClient(ctx, "tenant-a")
	require.NoError(t, err)
	other, err := f.GetClient(ctx, "tenant-b")
	require.NoError(t, err)

	assert.Same(t, first, second)
	assert.NotSame(t, first, other)
	assert.Equal(t, int64(2), calls.Load())
}

// TestGetClient_EmptyNamespaceAliasesDefault verifies "" and the SDK default
// namespace share one entry.
func TestGetClient_EmptyNamespaceAliasesDefault(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	f := stubbedFactory(func(_ context.Context, namespace string) (*Client, error) {
		calls.Add(1)
		return &Client{namespace: namespace}, nil
	})

	ctx := t.Context()
	byEmpty, err := f.GetClient(ctx, "")
	require.NoError(t, err)
	byName, err := f.GetClient(ctx, temporalclient.DefaultNamespace)
	require.NoError(t, err)

	assert.Same(t, byEmpty, byName)
	assert.Equal(t, int64(1), calls.Load())
}

// TestGetClient_ConcurrentFirstRequestsCollapse verifies concurrent first
// requests for one namespace produce exactly one constructor call and one
// shared client — the double-setup race the old cache had.
func TestGetClient_ConcurrentFirstRequestsCollapse(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	f := stubbedFactory(func(_ context.Context, namespace string) (*Client, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		return &Client{namespace: namespace}, nil
	})

	const goroutines = 32
	clients := make([]*Client, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := f.GetClient(context.Background(), "tenant-a")
			assert.NoError(t, err)
			clients[i] = c
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), calls.Load())
	for i := 1; i < goroutines; i++ {
		assert.Same(t, clients[0], clients[i])
	}
}

// TestGetClient_ErrorIsNotMemoized verifies a failed creation is retried on
// the next request instead of being cached.
func TestGetClient_ErrorIsNotMemoized(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	boom := errors.New("temporal unavailable")
	f := stubbedFactory(func(_ context.Context, namespace string) (*Client, error) {
		if calls.Add(1) == 1 {
			return nil, boom
		}
		return &Client{namespace: namespace}, nil
	})

	ctx := t.Context()
	_, err := f.GetClient(ctx, "tenant-a")
	require.ErrorIs(t, err, boom)

	client, err := f.GetClient(ctx, "tenant-a")
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, int64(2), calls.Load())
}

// TestGetClient_SetupOutlivesCancelledRequest verifies the namespace setup is
// triggered by the first request but never owned by it: a cancelled caller
// stops waiting, while the setup keeps running under the factory's lifecycle
// context and its result is memoized for the next request.
func TestGetClient_SetupOutlivesCancelledRequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	setupCtxErr := make(chan error, 1)
	var calls atomic.Int64

	f := stubbedFactory(func(ctx context.Context, namespace string) (*Client, error) {
		calls.Add(1)
		<-release
		setupCtxErr <- ctx.Err()
		return &Client{namespace: namespace}, nil
	})

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := f.GetClient(requestCtx, "tenant-a")
		errCh <- err
	}()

	cancelRequest()
	require.ErrorIs(t, <-errCh, context.Canceled, "cancelled caller must stop waiting")

	close(release)
	require.NoError(t, <-setupCtxErr, "setup ctx must not inherit the request's cancellation")

	client, err := f.GetClient(context.Background(), "tenant-a")
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, int64(1), calls.Load(), "the abandoned setup must be memoized, not re-run")
}

// TestGetClient_SetupCtxDerivesFromLifecycleCtx verifies the miss-path setup
// context is parented on the factory's lifecycle context: cancelling the
// lifecycle context (shutdown) cancels an in-flight setup.
func TestGetClient_SetupCtxDerivesFromLifecycleCtx(t *testing.T) {
	t.Parallel()

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	root, _ := newFakeRoot()
	f := NewDefaultClientFactory(rootCtx, root, "localhost:7233", DefaultClientCreationTimeout)
	f.newClient = func(ctx context.Context, _ string) (*Client, error) {
		cancelRoot()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, err := f.GetClient(context.Background(), "tenant-a")
	require.ErrorIs(t, err, context.Canceled)
}

// TestClose_ClosesDerivedClientsAndRootOnce verifies Close releases every
// handed-out client and the root exactly once, including when the default
// namespace entry wraps the root client itself.
func TestClose_ClosesDerivedClientsAndRootOnce(t *testing.T) {
	t.Parallel()

	var derivedClosed atomic.Int64
	root, rootClosed := newFakeRoot()

	f := NewDefaultClientFactory(context.Background(), root, "localhost:7233", DefaultClientCreationTimeout)
	f.clients = map[string]*Client{
		temporalclient.DefaultNamespace: {namespace: temporalclient.DefaultNamespace, temporal: root},
		"tenant-a":                      {namespace: "tenant-a", temporal: fakeTemporal{closed: &derivedClosed}},
		"tenant-b":                      {namespace: "tenant-b", temporal: fakeTemporal{closed: &derivedClosed}},
	}

	f.Close()

	assert.Equal(t, int64(1), rootClosed.Load(), "root must be closed exactly once")
	assert.Equal(t, int64(2), derivedClosed.Load())
	assert.Empty(t, f.clients)

	assert.NotPanics(t, func() { f.Close() })
	assert.Equal(t, int64(1), rootClosed.Load(), "second Close must be a no-op")
}

// TestClose_ClosesRootWithoutDefaultEntry verifies the root connection is
// released even when no client for the default namespace was ever created.
func TestClose_ClosesRootWithoutDefaultEntry(t *testing.T) {
	t.Parallel()

	root, rootClosed := newFakeRoot()
	f := NewDefaultClientFactory(context.Background(), root, "localhost:7233", DefaultClientCreationTimeout)

	f.Close()

	assert.Equal(t, int64(1), rootClosed.Load())
}

// TestNewDefaultClientFactory_CreationTimeout verifies the configured setup
// timeout is stored and a non-positive value falls back to the default.
func TestNewDefaultClientFactory_CreationTimeout(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 30*time.Second, DefaultClientCreationTimeout)

	root, _ := newFakeRoot()
	f := NewDefaultClientFactory(context.Background(), root, "localhost:7233", 5*time.Second)
	assert.Equal(t, 5*time.Second, f.creationTimeout)

	unconfigured, _ := newFakeRoot()
	u := NewDefaultClientFactory(context.Background(), unconfigured, "localhost:7233", 0)
	assert.Equal(t, DefaultClientCreationTimeout, u.creationTimeout)
}

// TestGetClient_AfterCloseReturnsError verifies a closed factory refuses new
// requests instead of deriving from a connection that is being torn down.
func TestGetClient_AfterCloseReturnsError(t *testing.T) {
	t.Parallel()

	f := stubbedFactory(func(_ context.Context, namespace string) (*Client, error) {
		return &Client{namespace: namespace}, nil
	})

	f.Close()

	_, err := f.GetClient(t.Context(), "tenant-a")
	require.ErrorIs(t, err, ErrClientFactoryClosed)
}

// TestClose_DuringCreationClosesCreatedClient verifies that a creation
// in-flight when Close runs does not store or leak its client: the caller
// gets ErrClientFactoryClosed and the freshly created client is released.
func TestClose_DuringCreationClosesCreatedClient(t *testing.T) {
	t.Parallel()

	creating := make(chan struct{})
	closed := make(chan struct{})
	var derivedClosed atomic.Int64

	f := stubbedFactory(func(_ context.Context, namespace string) (*Client, error) {
		close(creating)
		<-closed // hold the creation until Close has finished
		return &Client{namespace: namespace, temporal: fakeTemporal{closed: &derivedClosed}}, nil
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := f.GetClient(context.Background(), "tenant-a")
		errCh <- err
	}()

	<-creating
	f.Close()
	close(closed)

	require.ErrorIs(t, <-errCh, ErrClientFactoryClosed)
	assert.Equal(t, int64(1), derivedClosed.Load(), "the orphaned client must be released")
}

// fakeOperatorService overrides the two operator RPCs addSearchAttributes
// uses; everything else on the embedded interface panics if called.
type fakeOperatorService struct {
	operatorservice.OperatorServiceClient
	list func(*operatorservice.ListSearchAttributesRequest) (*operatorservice.ListSearchAttributesResponse, error)
	add  func(*operatorservice.AddSearchAttributesRequest) (*operatorservice.AddSearchAttributesResponse, error)
}

func (f fakeOperatorService) ListSearchAttributes(_ context.Context, in *operatorservice.ListSearchAttributesRequest, _ ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error) {
	return f.list(in)
}

func (f fakeOperatorService) AddSearchAttributes(_ context.Context, in *operatorservice.AddSearchAttributesRequest, _ ...grpc.CallOption) (*operatorservice.AddSearchAttributesResponse, error) {
	return f.add(in)
}

// operatorTemporal exposes a fake operator service on the otherwise unused
// Temporal client interface.
type operatorTemporal struct {
	temporalclient.Client
	operator fakeOperatorService
}

func (f operatorTemporal) OperatorService() operatorservice.OperatorServiceClient {
	return f.operator
}

// TestAddSearchAttributes_RetriesStillMissingOnAlreadyExists verifies the
// list-then-add race fix: when the batched add fails with AlreadyExists
// because another actor registered one attribute in between, the remaining
// genuinely-missing attributes are re-derived and registered rather than
// silently skipped.
func TestAddSearchAttributes_RetriesStillMissingOnAlreadyExists(t *testing.T) {
	t.Parallel()

	racedAttr := SearchAttributes[0].GetName()
	var lists, adds atomic.Int64
	var lastAdd map[string]enums.IndexedValueType

	operator := fakeOperatorService{
		list: func(*operatorservice.ListSearchAttributesRequest) (*operatorservice.ListSearchAttributesResponse, error) {
			existing := map[string]enums.IndexedValueType{}
			if lists.Add(1) > 1 {
				// After the raced first add, one attribute exists.
				existing[racedAttr] = SearchAttributes[0].GetValueType()
			}
			return &operatorservice.ListSearchAttributesResponse{CustomAttributes: existing}, nil
		},
		add: func(in *operatorservice.AddSearchAttributesRequest) (*operatorservice.AddSearchAttributesResponse, error) {
			lastAdd = in.GetSearchAttributes()
			if adds.Add(1) == 1 {
				return nil, status.Error(codes.AlreadyExists, "search attribute already exists")
			}
			return &operatorservice.AddSearchAttributesResponse{}, nil
		},
	}

	root, _ := newFakeRoot()
	f := NewDefaultClientFactory(context.Background(), root, "localhost:7233", DefaultClientCreationTimeout)
	err := f.addSearchAttributes(t.Context(), operatorTemporal{operator: operator}, "tenant-a")

	require.NoError(t, err)
	assert.Equal(t, int64(2), adds.Load())
	assert.Len(t, lastAdd, len(SearchAttributes)-1)
	assert.NotContains(t, lastAdd, racedAttr, "retry must only send still-missing attributes")
}

// TestAddSearchAttributes_PersistentAlreadyExistsSurfaces verifies the retry
// is bounded: an AlreadyExists that never resolves is returned to the caller
// (so the client is not memoized and the next request retries) instead of
// looping forever or being swallowed.
func TestAddSearchAttributes_PersistentAlreadyExistsSurfaces(t *testing.T) {
	t.Parallel()

	var adds atomic.Int64
	operator := fakeOperatorService{
		list: func(*operatorservice.ListSearchAttributesRequest) (*operatorservice.ListSearchAttributesResponse, error) {
			return &operatorservice.ListSearchAttributesResponse{}, nil
		},
		add: func(*operatorservice.AddSearchAttributesRequest) (*operatorservice.AddSearchAttributesResponse, error) {
			adds.Add(1)
			return nil, status.Error(codes.AlreadyExists, "search attribute already exists")
		},
	}

	root, _ := newFakeRoot()
	f := NewDefaultClientFactory(context.Background(), root, "localhost:7233", DefaultClientCreationTimeout)
	err := f.addSearchAttributes(t.Context(), operatorTemporal{operator: operator}, "tenant-a")

	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(errors.Unwrap(err)))
	assert.Equal(t, int64(searchAttributeAttempts), adds.Load())
}

// TestWaitOrCancel_HonoursCancelledContext pins the cancellation check that
// precedes the wait. A zero delay makes the timer fire at once, so both
// select cases are ready and an unguarded implementation returns nil about
// half the time; the loop turns that coin flip into a certain failure.
func TestWaitOrCancel_HonoursCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := range 500 {
		require.ErrorIs(t, waitOrCancel(ctx, 0), context.Canceled,
			"iteration %d must abort on the cancelled context", i)
	}
}

// TestJitteredBackoff_StaysWithinCap pins the retry envelope: every attempt
// waits a positive delay and never exceeds searchAttributeBackoffMax, so
// growth cannot drift past the bound the batching relies on.
func TestJitteredBackoff_StaysWithinCap(t *testing.T) {
	t.Parallel()

	for attempt := 1; attempt <= 8; attempt++ {
		for range 50 {
			delay := jitteredBackoff(attempt)
			require.Positive(t, delay, "attempt %d", attempt)
			require.LessOrEqual(t, delay, searchAttributeBackoffMax, "attempt %d exceeds the cap", attempt)
		}
	}
}
