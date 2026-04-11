package workflow

import (
	"context"
	"fmt"
	"sync"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	temporalsvc "github.com/pyck-ai/pyck/backend/common/services/temporal"
)

type ClientFactory interface {
	GetClient(ctx context.Context, namespace string) (*Client, error)
	Close()
}

type ClientCache struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func NewClientCache() *ClientCache {
	return &ClientCache{
		clients: make(map[string]*Client),
	}
}

func (c *ClientCache) Get(namespace string) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clients[namespace]
}

func (c *ClientCache) Set(namespace string, client *Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clients[namespace] = client
}

func (c *ClientCache) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.clients)
}

func (c *ClientCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, client := range c.clients {
		if client != nil {
			client.Close()
		}
	}

	c.clients = make(map[string]*Client)
}

type DefaultClientFactory struct {
	temporalURL string
	clientCache *ClientCache
}

func NewDefaultClientFactory(temporalURL string, cache *ClientCache) *DefaultClientFactory {
	if cache == nil {
		cache = NewClientCache()
	}
	return &DefaultClientFactory{
		temporalURL: temporalURL,
		clientCache: cache,
	}
}
func (f *DefaultClientFactory) GetClient(ctx context.Context, namespace string) (*Client, error) {
	if namespace == "" {
		namespace = temporalclient.DefaultNamespace
	}

	if client := f.clientCache.Get(namespace); client != nil {
		return client, nil
	}

	createCtx, cancel := context.WithTimeout(ctx, ClientCreationTimeout)
	defer cancel()

	wfClient, err := f.createClient(createCtx, namespace)
	if err != nil {
		return nil, err
	}

	f.clientCache.Set(namespace, wfClient)

	return wfClient, nil
}

func (f *DefaultClientFactory) createClient(ctx context.Context, namespace string) (*Client, error) {
	// Create namespace if it doesn't exist
	if namespace != temporalclient.DefaultNamespace {
		if err := f.ensureNamespaceExists(ctx, namespace); err != nil {
			return nil, fmt.Errorf("failed to ensure namespace exists: %w", err)
		}
	}

	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		return nil, fmt.Errorf("create tracing interceptor: %w", err)
	}

	clientLogger := log.DefaultLogger().With().
		Str("component", "temporal-client").
		Str("namespace", namespace).
		Logger()

	// We need to create a new detached context, because this is a long-lived
	// client which will be reused by multiple requests.
	clientCtx := clientLogger.WithContext(context.Background())

	temporalClient, err := temporalclient.DialContext(clientCtx, temporalclient.Options{ //nolint:contextcheck
		Namespace:    namespace,
		HostPort:     f.temporalURL,
		Logger:       logadapter.TemporalSDKLogAdapter(clientLogger),
		Interceptors: []interceptor.ClientInterceptor{tracingInterceptor},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Temporal client for namespace %q: %w", namespace, err)
	}

	if namespace != temporalclient.DefaultNamespace {
		if err := f.addSearchAttributes(ctx, temporalClient, namespace); err != nil {
			temporalClient.Close()
			return nil, err
		}
	}

	wfClient, err := NewClient(namespace, temporalClient)
	if err != nil {
		temporalClient.Close()
		return nil, fmt.Errorf("failed to create workflow client: %w", err)
	}

	return wfClient, nil
}

func (f *DefaultClientFactory) ensureNamespaceExists(ctx context.Context, namespace string) error {
	namespaceClient, err := temporalsvc.NewTemporalNamespaceClient(ctx, f.temporalURL)
	if err != nil {
		return fmt.Errorf("failed to create namespace client: %w", err)
	}
	defer namespaceClient.Close()

	if err := temporalsvc.CreateTemporalNamespace(ctx, namespaceClient, namespace); err != nil {
		return fmt.Errorf("failed to create namespace %q: %w", namespace, err)
	}

	return nil
}

func (f *DefaultClientFactory) addSearchAttributes(ctx context.Context, temporalClient temporalclient.Client, namespace string) error {
	searchAttributes := make(map[string]enums.IndexedValueType, len(SearchAttributes))
	for _, attr := range SearchAttributes {
		searchAttributes[attr.GetName()] = attr.GetValueType()
	}

	operatorService := temporalClient.OperatorService()

	if _, err := operatorService.AddSearchAttributes(ctx, &operatorservice.AddSearchAttributesRequest{
		Namespace:        namespace,
		SearchAttributes: searchAttributes,
	}); err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return nil // Ignore already exists error
		}

		return fmt.Errorf("failed to add search attributes, error: %w", err)
	}

	return nil
}

func (f *DefaultClientFactory) Close() {
	if f.clientCache != nil {
		f.clientCache.Close()
	}
}
