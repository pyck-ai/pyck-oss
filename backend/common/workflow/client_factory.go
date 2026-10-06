package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
)

const (
	// DefaultClientCreationTimeout is the fallback bound on the lazy
	// per-namespace client setup for callers that have no env-derived
	// configuration; services pass config.TemporalClientCreationTimeout.
	DefaultClientCreationTimeout = 30 * time.Second

	// searchAttributeAttempts bounds the list-then-add retry in
	// addSearchAttributes. Only a handful of actors ever register the same
	// namespace concurrently, so one retry is the realistic worst case.
	searchAttributeAttempts = 3

	// searchAttributeBackoffBase and searchAttributeBackoffMax bound the
	// exponential backoff between addSearchAttributes retries, so a hot loop
	// of AlreadyExists retries does not itself trip Temporal's per-namespace
	// rate limiter, which batching these calls exists to avoid.
	searchAttributeBackoffBase = 50 * time.Millisecond
	searchAttributeBackoffMax  = 400 * time.Millisecond
)

// ErrUnexpectedClientType reports a singleflight result that is not a
// *Client — impossible unless the miss-path constructor is miswired.
var ErrUnexpectedClientType = errors.New("unexpected client type")

// ErrClientFactoryClosed is returned by GetClient once Close has run, so a
// request racing shutdown cannot derive a client from a connection that is
// being torn down.
var ErrClientFactoryClosed = errors.New("client factory is closed")

type ClientFactory interface {
	GetClient(ctx context.Context, namespace string) (*Client, error)
	Close()
}

// DefaultClientFactory hands out namespace-scoped workflow Clients that all
// share the injected root client's single gRPC connection: per-namespace
// clients are derived from the root via client.NewClientFromExisting. A
// derived client holds no connection or goroutines of its own, so entries for
// tenants that have since been disabled or deleted cost a map entry, not
// network resources.
//
// The per-namespace map also memoizes the one-time namespace setup
// (namespace creation + search-attribute registration): it runs exactly once
// per namespace, with concurrent first requests collapsed by singleflight so
// a race cannot run the setup twice.
type DefaultClientFactory struct {
	temporalURL string

	// creationTimeout bounds each lazy per-namespace setup attempt. Immutable
	// after construction.
	creationTimeout time.Duration

	group singleflight.Group

	// rootCtx is the application lifecycle context the factory was built
	// under. Lazy namespace setup runs on contexts derived from it — never
	// from the triggering request's — so a finished request cannot cancel
	// setup that later requests depend on, while shutdown (root cancellation)
	// still stops it.
	//
	//nolint:containedctx // Lifecycle context for factory-owned background setup, à la http.Server.BaseContext.
	rootCtx context.Context

	// root is the injected, already-dialed client owning the factory's single
	// gRPC connection. Immutable after construction — createClient reads it
	// without holding mu — and released by Close.
	root temporalclient.Client

	// mu guards clients and closed. It is only ever held for map and field
	// access, never across network calls.
	mu      sync.Mutex
	clients map[string]*Client
	closed  bool

	// newClient is the miss-path constructor, a seam so unit tests can
	// exercise the memoization and lifecycle logic without a Temporal server.
	// Production wiring is f.createClient.
	newClient func(ctx context.Context, namespace string) (*Client, error)
}

// NewDefaultClientFactory wraps an already-dialed root Temporal client
// (services obtain one at startup via temporal.NewTemporalClient). The
// factory takes ownership of root: Close releases it together with every
// derived client. rootCtx must be the application lifecycle context — it
// parents the lazy per-namespace setup, which the first request for a
// namespace triggers but never owns. creationTimeout bounds each setup
// attempt (DefaultClientCreationTimeout if non-positive).
func NewDefaultClientFactory(rootCtx context.Context, root temporalclient.Client, temporalURL string, creationTimeout time.Duration) *DefaultClientFactory {
	if creationTimeout <= 0 {
		creationTimeout = DefaultClientCreationTimeout
	}
	f := &DefaultClientFactory{
		temporalURL:     temporalURL,
		creationTimeout: creationTimeout,
		rootCtx:         rootCtx,
		root:            root,
		clients:         make(map[string]*Client),
	}
	f.newClient = f.createClient
	return f
}

func (f *DefaultClientFactory) GetClient(ctx context.Context, namespace string) (*Client, error) {
	if namespace == "" {
		namespace = temporalclient.DefaultNamespace
	}

	client, ok, err := f.cachedClient(namespace)
	if err != nil {
		return nil, err
	}
	if ok {
		return client, nil
	}

	// Errors are returned but not memoized: the next request for the
	// namespace retries the whole creation.
	//
	// DoChan rather than Do so the caller only waits, never owns: when the
	// requesting context is cancelled the caller unblocks, but the setup
	// keeps running under the factory's lifecycle context and memoizes for
	// the next request.
	//nolint:contextcheck // The setup intentionally ignores the caller's ctx and runs under rootCtx: see the comment above.
	ch := f.group.DoChan(namespace, func() (any, error) {
		client, ok, err := f.cachedClient(namespace)
		if err != nil {
			return nil, err
		}
		if ok {
			return client, nil
		}

		// The setup must outlive the triggering request but not the process:
		// derive from the lifecycle context, bounded by its own deadline so
		// an unreachable Temporal fails the waiting request instead of
		// wedging until shutdown.
		createCtx, cancel := context.WithTimeout(f.rootCtx, f.creationTimeout)
		defer cancel()

		created, err := f.newClient(createCtx, namespace)
		if err != nil {
			return nil, err
		}

		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			created.Close()
			return nil, ErrClientFactoryClosed
		}
		f.clients[namespace] = created
		f.mu.Unlock()
		return created, nil
	})

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for Temporal client for namespace %q: %w", namespace, ctx.Err())
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		client, ok := res.Val.(*Client)
		if !ok {
			return nil, fmt.Errorf("%w: %T for namespace %q", ErrUnexpectedClientType, res.Val, namespace)
		}
		return client, nil
	}
}

// cachedClient returns the memoized client for the namespace, or an error if
// the factory has been closed.
func (f *DefaultClientFactory) cachedClient(namespace string) (*Client, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, false, ErrClientFactoryClosed
	}
	client, ok := f.clients[namespace]
	return client, ok, nil
}

// createClient builds the namespace-scoped client on first use of a
// namespace: run the one-time namespace setup and derive a client over the
// root client's shared connection.
func (f *DefaultClientFactory) createClient(ctx context.Context, namespace string) (*Client, error) {
	if namespace == temporalclient.DefaultNamespace {
		return NewClient(namespace, f.root)
	}

	if err := f.ensureNamespaceExists(ctx, namespace); err != nil {
		return nil, fmt.Errorf("failed to ensure namespace exists: %w", err)
	}

	nsClient, err := f.deriveNamespaceClient(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to derive Temporal client for namespace %q: %w", namespace, err)
	}

	if err := f.addSearchAttributes(ctx, nsClient, namespace); err != nil {
		// Closing a derived client only releases its reference on the shared
		// connection; the root client keeps the connection alive.
		nsClient.Close()
		return nil, err
	}

	wfClient, err := NewClient(namespace, nsClient)
	if err != nil {
		nsClient.Close()
		return nil, fmt.Errorf("failed to create workflow client: %w", err)
	}

	return wfClient, nil
}

// deriveNamespaceClient creates a namespace-scoped client over the root
// client's connection. Interceptors are per-client in the Temporal SDK, so
// the derived client gets its own tracing interceptor and namespace-tagged
// logger.
//
//nolint:ireturn // temporalclient.NewClientFromExistingWithContext only yields the client.Client interface.
func (f *DefaultClientFactory) deriveNamespaceClient(ctx context.Context, namespace string) (temporalclient.Client, error) {
	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		return nil, fmt.Errorf("create tracing interceptor: %w", err)
	}

	clientLogger := log.ForContext(ctx).With().
		Str("component", "temporal-client").
		Str("namespace", namespace).
		Logger()

	return temporalclient.NewClientFromExistingWithContext(ctx, f.root, temporalclient.Options{
		Namespace:    namespace,
		Logger:       logadapter.TemporalSDKLogAdapter(clientLogger),
		Interceptors: []interceptor.ClientInterceptor{tracingInterceptor},
	})
}

func (f *DefaultClientFactory) ensureNamespaceExists(ctx context.Context, namespace string) error {
	_, err := f.root.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        namespace,
		WorkflowExecutionRetentionPeriod: &durationpb.Duration{Seconds: 86400},
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to create namespace %q: %w", namespace, err)
	}

	return nil
}

// addSearchAttributes registers the pyck search attributes that are not yet
// present on the namespace. It reads the existing set first and batches all
// missing attributes into one request: per-attribute requests (one RPC each,
// thirteen attributes) trip Temporal's per-namespace rate limit when many
// namespaces are created under load, and a rate-limited GetClient retries on
// the next request, keeping the limiter saturated. Tenant registration
// already adds these attributes, so the steady state here is a single read.
//
// AddSearchAttributes rejects the whole batch with AlreadyExists if any one
// requested attribute was registered between the list and the add, which
// would silently skip the genuinely-missing rest, so on AlreadyExists the
// still-missing set is recomputed and retried.
func (f *DefaultClientFactory) addSearchAttributes(ctx context.Context, temporalClient temporalclient.Client, namespace string) error {
	operatorService := temporalClient.OperatorService()

	for attempt := 1; ; attempt++ {
		existing, err := operatorService.ListSearchAttributes(ctx, &operatorservice.ListSearchAttributesRequest{
			Namespace: namespace,
		})
		if err != nil {
			return fmt.Errorf("failed to list search attributes: %w", err)
		}

		missing := make(map[string]enums.IndexedValueType, len(SearchAttributes))
		for _, attr := range SearchAttributes {
			if _, ok := existing.GetCustomAttributes()[attr.GetName()]; !ok {
				missing[attr.GetName()] = attr.GetValueType()
			}
		}
		if len(missing) == 0 {
			return nil
		}

		_, err = operatorService.AddSearchAttributes(ctx, &operatorservice.AddSearchAttributesRequest{
			Namespace:        namespace,
			SearchAttributes: missing,
		})
		if err == nil {
			return nil
		}
		if status.Code(err) == codes.AlreadyExists && attempt < searchAttributeAttempts {
			if err := sleepWithJitter(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("failed to add search attributes %v: %w", slices.Sorted(maps.Keys(missing)), err)
	}
}

// sleepWithJitter waits an exponentially increasing, jittered delay before
// the next addSearchAttributes retry, or returns ctx.Err() if ctx is done
// first. attempt is 1-based, matching the caller's loop counter.
func sleepWithJitter(ctx context.Context, attempt int) error {
	return waitOrCancel(ctx, jitteredBackoff(attempt))
}

// jitteredBackoff is the delay before the given 1-based retry attempt:
// exponential from searchAttributeBackoffBase, capped at
// searchAttributeBackoffMax, then spread across the upper half of that
// window so concurrent registrants do not retry in lockstep.
func jitteredBackoff(attempt int) time.Duration {
	backoff := searchAttributeBackoffBase
	for i := 1; i < attempt && backoff < searchAttributeBackoffMax; i++ {
		backoff *= 2
	}
	if backoff > searchAttributeBackoffMax {
		backoff = searchAttributeBackoffMax
	}
	return backoff/2 + time.Duration(rand.Int63n(int64(backoff/2+1))) //nolint:gosec // non-crypto jitter
}

// waitOrCancel sleeps for delay, or returns ctx.Err() as soon as ctx is
// done. The unconditional ctx.Err() check is load-bearing rather than a
// shortcut: once the timer has fired both select cases are ready, and Go
// picks between ready cases uniformly at random, so without it a cancelled
// caller would be handed another attempt about half the time.
func waitOrCancel(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Close releases every derived client and then the root client, and marks
// the factory closed so a GetClient racing shutdown gets an error instead of
// deriving from a connection that is being torn down. The shared connection
// actually closes when its last holder does, so all of them must be closed
// here. root stays assigned so concurrent readers never observe a mutation;
// the closed flag gates further use. Close is idempotent.
func (f *DefaultClientFactory) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true

	rootClosed := false
	for _, client := range f.clients {
		if client == nil {
			continue
		}
		if client.temporal == f.root {
			rootClosed = true
		}
		client.Close()
	}
	f.clients = nil

	if f.root != nil && !rootClosed {
		f.root.Close()
	}
}
