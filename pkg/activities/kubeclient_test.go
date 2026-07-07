package activities

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// These tests exercise the REAL clientsetBinder against client-go's in-memory
// fake clientset — hermetic (no cluster), yet covering the actual create/
// update/delete/idempotency logic.

func TestClientsetBinderEnsureBinding(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := &clientsetBinder{client: cs}
	ctx := context.Background()

	conn := BindingConnection{
		Host: "localhost", Port: "5432", Username: "u", Password: "p",
		Database: "db_1", URI: "postgresql://u:p@localhost:5432/db_1",
	}
	ids, err := b.EnsureBinding(ctx, "default", "sample", conn)
	if err != nil {
		t.Fatalf("EnsureBinding: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 resource ids, got %v", ids)
	}

	sec, err := cs.CoreV1().Secrets("default").Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret not created: %v", err)
	}
	if sec.StringData["uri"] != conn.URI || sec.StringData["database"] != "db_1" {
		t.Errorf("secret data mismatch: %+v", sec.StringData)
	}
	if sec.Labels["app.kubernetes.io/managed-by"] != managedByLabel {
		t.Errorf("missing managed-by label: %+v", sec.Labels)
	}

	svc, err := cs.CoreV1().Services("default").Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service not created: %v", err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 5432 {
		t.Errorf("unexpected service ports: %+v", svc.Spec.Ports)
	}
}

func TestClientsetBinderEnsureBindingIsIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := &clientsetBinder{client: cs}
	ctx := context.Background()

	conn := BindingConnection{Host: "h", Port: "5432", Username: "u", Password: "p", Database: "db", URI: "u1"}
	if _, err := b.EnsureBinding(ctx, "default", "sample", conn); err != nil {
		t.Fatalf("first EnsureBinding: %v", err)
	}

	// Second call with rotated credentials must succeed (update path) and refresh
	// the secret without duplicating objects.
	conn.URI = "u2"
	conn.Password = "p2"
	if _, err := b.EnsureBinding(ctx, "default", "sample", conn); err != nil {
		t.Fatalf("second EnsureBinding: %v", err)
	}

	sec, err := cs.CoreV1().Secrets("default").Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret get: %v", err)
	}
	if sec.StringData["uri"] != "u2" || sec.StringData["password"] != "p2" {
		t.Errorf("secret not refreshed on second ensure: %+v", sec.StringData)
	}

	secList, _ := cs.CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	if len(secList.Items) != 1 {
		t.Errorf("expected exactly 1 secret, got %d", len(secList.Items))
	}
}

func TestClientsetBinderDeleteBinding(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := &clientsetBinder{client: cs}
	ctx := context.Background()

	conn := BindingConnection{Host: "h", Port: "5432", Username: "u", Password: "p", Database: "db", URI: "u"}
	if _, err := b.EnsureBinding(ctx, "default", "sample", conn); err != nil {
		t.Fatalf("EnsureBinding: %v", err)
	}

	if err := b.DeleteBinding(ctx, "default", "sample"); err != nil {
		t.Fatalf("DeleteBinding: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("default").Get(ctx, "sample", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("secret should be gone, got err=%v", err)
	}
	if _, err := cs.CoreV1().Services("default").Get(ctx, "sample", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("service should be gone, got err=%v", err)
	}

	// DeleteBinding again is idempotent (NotFound ignored).
	if err := b.DeleteBinding(ctx, "default", "sample"); err != nil {
		t.Errorf("second DeleteBinding should be a no-op, got %v", err)
	}
}

func TestBindingLabels(t *testing.T) {
	l := bindingLabels("sample")
	if l["app.kubernetes.io/managed-by"] != managedByLabel || l["app.kubernetes.io/instance"] != "sample" {
		t.Errorf("unexpected labels: %+v", l)
	}
}
