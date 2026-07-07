package activities

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// These tests exercise the REAL clientsetDeployer against client-go's in-memory
// fake clientset — hermetic (no cluster), covering object creation, idempotency,
// deletion, and endpoint discovery.

func TestCreateWorkloadObjects(t *testing.T) {
	cs := fake.NewSimpleClientset()
	d := &clientsetDeployer{client: cs}
	ctx := context.Background()

	pw, ids, err := d.createWorkloadObjects(ctx, "default", "sample")
	if err != nil {
		t.Fatalf("createWorkloadObjects: %v", err)
	}
	if pw == "" {
		t.Error("expected a generated superuser password")
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 resource ids, got %v", ids)
	}

	sec, err := cs.CoreV1().Secrets("default").Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret not created: %v", err)
	}
	if sec.StringData[superuserEnvName] != pw {
		t.Errorf("secret password mismatch: %+v", sec.StringData)
	}

	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deployment not created: %v", err)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if c.Image != workloadImage() {
		t.Errorf("deployment image = %q, want %q", c.Image, workloadImage())
	}
	if c.Env[0].ValueFrom.SecretKeyRef.Name != "sample" || c.Env[0].Name != superuserEnvName {
		t.Errorf("POSTGRES_PASSWORD not wired from the secret: %+v", c.Env)
	}

	svc, err := cs.CoreV1().Services("default").Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service not created: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeNodePort || svc.Spec.Ports[0].Port != postgresPort {
		t.Errorf("service not a NodePort on 5432: %+v", svc.Spec)
	}
}

func TestCreateWorkloadObjectsIsIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	d := &clientsetDeployer{client: cs}
	ctx := context.Background()

	pw1, _, err := d.createWorkloadObjects(ctx, "default", "sample")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	pw2, _, err := d.createWorkloadObjects(ctx, "default", "sample")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// The running pod's password can't change after start, so a re-deploy must
	// reuse the existing secret's password.
	if pw1 != pw2 {
		t.Errorf("re-deploy rotated the superuser password (%q -> %q); must reuse", pw1, pw2)
	}
	secs, _ := cs.CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	deps, _ := cs.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{})
	if len(secs.Items) != 1 || len(deps.Items) != 1 {
		t.Errorf("expected exactly 1 secret and 1 deployment, got %d/%d", len(secs.Items), len(deps.Items))
	}
}

func TestWaitAndDiscover(t *testing.T) {
	ready := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeNodePort,
			Ports: []corev1.ServicePort{{Port: postgresPort, NodePort: 31000}},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "kind-control-plane"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "kind-control-plane"},
			{Type: corev1.NodeInternalIP, Address: "172.18.0.9"},
		}},
	}
	cs := fake.NewSimpleClientset(ready, svc, node)
	d := &clientsetDeployer{client: cs}

	host, port, err := d.waitAndDiscover(context.Background(), "default", "sample")
	if err != nil {
		t.Fatalf("waitAndDiscover: %v", err)
	}
	if host != "172.18.0.9" || port != "31000" {
		t.Errorf("discovered endpoint = %s:%s, want 172.18.0.9:31000", host, port)
	}
}

func TestDeletePostgres(t *testing.T) {
	cs := fake.NewSimpleClientset()
	d := &clientsetDeployer{client: cs}
	ctx := context.Background()

	if _, _, err := d.createWorkloadObjects(ctx, "default", "sample"); err != nil {
		t.Fatalf("createWorkloadObjects: %v", err)
	}
	if err := d.DeletePostgres(ctx, "default", "sample"); err != nil {
		t.Fatalf("DeletePostgres: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Get(ctx, "sample", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("deployment should be gone, got %v", err)
	}
	if _, err := cs.CoreV1().Services("default").Get(ctx, "sample", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("service should be gone, got %v", err)
	}
	if _, err := cs.CoreV1().Secrets("default").Get(ctx, "sample", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("secret should be gone, got %v", err)
	}
	// Idempotent: a second delete is a no-op.
	if err := d.DeletePostgres(ctx, "default", "sample"); err != nil {
		t.Errorf("second DeletePostgres should be a no-op, got %v", err)
	}
}

func TestWorkloadLabels(t *testing.T) {
	l := workloadLabels("sample")
	if l["app.kubernetes.io/managed-by"] != managedByLabel ||
		l["app.kubernetes.io/instance"] != "sample" ||
		l["app.kubernetes.io/name"] != postgresName {
		t.Errorf("unexpected labels: %+v", l)
	}
}
