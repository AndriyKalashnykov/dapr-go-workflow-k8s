package activities

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/dapr/durabletask-go/workflow"
)

func CallDeployKubernetesResources(ctx *workflow.WorkflowContext, input DeployKubernetesResourcesInput) (DeployKubernetesResourcesOutput, error) {
	task := ctx.CallActivity(DeployKubernetesResources, workflow.WithActivityInput(input))

	output := DeployKubernetesResourcesOutput{}
	err := task.Await(&output)
	if err != nil {
		return DeployKubernetesResourcesOutput{}, err
	}

	return output, nil
}

type DeployKubernetesResourcesInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type DeployKubernetesResourcesOutput struct {
	Resources []string `json:"resources"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
}

func DeployKubernetesResources(ctx workflow.ActivityContext) (any, error) {
	input := DeployKubernetesResourcesInput{}
	err := ctx.GetInput(&input)
	if err != nil {
		return nil, err
	}

	// Pretend we are deploying resources...
	logger := slog.Default()
	logger.Info("Deploying Kubernetes Deployment")
	logger.Info("Deploying Kubernetes Service")
	logger.Info("Waiting for pods to be ready")
	time.Sleep(2 * time.Second)
	logger.Info("Pods are ready")

	return DeployKubernetesResourcesOutput{
		Host: fmt.Sprintf("%s.%s.svc.cluster.local", input.Namespace, input.Name),
		Port: 5432,
		Resources: []string{
			"/planes/kubernetes/local/namespaces/" + input.Namespace + "/providers/core/Service/" + input.Name,
			"/planes/kubernetes/local/namespaces/" + input.Namespace + "/providers/apps/Deployment/" + input.Name,
		},
	}, nil
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

func DeleteKubernetesResources(ctx workflow.ActivityContext) (any, error) {
	input := DeleteKubernetesResourcesInput{}
	err := ctx.GetInput(&input)
	if err != nil {
		return nil, err
	}

	// Pretend we are deleting resources...
	logger := slog.Default()
	logger.Info("Deleting Kubernetes Deployment")
	logger.Info("Deleting Kubernetes Service")

	return DeleteKubernetesResourcesOutput{}, nil
}
