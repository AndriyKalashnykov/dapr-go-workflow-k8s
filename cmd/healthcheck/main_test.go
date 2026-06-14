package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func splitHostPort(t *testing.T, srv *httptest.Server) (host, port string) {
	t.Helper()
	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	return host, port
}

func TestRunHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv)
	t.Setenv("HEALTHCHECK_HOST", host)
	t.Setenv("APP_PORT", port)

	if err := run(); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
}

func TestRunUnhealthyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv)
	t.Setenv("HEALTHCHECK_HOST", host)
	t.Setenv("APP_PORT", port)

	if err := run(); err == nil {
		t.Fatal("run() = nil, want error for non-200 status")
	}
}

func TestRunConnectionRefused(t *testing.T) {
	// Bind a port, close it → guaranteed nothing is listening there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()

	t.Setenv("HEALTHCHECK_HOST", "127.0.0.1")
	t.Setenv("APP_PORT", port)

	if err := run(); err == nil {
		t.Fatal("run() = nil, want error when nothing is listening")
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("FOO", "bar")
	if got := envOr("FOO", "fallback"); got != "bar" {
		t.Errorf("envOr set = %q, want bar", got)
	}
	t.Setenv("FOO", "")
	if got := envOr("FOO", "fallback"); got != "fallback" {
		t.Errorf("envOr empty = %q, want fallback", got)
	}
}
