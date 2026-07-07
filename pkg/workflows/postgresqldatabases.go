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

	// 1. Provision the login role, 2. create the database it owns (which returns
	// the reachable host/port), then 3. publish the connection binding into
	// Kubernetes. Ordering matters: the binding Secret carries the final
	// credentials, so it is published last.
	//
	// NOTE (scaffold limitation): each Put provisions a FRESH role + database
	// (unique random suffix) rather than reading and reusing a prior binding from
	// request.Resource.Properties["status"]["binding"]. A real Radius recipe would
	// reuse the existing binding on update; here a redeploy of the same resource
	// creates new objects and leaves the old ones for PostgresSQLDatabasesDelete
	// to reclaim. See CLAUDE.md "Known limitations".
	credentials, err := activities.CallCreatePostgresUser(ctx, activities.CreatePostgresUserInput{})
	if err != nil {
		return nil, err
	}

	database, err := activities.CallCreatePostgresDatabase(ctx, activities.CreatePostgresDatabaseInput{
		Username:       credentials.Username,
		Password:       credentials.Password,
		DatabasePrefix: request.Resource.Name,
	})
	if err != nil {
		return nil, err
	}

	// Build the advertised connection URI via url.URL escaping (activities.ConnectionURI)
	// so a resource-derived database name with URL-special characters cannot corrupt it.
	uri := activities.ConnectionURI(credentials.Username, credentials.Password, database.Host, database.Port, database.Database)

	deployed, err := activities.CallDeployKubernetesResources(ctx, activities.DeployKubernetesResourcesInput{
		Namespace: request.Runtime.Kubernetes.Namespace,
		Name:      request.Resource.Name,
		Host:      database.Host,
		Port:      database.Port,
		Username:  credentials.Username,
		Password:  credentials.Password,
		Database:  database.Database,
		URI:       uri,
	})
	if err != nil {
		return nil, err
	}

	// Return data to Radius
	result := recipes.Result{
		Values: map[string]any{
			"host":     database.Host,
			"port":     database.Port,
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

	database, ok := request.Resource.GetStringValue("/status/binding/database")
	if ok {
		_, err = activities.CallDeletePostgresDatabase(ctx, activities.DeletePostgresDatabaseInput{
			Database:     database,
			CreateBackup: true,
		})
		if err != nil {
			return nil, err
		}
	}

	username, ok := request.Resource.GetStringValue("/status/binding/username")
	if ok {
		_, err = activities.CallDeletePostgresUser(ctx, activities.DeletePostgresUserInput{
			Username: username,
		})
		if err != nil {
			return nil, err
		}
	}

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
