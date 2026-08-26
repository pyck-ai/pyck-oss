package temporal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/protobuf/types/known/durationpb"
)

// DefaultDialTimeout is the fallback dial timeout for callers that have no
// env-derived configuration; services pass config.TemporalDialTimeout.
const DefaultDialTimeout = 30 * time.Second

// NewTemporalClient dials the Temporal frontend at url and eagerly verifies
// connectivity, bounded by dialTimeout (DefaultDialTimeout if non-positive)
// so an unreachable Temporal fails the caller instead of hanging it. The
// returned client owns a gRPC connection whose lifetime is not tied to ctx —
// it lives until Close — so pass the application root context, not a request
// context: services dial once at startup and hold the connection for the
// process lifetime.
//
//nolint:ireturn // client.DialContext only yields the client.Client interface.
func NewTemporalClient(ctx context.Context, url string, dialTimeout time.Duration) (client.Client, error) {
	if dialTimeout <= 0 {
		dialTimeout = DefaultDialTimeout
	}

	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		return nil, fmt.Errorf("create tracing interceptor: %w", err)
	}

	clientLogger := log.ForContext(ctx).With().
		Str("component", "temporal-client").
		Logger()

	dialCtx, cancel := context.WithTimeout(clientLogger.WithContext(ctx), dialTimeout)
	defer cancel()

	c, err := client.DialContext(dialCtx, client.Options{
		HostPort:     url,
		Logger:       logadapter.TemporalSDKLogAdapter(clientLogger),
		Interceptors: []interceptor.ClientInterceptor{tracingInterceptor},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to dial Temporal at %q: %w", url, err)
	}

	return c, nil
}

func NewTemporalNamespaceClient(ctx context.Context, url string) (client.NamespaceClient, error) {
	c, err := client.NewNamespaceClient(client.Options{
		HostPort: url,
		Logger:   logadapter.TemporalSDKLogAdapter(*log.ForContext(ctx)),
	})
	if err != nil {
		return nil, err
	}

	return c, nil
}

func CreateTemporalNamespace(ctx context.Context, nsClient client.NamespaceClient, namespace string) error {
	request := &workflowservice.RegisterNamespaceRequest{
		Namespace:                        namespace,
		WorkflowExecutionRetentionPeriod: &durationpb.Duration{Seconds: 86400},
	}
	err := nsClient.Register(ctx, request)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}

	return nil
}
