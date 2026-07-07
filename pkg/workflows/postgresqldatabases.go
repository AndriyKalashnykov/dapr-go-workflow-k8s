package workflows

import (
	"log/slog"

	"github.com/dapr/durabletask-go/workflow"

	"github.com/AndriyKalashnykov/dapr-go-workflow-k8s/pkg/activities"
	"github.com/AndriyKalashnykov/dapr-go-workflow-k8s/pkg/recipes"
)

func PostgresSQLDatabasesPut(ctx *workflow.WorkflowContext) (any, error) {
	request := recipes.Context{}
	err := ctx.GetInput(&request)
	if err != nil {
		return nil, err
	}

	logger := slog.Default()
	if ctx.IsReplaying() {
		logger.Info("Resuming PostgresSQL database creation/update")
	} else {
		logger.Info("Creating/Updating PostgresSQL database")
	}

	namespace := request.Runtime.Kubernetes.Namespace
	name := request.Resource.Name

	// 1. Deploy the PostgreSQL workload (Deployment + Service + Secret) and wait
	// for it to be ready. This returns the in-cluster Service DNS consumers use
	// AND a host-reachable superuser endpoint used to run the provisioning DDL.
	deployed, err := activities.CallDeployKubernetesResources(ctx, activities.DeployKubernetesResourcesInput{
		Namespace: namespace,
		Name:      name,
	})
	if err != nil {
		return nil, err
	}

	admin := activities.AdminConn{
		AdminHost:     deployed.ReachableHost,
		AdminPort:     deployed.ReachablePort,
		AdminUser:     deployed.AdminUser,
		AdminPassword: deployed.AdminPassword,
	}

	// Idempotent update: if the recipe context carries a prior status.binding,
	// reuse those role and database names on the (now redeployed) instance — the
	// role's password is rotated, the database ensured. A first Put has empty
	// bindings, so fresh unique names are generated.
	existingUser, _ := request.Resource.GetStringValue("/status/binding/username")
	existingDatabase, _ := request.Resource.GetStringValue("/status/binding/database")

	// 2. Provision (or reuse) the login role on the deployed instance.
	credentials, err := activities.CallCreatePostgresUser(ctx, activities.CreatePostgresUserInput{
		AdminConn: admin,
		Username:  existingUser,
	})
	if err != nil {
		return nil, err
	}

	// 3. Create (or ensure) the database it owns.
	database, err := activities.CallCreatePostgresDatabase(ctx, activities.CreatePostgresDatabaseInput{
		AdminConn:      admin,
		Username:       credentials.Username,
		DatabasePrefix: name,
		Database:       existingDatabase,
	})
	if err != nil {
		return nil, err
	}

	// The recipe advertises the in-cluster Service DNS (what consumers connect
	// to). ConnectionURI's url.URL escaping keeps a resource-derived database name
	// from corrupting the connection string.
	uri := activities.ConnectionURI(credentials.Username, credentials.Password, deployed.InClusterHost, deployed.Port, database.Database)

	result := recipes.Result{
		Values: map[string]any{
			"host":     deployed.InClusterHost,
			"port":     deployed.Port,
			"username": credentials.Username,
			"database": database.Database,
		},
		Secrets: map[string]any{
			"password": credentials.Password,
			"uri":      uri,
		},
		Resources: deployed.Resources,
	}

	logger.Info("Done creating/updating PostgresSQL database")
	return result, nil
}

func PostgresSQLDatabasesDelete(ctx *workflow.WorkflowContext) (any, error) {
	request := recipes.Context{}
	err := ctx.GetInput(&request)
	if err != nil {
		return nil, err
	}

	logger := slog.Default()
	if ctx.IsReplaying() {
		logger.Info("Resuming PostgresSQL database deletion")
	} else {
		logger.Info("Deleting PostgresSQL database")
	}

	// Destroying the workload (Deployment + Service + Secret) destroys the
	// PostgreSQL instance and every role/database it held — no per-object drop
	// needed.
	_, err = activities.CallDeleteKubernetesResources(ctx, activities.DeleteKubernetesResourcesInput{
		Namespace: request.Runtime.Kubernetes.Namespace,
		Name:      request.Resource.Name,
	})
	if err != nil {
		return nil, err
	}

	logger.Info("Done deleting PostgresSQL database")
	return struct{}{}, nil
}
