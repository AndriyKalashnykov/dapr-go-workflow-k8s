package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestStartLifecycle exercises the real Start path: it binds a listener,
// serves /healthz, and registers a working shutdown closure. wfClient is nil
// because only the /healthz route (which never touches it) is exercised.
func TestStartLifecycle(t *testing.T) {
	// Reserve a free port, then release it for Start to bind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	t.Setenv("APP_PORT", port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	services := map[string]context.CancelFunc{}
	if err := Start(ctx, services, nil); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	shutdown, ok := services["http"]
	if !ok {
		t.Fatal("Start did not register an http shutdown closure")
	}
	t.Cleanup(shutdown)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStartListenError(t *testing.T) {
	// Occupy a port so Start's bind fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv("APP_PORT", port)

	if err := Start(context.Background(), map[string]context.CancelFunc{}, nil); err == nil {
		t.Fatal("Start() = nil, want error when the port is already bound")
	}
}
