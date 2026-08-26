// Package temporal dials pyck's local Temporal server the way the
// SDK-facing services do: a bearer token presented as a Temporal API key
// (tenant PAT or the system service token — pyck-temporal's claim mapper
// scopes namespace access from it) over plain TCP.
package temporal

import (
	"context"
	"fmt"

	sdkclient "go.temporal.io/sdk/client"
)

// Dial opens a workflow client on the given namespace. API-key credentials
// auto-enable TLS in the SDK (a Temporal Cloud assumption); the local stack
// is plain TCP, so TLS is explicitly disabled here — this is the one place
// that gotcha lives.
func Dial(ctx context.Context, address, namespace, apiKey string) (sdkclient.Client, error) {
	c, err := sdkclient.DialContext(ctx, options(address, namespace, apiKey))
	if err != nil {
		return nil, fmt.Errorf("dial temporal namespace %q: %w", namespace, err)
	}
	return c, nil
}

// NewNamespaceClient opens a namespace-management client (Describe/List of
// namespaces rather than workflows). Same API-key/TLS handling as Dial.
func NewNamespaceClient(address, apiKey string) (sdkclient.NamespaceClient, error) {
	nc, err := sdkclient.NewNamespaceClient(options(address, "", apiKey))
	if err != nil {
		return nil, fmt.Errorf("dial temporal namespace client: %w", err)
	}
	return nc, nil
}

func options(address, namespace, apiKey string) sdkclient.Options {
	return sdkclient.Options{
		HostPort:          address,
		Namespace:         namespace,
		Credentials:       sdkclient.NewAPIKeyStaticCredentials(apiKey),
		ConnectionOptions: sdkclient.ConnectionOptions{TLSDisabled: true},
	}
}
