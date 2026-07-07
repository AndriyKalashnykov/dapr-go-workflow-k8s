package activities

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// BindingConnection is the connection information published to the cluster so
// in-cluster consumers can reach the provisioned database.
type BindingConnection struct {
	Host     string
	Port     string
	Username string
	Password string
	Database string
	URI      string
}

// kubeBinder publishes/removes the Kubernetes objects that represent a
// provisioned database binding (a Service + a Secret). Small interface so
// activities are unit-testable against a fake with no cluster.
type kubeBinder interface {
	EnsureBinding(ctx context.Context, namespace, name string, conn BindingConnection) ([]string, error)
	DeleteBinding(ctx context.Context, namespace, name string) error
}

// newKubeBinder constructs a kubeBinder from ambient kubeconfig / in-cluster
// config. Package-level var so tests can substitute a fake.
var newKubeBinder = func(ctx context.Context) (kubeBinder, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubernetes config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return &clientsetBinder{client: clientset}, nil
}

// restConfig resolves a client config: kubeconfig (KUBECONFIG / ~/.kube/config)
// first, falling back to in-cluster config when running inside a pod.
func restConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err == nil {
		return cfg, nil
	}
	inCluster, inErr := rest.InClusterConfig()
	if inErr != nil {
		return nil, fmt.Errorf("no kubeconfig and not in-cluster: %w", errors.Join(err, inErr))
	}
	return inCluster, nil
}

const managedByLabel = "dapr-go-workflow-k8s"

type clientsetBinder struct {
	client kubernetes.Interface
}

// resourceID renders a Radius-style resource id for a namespaced core resource.
func resourceID(namespace, kind, name string) string {
	return fmt.Sprintf("/planes/kubernetes/local/namespaces/%s/providers/core/%s/%s", namespace, kind, name)
}

func bindingLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": managedByLabel,
		"app.kubernetes.io/instance":   name,
	}
}

// EnsureBinding creates (or updates) a Secret holding the connection info and a
// Service that represents the database endpoint. Idempotent so workflow activity
// retries are safe.
func (b *clientsetBinder) EnsureBinding(ctx context.Context, namespace, name string, conn BindingConnection) ([]string, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: bindingLabels(name)},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"host":     conn.Host,
			"port":     conn.Port,
			"username": conn.Username,
			"password": conn.Password,
			"database": conn.Database,
			"uri":      conn.URI,
		},
	}
	if err := b.applySecret(ctx, namespace, secret); err != nil {
		return nil, err
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: bindingLabels(name)},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Name:       postgresName,
				Port:       5432,
				TargetPort: intstr.FromInt32(5432),
			}},
		},
	}
	if err := b.applyService(ctx, namespace, service); err != nil {
		return nil, err
	}

	return []string{
		resourceID(namespace, "Service", name),
		resourceID(namespace, "Secret", name),
	}, nil
}

func (b *clientsetBinder) applySecret(ctx context.Context, namespace string, secret *corev1.Secret) error {
	secrets := b.client.CoreV1().Secrets(namespace)
	_, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := secrets.Get(ctx, secret.Name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("getting secret %q: %w", secret.Name, getErr)
		}
		existing.StringData = secret.StringData
		existing.Labels = secret.Labels
		if _, err = secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating secret %q: %w", secret.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating secret %q: %w", secret.Name, err)
	}
	return nil
}

func (b *clientsetBinder) applyService(ctx context.Context, namespace string, service *corev1.Service) error {
	services := b.client.CoreV1().Services(namespace)
	_, err := services.Create(ctx, service, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// The binding Service already exists and is left as-is: its ClusterIP is
		// immutable and nothing else about it changes across re-publishes. (The
		// connection details live in the Secret, which applySecret does refresh.)
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating service %q: %w", service.Name, err)
	}
	return nil
}

// DeleteBinding removes the Service and Secret, ignoring NotFound.
func (b *clientsetBinder) DeleteBinding(ctx context.Context, namespace, name string) error {
	svcErr := b.client.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if svcErr != nil && !apierrors.IsNotFound(svcErr) {
		return fmt.Errorf("deleting service %q: %w", name, svcErr)
	}
	secErr := b.client.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if secErr != nil && !apierrors.IsNotFound(secErr) {
		return fmt.Errorf("deleting secret %q: %w", name, secErr)
	}
	return nil
}
