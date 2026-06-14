package activities

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dapr/durabletask-go/api"
	"github.com/dapr/durabletask-go/api/protos"
	"github.com/dapr/durabletask-go/workflow"
)

// fakeActivityCtx implements workflow.ActivityContext (task.ActivityContext) for
// unit-testing activity functions in isolation. GetInput JSON-round-trips the
// supplied input, matching how the real runtime serializes activity inputs.
type fakeActivityCtx struct {
	input any
}

func (f fakeActivityCtx) GetInput(resultPtr any) error {
	b, err := json.Marshal(f.input)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, resultPtr)
}

func (f fakeActivityCtx) GetTaskID() int32                             { return 0 }
func (f fakeActivityCtx) GetTaskExecutionID() string                   { return "" }
func (f fakeActivityCtx) Context() context.Context                     { return context.Background() }
func (f fakeActivityCtx) GetTraceContext() *protos.TraceContext        { return nil }
func (f fakeActivityCtx) GetPropagatedHistory() *api.PropagatedHistory { return nil }

var _ workflow.ActivityContext = fakeActivityCtx{}

func TestCreatePostgresUser(t *testing.T) {
	out, err := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresUserOutput)
	if got.Username != "pguser" {
		t.Errorf("username = %q, want pguser", got.Username)
	}
	if got.Password == "" {
		t.Error("expected a generated password, got empty")
	}
}

func TestCreatePostgresUserPasswordsAreUnique(t *testing.T) {
	a, _ := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{}})
	b, _ := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{}})
	if a.(CreatePostgresUserOutput).Password == b.(CreatePostgresUserOutput).Password {
		t.Error("expected distinct passwords across invocations")
	}
}

func TestCreatePostgresDatabase(t *testing.T) {
	out, err := CreatePostgresDatabase(fakeActivityCtx{input: CreatePostgresDatabaseInput{
		Username:       "u",
		Password:       "p",
		DatabasePrefix: "orders",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresDatabaseOutput)
	if !strings.HasPrefix(got.Database, "orders_") {
		t.Errorf("database = %q, want prefix orders_", got.Database)
	}
}

func TestDeletePostgresUser(t *testing.T) {
	out, err := DeletePostgresUser(fakeActivityCtx{input: DeletePostgresUserInput{Username: "u"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := out.(DeletePostgresUserOutput); !ok {
		t.Errorf("unexpected output type %T", out)
	}
}

func TestDeletePostgresDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: DeletePostgresDatabase simulates a 5s backup")
	}
	out, err := DeletePostgresDatabase(fakeActivityCtx{input: DeletePostgresDatabaseInput{
		Database:     "orders_x",
		CreateBackup: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := out.(DeletePostgresDatabaseOutput); !ok {
		t.Errorf("unexpected output type %T", out)
	}
}

func TestDeployKubernetesResources(t *testing.T) {
	out, err := DeployKubernetesResources(fakeActivityCtx{input: DeployKubernetesResourcesInput{
		Namespace: "team-a",
		Name:      "db1",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(DeployKubernetesResourcesOutput)
	if got.Host != "team-a.db1.svc.cluster.local" {
		t.Errorf("host = %q", got.Host)
	}
	if got.Port != 5432 {
		t.Errorf("port = %d, want 5432", got.Port)
	}
	if len(got.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(got.Resources))
	}
}

func TestDeleteKubernetesResources(t *testing.T) {
	out, err := DeleteKubernetesResources(fakeActivityCtx{input: DeleteKubernetesResourcesInput{
		Namespace: "team-a",
		Name:      "db1",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := out.(DeleteKubernetesResourcesOutput); !ok {
		t.Errorf("unexpected output type %T", out)
	}
}
