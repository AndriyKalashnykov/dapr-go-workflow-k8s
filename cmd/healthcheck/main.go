// Command healthcheck is a tiny dependency-free probe used as the container
// HEALTHCHECK CMD. The runtime image is distroless/static (no shell, no curl),
// so the probe ships as a compiled binary. It performs a GET against the
// application's /healthz endpoint and exits non-zero on any failure.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	host := envOr("HEALTHCHECK_HOST", "localhost")
	port := envOr("APP_PORT", "7999")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+":"+port+"/healthz", http.NoBody)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
