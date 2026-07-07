package activities

import (
	"log/slog"
	"strings"

	"github.com/dapr/durabletask-go/workflow"
	"github.com/google/uuid"
)

// shortID returns 8 hex characters suitable for a unique, DNS/identifier-safe
// suffix on generated role and database names.
func shortID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
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
	// Username, when non-empty, reuses an existing role (idempotent update): the
	// role's password is rotated rather than a new role being created. Empty
	// generates a fresh unique role name.
	Username string `json:"username,omitempty"`
}

type CreatePostgresUserOutput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreatePostgresUser provisions (or, when Username is supplied, reuses) a LOGIN
// role on the admin PostgreSQL endpoint with a freshly generated password.
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

	admin, err := newPGAdmin(ctx.Context())
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

func CallDeletePostgresUser(ctx *workflow.WorkflowContext, input DeletePostgresUserInput) (DeletePostgresUserOutput, error) {
	task := ctx.CallActivity(DeletePostgresUser, workflow.WithActivityInput(input))

	output := DeletePostgresUserOutput{}
	err := task.Await(&output)
	if err != nil {
		return DeletePostgresUserOutput{}, err
	}

	return output, nil
}

type DeletePostgresUserInput struct {
	Username string `json:"username"`
}

type DeletePostgresUserOutput struct {
}

// DeletePostgresUser drops the role from the admin PostgreSQL endpoint.
func DeletePostgresUser(ctx workflow.ActivityContext) (any, error) {
	input := DeletePostgresUserInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	logger := slog.Default()
	logger.Info("Deleting postgres role", slog.String("username", input.Username))

	admin, err := newPGAdmin(ctx.Context())
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(ctx.Context()) }()

	if err := admin.DropRole(ctx.Context(), input.Username); err != nil {
		return nil, err
	}

	return DeletePostgresUserOutput{}, nil
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
	Username       string `json:"username"`
	Password       string `json:"password"`
	DatabasePrefix string `json:"databasePrefix"`
	// Database, when non-empty, reuses an existing database (idempotent update):
	// CreateDatabase is a no-op if it already exists. Empty generates a fresh
	// unique name from DatabasePrefix.
	Database string `json:"database,omitempty"`
}

type CreatePostgresDatabaseOutput struct {
	Database string `json:"database"`
	// Host and Port locate the server the database was created on — the same
	// reachable admin endpoint — so the recipe's advertised URI is connectable.
	Host string `json:"host"`
	Port string `json:"port"`
}

// CreatePostgresDatabase creates a uniquely named database owned by the role on
// the admin PostgreSQL endpoint and returns the reachable host/port.
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

	admin, err := newPGAdmin(ctx.Context())
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(ctx.Context()) }()

	if err := admin.CreateDatabase(ctx.Context(), database, input.Username); err != nil {
		return nil, err
	}

	ep := adminEndpoint()
	return CreatePostgresDatabaseOutput{
		Database: database,
		Host:     ep.Host,
		Port:     ep.Port,
	}, nil
}

func CallDeletePostgresDatabase(ctx *workflow.WorkflowContext, input DeletePostgresDatabaseInput) (DeletePostgresDatabaseOutput, error) {
	task := ctx.CallActivity(DeletePostgresDatabase, workflow.WithActivityInput(input))

	output := DeletePostgresDatabaseOutput{}
	err := task.Await(&output)
	if err != nil {
		return DeletePostgresDatabaseOutput{}, err
	}

	return output, nil
}

type DeletePostgresDatabaseInput struct {
	Database     string `json:"database"`
	CreateBackup bool   `json:"createBackup"`
}

type DeletePostgresDatabaseOutput struct {
}

// DeletePostgresDatabase optionally backs up (best-effort pg_dump) then drops the
// database from the admin PostgreSQL endpoint.
func DeletePostgresDatabase(ctx workflow.ActivityContext) (any, error) {
	input := DeletePostgresDatabaseInput{}
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}

	logger := slog.Default()
	logger.Info("Deleting database", slog.String("database", input.Database), slog.Bool("backup", input.CreateBackup))

	admin, err := newPGAdmin(ctx.Context())
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(ctx.Context()) }()

	if err := admin.DropDatabase(ctx.Context(), input.Database, input.CreateBackup); err != nil {
		return nil, err
	}

	return DeletePostgresDatabaseOutput{}, nil
}
