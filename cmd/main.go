package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/dapr/durabletask-go/workflow"
	daprclient "github.com/dapr/go-sdk/client"

	"github.com/AndriyKalashnykov/dapr-go-workflow-k8s/pkg/activities"
	"github.com/AndriyKalashnykov/dapr-go-workflow-k8s/pkg/server"
	"github.com/AndriyKalashnykov/dapr-go-workflow-k8s/pkg/workflows"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Error starting services", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	services := map[string]context.CancelFunc{}
	ctx, cancel := registerShutdown(ctx, services)
	defer cancel()

	if err := start(ctx, services); err != nil {
		return err
	}

	slog.InfoContext(ctx, "Server started: Press CTRL+C to stop")
	<-ctx.Done()
	return nil
}

func registerShutdown(ctx context.Context, services map[string]context.CancelFunc) (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	return ctx, func() {
		cancel()
		for name, cancel := range services {
			slog.InfoContext(ctx, "Shutting down", slog.String("service", name))
			cancel()
		}
	}
}

func start(ctx context.Context, services map[string]context.CancelFunc) error {
	slog.InfoContext(ctx, "Connecting to Dapr")

	dapr, err := daprclient.NewClient()
	if err != nil {
		return fmt.Errorf("error creating Dapr client: %w", err)
	}
	services["daprclient"] = dapr.Close

	// The Dapr workflow authoring/management API lives in durabletask-go and
	// talks to the Dapr sidecar over the client's shared gRPC connection.
	wfClient := workflow.NewClient(dapr.GrpcClientConn())

	if err := registerWorkflows(ctx, wfClient); err != nil {
		return fmt.Errorf("error initializing workflows: %w", err)
	}

	if err := server.Start(ctx, services, wfClient); err != nil {
		return fmt.Errorf("error starting HTTP server: %w", err)
	}

	return nil
}

func registerWorkflows(ctx context.Context, wfClient *workflow.Client) error {
	r := workflow.NewRegistry()

	if err := r.AddWorkflow(workflows.PostgresSQLDatabasesPut); err != nil {
		return fmt.Errorf("error registering workflow PostgresSQLDatabasesPut: %w", err)
	}
	if err := r.AddWorkflow(workflows.PostgresSQLDatabasesDelete); err != nil {
		return fmt.Errorf("error registering workflow PostgresSQLDatabasesDelete: %w", err)
	}

	if err := r.AddActivity(activities.DeployKubernetesResources); err != nil {
		return fmt.Errorf("error registering activity DeployKubernetesResources: %w", err)
	}
	if err := r.AddActivity(activities.DeleteKubernetesResources); err != nil {
		return fmt.Errorf("error registering activity DeleteKubernetesResources: %w", err)
	}
	if err := r.AddActivity(activities.CreatePostgresUser); err != nil {
		return fmt.Errorf("error registering activity CreatePostgresUser: %w", err)
	}
	if err := r.AddActivity(activities.DeletePostgresUser); err != nil {
		return fmt.Errorf("error registering activity DeletePostgresUser: %w", err)
	}
	if err := r.AddActivity(activities.CreatePostgresDatabase); err != nil {
		return fmt.Errorf("error registering activity CreatePostgresDatabase: %w", err)
	}
	if err := r.AddActivity(activities.DeletePostgresDatabase); err != nil {
		return fmt.Errorf("error registering activity DeletePostgresDatabase: %w", err)
	}

	if err := wfClient.StartWorker(ctx, r); err != nil {
		return fmt.Errorf("error starting Dapr workflow worker: %w", err)
	}

	slog.InfoContext(ctx, "Dapr workflow worker started")
	return nil
}
