package activities

import (
	"log/slog"

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
	// Connection info published to the cluster as a Secret so in-cluster
	// consumers can reach the provisioned database.
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	URI      string `json:"uri"`
}

type DeployKubernetesResourcesOutput struct {
	Resources []string `json:"resources"`
}

// DeployKubernetesResources publishes the database binding (a Service + a Secret)
// into the target namespace via the Kubernetes API and returns the created
// resource ids.
func DeployKubernetesResources(ctx workflow.ActivityContext) (any, error) {
	input := DeployKubernetesResourcesInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	logger := slog.Default()
	logger.Info("Publishing Kubernetes binding",
		slog.String("namespace", input.Namespace), slog.String("name", input.Name))

	binder, err := newKubeBinder(ctx.Context())
	if err != nil {
		return nil, err
	}

	resources, err := binder.EnsureBinding(ctx.Context(), input.Namespace, input.Name, BindingConnection{
		Host:     input.Host,
		Port:     input.Port,
		Username: input.Username,
		Password: input.Password,
		Database: input.Database,
		URI:      input.URI,
	})
	if err != nil {
		return nil, err
	}

	logger.Info("Published Kubernetes binding", slog.Int("resources", len(resources)))
	return DeployKubernetesResourcesOutput{Resources: resources}, nil
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

// DeleteKubernetesResources removes the database binding (Service + Secret) from
// the target namespace via the Kubernetes API.
func DeleteKubernetesResources(ctx workflow.ActivityContext) (any, error) {
	input := DeleteKubernetesResourcesInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	logger := slog.Default()
	logger.Info("Deleting Kubernetes binding",
		slog.String("namespace", input.Namespace), slog.String("name", input.Name))

	binder, err := newKubeBinder(ctx.Context())
	if err != nil {
		return nil, err
	}

	if err := binder.DeleteBinding(ctx.Context(), input.Namespace, input.Name); err != nil {
		return nil, err
	}

	return DeleteKubernetesResourcesOutput{}, nil
}
