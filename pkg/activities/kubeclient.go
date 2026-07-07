package activities

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// PostgresDeployment describes a Postgres workload the recipe deployed into the
// cluster: its Radius-style resource ids, the in-cluster Service DNS consumers
// use, and a host-reachable admin endpoint (node InternalIP + NodePort) the
// workflow uses to run provisioning DDL from outside the cluster.
type PostgresDeployment struct {
	Resources     []string `json:"resources"`
	InClusterHost string   `json:"inClusterHost"` // <name>.<ns>.svc.cluster.local
	Port          string   `json:"port"`          // in-cluster port (5432)
	AdminUser     string   `json:"adminUser"`     // postgres superuser
	AdminPassword string   `json:"adminPassword"`
	ReachableHost string   `json:"reachableHost"` // node InternalIP (host-reachable on kind)
	ReachablePort string   `json:"reachablePort"` // NodePort
}

// kubeDeployer deploys/removes a Postgres workload (Deployment + Service +
// Secret). Small interface so activities are unit-testable against a fake.
type kubeDeployer interface {
	DeployPostgres(ctx context.Context, namespace, name string) (PostgresDeployment, error)
	DeletePostgres(ctx context.Context, namespace, name string) error
}

// newKubeDeployer constructs a kubeDeployer from ambient kubeconfig / in-cluster
// config. Package-level var so tests can substitute a fake.
var newKubeDeployer = func(_ context.Context) (kubeDeployer, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubernetes config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return &clientsetDeployer{client: clientset}, nil
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

const (
	managedByLabel = "dapr-go-workflow-k8s"
	instanceLabel  = "app.kubernetes.io/instance"
	postgresPort   = int32(5432)
)

type clientsetDeployer struct {
	client kubernetes.Interface
}

// resourceID renders a Radius-style resource id for a namespaced resource.
func resourceID(namespace, provider, kind, name string) string {
	return fmt.Sprintf("/planes/kubernetes/local/namespaces/%s/providers/%s/%s/%s", namespace, provider, kind, name)
}

func workloadLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": managedByLabel,
		instanceLabel:                  name,
		"app.kubernetes.io/name":       postgresName,
	}
}

// DeployPostgres creates (idempotently) the Postgres workload and, once the
// rollout is complete, returns the deployment info including a host-reachable
// admin endpoint. createWorkloadObjects is split out so it is unit-testable
// against a fake clientset; the rollout/endpoint discovery needs a real cluster
// (covered by make e2e).
func (d *clientsetDeployer) DeployPostgres(ctx context.Context, namespace, name string) (PostgresDeployment, error) {
	password, resources, err := d.createWorkloadObjects(ctx, namespace, name)
	if err != nil {
		return PostgresDeployment{}, err
	}
	host, port, err := d.waitAndDiscover(ctx, namespace, name)
	if err != nil {
		return PostgresDeployment{}, err
	}
	return PostgresDeployment{
		Resources:     resources,
		InClusterHost: fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace),
		Port:          "5432",
		AdminUser:     postgresName,
		AdminPassword: password,
		ReachableHost: host,
		ReachablePort: port,
	}, nil
}

// createWorkloadObjects ensures the Secret (superuser password), Deployment
// (postgres image), and NodePort Service exist. Idempotent: an existing Secret's
// password is reused (the running pod's env can't be changed after start), and
// existing Deployment/Service are left as-is. Returns the superuser password and
// the created resource ids.
func (d *clientsetDeployer) createWorkloadObjects(ctx context.Context, namespace, name string) (password string, resources []string, err error) {
	password, err = d.ensureSuperuserSecret(ctx, namespace, name)
	if err != nil {
		return "", nil, err
	}
	if err := d.ensureDeployment(ctx, namespace, name); err != nil {
		return "", nil, err
	}
	if err := d.ensureService(ctx, namespace, name); err != nil {
		return "", nil, err
	}
	return password, []string{
		resourceID(namespace, "apps", "Deployment", name),
		resourceID(namespace, "core", "Service", name),
		resourceID(namespace, "core", "Secret", name),
	}, nil
}

const superuserEnvName = "POSTGRES_PASSWORD"

func (d *clientsetDeployer) ensureSuperuserSecret(ctx context.Context, namespace, name string) (string, error) {
	secrets := d.client.CoreV1().Secrets(namespace)
	if existing, err := secrets.Get(ctx, name, metav1.GetOptions{}); err == nil {
		if pw := string(existing.Data[superuserEnvName]); pw != "" {
			return pw, nil // reuse — the running pod already uses this password
		}
		if pw := existing.StringData[superuserEnvName]; pw != "" {
			return pw, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("getting secret %q: %w", name, err)
	}

	password := uuid.NewString()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: workloadLabels(name)},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{superuserEnvName: password},
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating secret %q: %w", name, err)
	}
	return password, nil
}

func (d *clientsetDeployer) ensureDeployment(ctx context.Context, namespace, name string) error {
	replicas := int32(1)
	labels := workloadLabels(name)
	selector := map[string]string{instanceLabel: name}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  postgresName,
						Image: workloadImage(),
						Ports: []corev1.ContainerPort{{ContainerPort: postgresPort}},
						Env: []corev1.EnvVar{{
							Name: superuserEnvName,
							ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: name},
								Key:                  superuserEnvName,
							}},
						}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
								Command: []string{"pg_isready", "-U", postgresName},
							}},
							InitialDelaySeconds: 3,
							PeriodSeconds:       3,
						},
					}},
				},
			},
		},
	}
	_, err := d.client.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating deployment %q: %w", name, err)
	}
	return nil
}

func (d *clientsetDeployer) ensureService(ctx context.Context, namespace, name string) error {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: workloadLabels(name)},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{instanceLabel: name},
			Ports: []corev1.ServicePort{{
				Name:       postgresName,
				Port:       postgresPort,
				TargetPort: intstr.FromInt32(postgresPort),
			}},
		},
	}
	_, err := d.client.CoreV1().Services(namespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating service %q: %w", name, err)
	}
	return nil
}

// waitAndDiscover blocks until the Deployment has a ready replica, then returns
// a host-reachable endpoint (a node InternalIP + the Service NodePort). On kind
// the node InternalIP is a docker-network address routable from the host.
func (d *clientsetDeployer) waitAndDiscover(ctx context.Context, namespace, name string) (host, port string, err error) {
	deadline := time.Now().Add(workloadRolloutTimeout())
	for {
		dep, err := d.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", fmt.Errorf("getting deployment %q: %w", name, err)
		}
		if dep.Status.ReadyReplicas >= 1 {
			break
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("deployment %q not ready within %s", name, workloadRolloutTimeout())
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	svc, err := d.client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("getting service %q: %w", name, err)
	}
	if len(svc.Spec.Ports) == 0 || svc.Spec.Ports[0].NodePort == 0 {
		return "", "", fmt.Errorf("service %q has no NodePort assigned", name)
	}
	nodePort := fmt.Sprintf("%d", svc.Spec.Ports[0].NodePort)

	nodes, err := d.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("listing nodes: %w", err)
	}
	for i := range nodes.Items {
		for _, addr := range nodes.Items[i].Status.Addresses {
			if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
				return addr.Address, nodePort, nil
			}
		}
	}
	return "", "", fmt.Errorf("no node InternalIP found for reachable endpoint")
}

// DeletePostgres removes the Deployment, Service, and Secret (destroying the
// instance and everything in it), ignoring NotFound.
func (d *clientsetDeployer) DeletePostgres(ctx context.Context, namespace, name string) error {
	depErr := d.client.AppsV1().Deployments(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if depErr != nil && !apierrors.IsNotFound(depErr) {
		return fmt.Errorf("deleting deployment %q: %w", name, depErr)
	}
	svcErr := d.client.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if svcErr != nil && !apierrors.IsNotFound(svcErr) {
		return fmt.Errorf("deleting service %q: %w", name, svcErr)
	}
	secErr := d.client.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if secErr != nil && !apierrors.IsNotFound(secErr) {
		return fmt.Errorf("deleting secret %q: %w", name, secErr)
	}
	return nil
}
