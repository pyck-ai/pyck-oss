package temporal

import (
	"context"
	"fmt"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/protobuf/types/known/durationpb"
)

func NewTemporalClient(ctx context.Context, url string) (client.Client, error) {
	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		return nil, fmt.Errorf("create tracing interceptor: %w", err)
	}

	c, err := client.Dial(client.Options{
		HostPort:     url,
		Logger:       logadapter.TemporalSDKLogAdapter(*log.ForContext(ctx)),
		Interceptors: []interceptor.ClientInterceptor{tracingInterceptor},
	})
	if err != nil {
		return nil, err
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
