package exporters

import (
	"context"
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/services/kubernetes"
)

// K8sSecretClient defines the interface for Kubernetes secret operations
type K8sSecretClient interface {
	SecretExists(ctx context.Context, name string, key string) (bool, error)
	UpsertSecrets(ctx context.Context, name string, data map[string][]byte) error
}

// K8sExporter exports credentials to a Kubernetes secret
type K8sExporter struct {
	client     K8sSecretClient
	namespace  string
	secretName string
}

// NewK8sExporter creates a new K8sExporter with the specified Kubernetes configuration.
// K8s client creation is optional: if it fails, the error is logged and export
// will fail gracefully when actually attempted.
func NewK8sExporter(ctx context.Context, namespace string, secretName string, inCluster bool, kubeConfigPath string) *K8sExporter {
	exporter := &K8sExporter{
		namespace:  namespace,
		secretName: secretName,
	}

	// Only create client if namespace is specified
	if namespace != "" {
		client, err := kubernetes.NewK8sClient(namespace, inCluster, kubeConfigPath)
		if err != nil {
			log.ForContext(ctx).Warn().Err(err).Str("namespace", namespace).Msg("Failed to create K8s client; K8s export will be unavailable")
			return exporter
		}
		exporter.client = client
	}

	return exporter
}

// NewK8sExporterWithClient creates a new K8sExporter with an injected client (useful for testing)
func NewK8sExporterWithClient(namespace string, secretName string, client K8sSecretClient) *K8sExporter {
	return &K8sExporter{
		client:     client,
		namespace:  namespace,
		secretName: secretName,
	}
}

// Exists checks if the Kubernetes secret exists and contains data.
func (e *K8sExporter) Exists(ctx context.Context, export Export) (bool, error) {
	if e.namespace == "" || e.client == nil {
		return false, nil
	}
	return e.client.SecretExists(ctx, e.secretName, export.Name)
}

// Export creates or updates a Kubernetes secret with the provided credentials
// The export.Target is treated as the key name within a "pyck-secrets" secret
func (e *K8sExporter) Export(ctx context.Context, credentials string, export Export) error {
	logger := log.ForContext(ctx)

	// Check if K8s is properly configured
	if e.namespace == "" {
		logger.Warn().Str("key", export.File).Msg("Kubernetes namespace not configured, skipping K8s secret export")
		return nil
	}

	if e.client == nil {
		return fmt.Errorf("Kubernetes client not initialized - check your K8s configuration")
	}

	// Export.Target is the key name within the secret
	secretData := map[string][]byte{
		export.Name: []byte(credentials),
	}

	logger.Debug().Str("secret", e.secretName).Str("key", export.Name).Msg("Saving credentials to K8s secret")

	// Use UpsertSecrets to create or update the secret
	if err := e.client.UpsertSecrets(ctx, e.secretName, secretData); err != nil {
		return fmt.Errorf("failed to save credentials to K8s secret %q: %w", e.secretName, err)
	}

	logger.Debug().Str("secret", e.secretName).Msg("Successfully saved credentials to K8s secret")
	return nil
}
