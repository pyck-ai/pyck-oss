package kubernetes

import (
	"context"
	"path/filepath"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func NewK8sClient(namespace string, inCluster bool, kubeConfigPath string) (*K8sClient, error) {
	var config *rest.Config
	var err error
	if !inCluster {
		// command runs out-of-cluster
		if kubeConfigPath == "" && homedir.HomeDir() != "" {
			kubeConfigPath = filepath.Join(homedir.HomeDir(), ".kube", "config")
		}

		// use the current context in kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
		if err != nil {
			return nil, err
		}

	} else {
		// command runs in-cluster
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &K8sClient{
		clientset: clientset,
		dynClient: dynClient,
		namespace: namespace,
	}, nil
}

type K8sClient struct {
	clientset *kubernetes.Clientset
	dynClient *dynamic.DynamicClient
	namespace string
}

// Clientset returns the typed Kubernetes clientset.
func (client *K8sClient) Clientset() *kubernetes.Clientset {
	return client.clientset
}

// DynamicClient returns the dynamic Kubernetes client for CRD operations.
func (client *K8sClient) DynamicClient() *dynamic.DynamicClient {
	return client.dynClient
}

// CreateSecrets writes a new kubernetes secret
func (client *K8sClient) CreateSecrets(ctx context.Context, name string, data map[string][]byte) error {
	object := metav1.ObjectMeta{Name: name}
	secret := &v1.Secret{Data: data, ObjectMeta: object}
	options := metav1.CreateOptions{}
	_, err := client.clientset.CoreV1().Secrets(client.namespace).Create(ctx, secret, options)
	return err
}

// UpdateSecrets updates an existing kubernetes secret
func (client *K8sClient) UpdateSecrets(ctx context.Context, name string, data map[string][]byte) error {
	existingSecret, err := client.clientset.CoreV1().Secrets(client.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	mergedData := make(map[string][]byte)
	for k, v := range existingSecret.Data {
		mergedData[k] = v
	}

	for k, v := range data {
		mergedData[k] = v
	}

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: client.namespace,
		},
		Data: mergedData,
	}

	options := metav1.UpdateOptions{}
	_, err = client.clientset.CoreV1().Secrets(client.namespace).Update(ctx, secret, options)
	return err
}

func (client *K8sClient) UpsertSecrets(ctx context.Context, name string, data map[string][]byte) error {
	err := client.CreateSecrets(ctx, name, data)

	if err == nil || !errors.IsAlreadyExists(err) {
		return err
	}

	return client.UpdateSecrets(ctx, name, data)
}
