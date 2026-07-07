package activities

import (
	"log/slog"

	"github.com/dapr/durabletask-go/workflow"
)

func CallDeployKubernetesResources(ctx *workflow.WorkflowContext, input DeployKubernetesResourcesInput) (PostgresDeployment, error) {
	task := ctx.CallActivity(DeployKubernetesResources, workflow.WithActivityInput(input))

	output := PostgresDeployment{}
	err := task.Await(&output)
	if err != nil {
		return PostgresDeployment{}, err
	}

	return output, nil
}

type DeployKubernetesResourcesInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// DeployKubernetesResources deploys a real PostgreSQL workload (Deployment +
// Service + Secret) into the target namespace and waits for it to be ready. It
// returns a PostgresDeployment: the in-cluster Service DNS the recipe advertises
// plus the host-reachable superuser endpoint the workflow uses to run DDL.
func DeployKubernetesResources(ctx workflow.ActivityContext) (any, error) {
	input := DeployKubernetesResourcesInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	logger := slog.Default()
	logger.Info("Deploying PostgreSQL workload",
		slog.String("namespace", input.Namespace), slog.String("name", input.Name))

	deployer, err := newKubeDeployer(ctx.Context())
	if err != nil {
		return nil, err
	}

	dep, err := deployer.DeployPostgres(ctx.Context(), input.Namespace, input.Name)
	if err != nil {
		return nil, err
	}

	logger.Info("PostgreSQL workload ready",
		slog.String("host", dep.InClusterHost), slog.Int("resources", len(dep.Resources)))
	return dep, nil
}

func CallDeleteKubernetesResources(ctx *workflow.WorkflowContext, input DeleteKubernetesResourcesInput) (DeleteKubernetesResourcesOutput, error) {
	task := ctx.CallActivity(DeleteKubernetesResources, workflow.WithActivityInput(input))

	output := DeleteKubernetesResourcesOutput{}
	err := task.Await(&output)
	if err != nil {
		return DeleteKubernetesResourcesOutput{}, err
	}

	return output, nil
}

type DeleteKubernetesResourcesInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type DeleteKubernetesResourcesOutput struct {
}

// DeleteKubernetesResources destroys the PostgreSQL workload (Deployment +
// Service + Secret) — and with it the database and roles it held.
func DeleteKubernetesResources(ctx workflow.ActivityContext) (any, error) {
	input := DeleteKubernetesResourcesInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	logger := slog.Default()
	logger.Info("Deleting PostgreSQL workload",
		slog.String("namespace", input.Namespace), slog.String("name", input.Name))

	deployer, err := newKubeDeployer(ctx.Context())
	if err != nil {
		return nil, err
	}

	if err := deployer.DeletePostgres(ctx.Context(), input.Namespace, input.Name); err != nil {
		return nil, err
	}

	return DeleteKubernetesResourcesOutput{}, nil
}
