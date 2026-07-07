package activities

import (
	"log/slog"
	"strings"

	"github.com/dapr/durabletask-go/workflow"
	"github.com/google/uuid"
)

// shortID returns 8 hex characters suitable for a unique, identifier-safe suffix
// on generated role and database names.
func shortID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
}

// AdminConn carries the host-reachable superuser endpoint of the deployed
// Postgres (threaded from DeployKubernetesResources) that the provisioning DDL
// connects to.
type AdminConn struct {
	AdminHost     string `json:"adminHost"`
	AdminPort     string `json:"adminPort"`
	AdminUser     string `json:"adminUser"`
	AdminPassword string `json:"adminPassword"`
}

func (a AdminConn) endpoint() PGEndpoint {
	return PGEndpoint{
		Host:     a.AdminHost,
		Port:     a.AdminPort,
		User:     a.AdminUser,
		Password: a.AdminPassword,
		Database: postgresName, // maintenance DB
	}
}

func CallCreatePostgresUser(ctx *workflow.WorkflowContext, input CreatePostgresUserInput) (CreatePostgresUserOutput, error) {
	task := ctx.CallActivity(CreatePostgresUser, workflow.WithActivityInput(input))

	output := CreatePostgresUserOutput{}
	err := task.Await(&output)
	if err != nil {
		return CreatePostgresUserOutput{}, err
	}

	return output, nil
}

type CreatePostgresUserInput struct {
	AdminConn
	// Username, when non-empty, reuses an existing role (idempotent update): its
	// password is rotated rather than a new role being created. Empty generates a
	// fresh unique role name.
	Username string `json:"username,omitempty"`
}

type CreatePostgresUserOutput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreatePostgresUser provisions (or, when Username is supplied, reuses) a LOGIN
// role on the deployed PostgreSQL with a freshly generated password.
func CreatePostgresUser(ctx workflow.ActivityContext) (any, error) {
	input := CreatePostgresUserInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	username := input.Username
	if username == "" {
		username = "pguser_" + shortID()
	}
	password := uuid.NewString()

	logger := slog.Default()
	logger.Info("Creating postgres role", slog.String("username", username))

	admin, err := newPGAdmin(ctx.Context(), input.endpoint())
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(ctx.Context()) }()

	if err := admin.CreateRole(ctx.Context(), username, password); err != nil {
		return nil, err
	}

	return CreatePostgresUserOutput{
		Username: username,
		Password: password,
	}, nil
}

func CallCreatePostgresDatabase(ctx *workflow.WorkflowContext, input CreatePostgresDatabaseInput) (CreatePostgresDatabaseOutput, error) {
	task := ctx.CallActivity(CreatePostgresDatabase, workflow.WithActivityInput(input))

	output := CreatePostgresDatabaseOutput{}
	err := task.Await(&output)
	if err != nil {
		return CreatePostgresDatabaseOutput{}, err
	}

	return output, nil
}

type CreatePostgresDatabaseInput struct {
	AdminConn
	Username       string `json:"username"`
	DatabasePrefix string `json:"databasePrefix"`
	// Database, when non-empty, reuses an existing database (idempotent update):
	// CreateDatabase is a no-op if it already exists. Empty generates a fresh
	// unique name from DatabasePrefix.
	Database string `json:"database,omitempty"`
}

type CreatePostgresDatabaseOutput struct {
	Database string `json:"database"`
}

// CreatePostgresDatabase creates (or reuses) a database owned by the role on the
// deployed PostgreSQL.
func CreatePostgresDatabase(ctx workflow.ActivityContext) (any, error) {
	input := CreatePostgresDatabaseInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	database := input.Database
	if database == "" {
		database = input.DatabasePrefix + "_" + shortID()
	}

	logger := slog.Default()
	logger.Info("Creating database", slog.String("database", database), slog.String("owner", input.Username))

	admin, err := newPGAdmin(ctx.Context(), input.endpoint())
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(ctx.Context()) }()

	if err := admin.CreateDatabase(ctx.Context(), database, input.Username); err != nil {
		return nil, err
	}

	return CreatePostgresDatabaseOutput{Database: database}, nil
}
