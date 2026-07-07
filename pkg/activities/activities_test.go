package activities

import (
	"context"
	"encoding/json"
	"net/url"
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

// --- fakes for the injected backend constructors ---

type fakePGAdmin struct {
	createdRoles map[string]string // username -> password
	createdDBs   map[string]string // database -> owner
	droppedDBs   []string
	droppedRoles []string
	backups      []string
	closed       bool
}

func newFakePGAdmin() *fakePGAdmin {
	return &fakePGAdmin{createdRoles: map[string]string{}, createdDBs: map[string]string{}}
}

func (f *fakePGAdmin) CreateRole(_ context.Context, username, password string) error {
	f.createdRoles[username] = password
	return nil
}
func (f *fakePGAdmin) CreateDatabase(_ context.Context, database, owner string) error {
	f.createdDBs[database] = owner
	return nil
}
func (f *fakePGAdmin) DropDatabase(_ context.Context, database string, backup bool) error {
	if backup {
		f.backups = append(f.backups, database)
	}
	f.droppedDBs = append(f.droppedDBs, database)
	return nil
}
func (f *fakePGAdmin) DropRole(_ context.Context, username string) error {
	f.droppedRoles = append(f.droppedRoles, username)
	return nil
}
func (f *fakePGAdmin) Close(context.Context) error { f.closed = true; return nil }

// withFakePGAdmin swaps newPGAdmin for the test and restores it afterwards.
func withFakePGAdmin(t *testing.T, fake *fakePGAdmin) {
	t.Helper()
	prev := newPGAdmin
	newPGAdmin = func(context.Context) (pgAdmin, error) { return fake, nil }
	t.Cleanup(func() { newPGAdmin = prev })
}

type fakeKubeBinder struct {
	ensured map[string]BindingConnection // "ns/name" -> conn
	deleted []string
}

func newFakeKubeBinder() *fakeKubeBinder {
	return &fakeKubeBinder{ensured: map[string]BindingConnection{}}
}

func (f *fakeKubeBinder) EnsureBinding(_ context.Context, namespace, name string, conn BindingConnection) ([]string, error) {
	f.ensured[namespace+"/"+name] = conn
	return []string{
		resourceID(namespace, "Service", name),
		resourceID(namespace, "Secret", name),
	}, nil
}
func (f *fakeKubeBinder) DeleteBinding(_ context.Context, namespace, name string) error {
	f.deleted = append(f.deleted, namespace+"/"+name)
	return nil
}

func withFakeKubeBinder(t *testing.T, fake *fakeKubeBinder) {
	t.Helper()
	prev := newKubeBinder
	newKubeBinder = func(context.Context) (kubeBinder, error) { return fake, nil }
	t.Cleanup(func() { newKubeBinder = prev })
}

// --- Postgres activities ---

func TestCreatePostgresUser(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	out, err := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresUserOutput)
	if !strings.HasPrefix(got.Username, "pguser_") {
		t.Errorf("username = %q, want prefix pguser_", got.Username)
	}
	if got.Password == "" {
		t.Error("expected a generated password, got empty")
	}
	if fake.createdRoles[got.Username] != got.Password {
		t.Errorf("CreateRole not called with returned credentials: %+v", fake.createdRoles)
	}
	if !fake.closed {
		t.Error("expected admin connection to be closed")
	}
}

func TestCreatePostgresUserCredentialsAreUnique(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	a, _ := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{}})
	b, _ := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{}})
	ao, bo := a.(CreatePostgresUserOutput), b.(CreatePostgresUserOutput)
	if ao.Username == bo.Username {
		t.Error("expected distinct usernames across invocations")
	}
	if ao.Password == bo.Password {
		t.Error("expected distinct passwords across invocations")
	}
}

func TestCreatePostgresDatabase(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

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
	if got.Host == "" || got.Port == "" {
		t.Errorf("expected host/port, got host=%q port=%q", got.Host, got.Port)
	}
	if fake.createdDBs[got.Database] != "u" {
		t.Errorf("CreateDatabase not called with owner u: %+v", fake.createdDBs)
	}
}

func TestDeletePostgresUser(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	out, err := DeletePostgresUser(fakeActivityCtx{input: DeletePostgresUserInput{Username: "u"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := out.(DeletePostgresUserOutput); !ok {
		t.Errorf("unexpected output type %T", out)
	}
	if len(fake.droppedRoles) != 1 || fake.droppedRoles[0] != "u" {
		t.Errorf("expected DropRole(u), got %+v", fake.droppedRoles)
	}
}

func TestDeletePostgresDatabase(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

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
	if len(fake.droppedDBs) != 1 || fake.droppedDBs[0] != "orders_x" {
		t.Errorf("expected DropDatabase(orders_x), got %+v", fake.droppedDBs)
	}
	if len(fake.backups) != 1 {
		t.Errorf("expected a backup to be requested, got %+v", fake.backups)
	}
}

func TestDeletePostgresDatabaseNoBackup(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	_, err := DeletePostgresDatabase(fakeActivityCtx{input: DeletePostgresDatabaseInput{
		Database:     "orders_x",
		CreateBackup: false,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.backups) != 0 {
		t.Errorf("expected no backup, got %+v", fake.backups)
	}
}

// --- Kubernetes activities ---

func TestDeployKubernetesResources(t *testing.T) {
	fake := newFakeKubeBinder()
	withFakeKubeBinder(t, fake)

	out, err := DeployKubernetesResources(fakeActivityCtx{input: DeployKubernetesResourcesInput{
		Namespace: "team-a",
		Name:      "db1",
		Host:      "localhost",
		Port:      "5432",
		Username:  "u",
		Password:  "p",
		Database:  "db1_abc",
		URI:       "postgresql://u:p@localhost:5432/db1_abc",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(DeployKubernetesResourcesOutput)
	if len(got.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(got.Resources), got.Resources)
	}
	conn, ok := fake.ensured["team-a/db1"]
	if !ok {
		t.Fatalf("EnsureBinding not called for team-a/db1: %+v", fake.ensured)
	}
	if conn.URI != "postgresql://u:p@localhost:5432/db1_abc" || conn.Database != "db1_abc" {
		t.Errorf("binding connection not threaded through: %+v", conn)
	}
}

func TestDeleteKubernetesResources(t *testing.T) {
	fake := newFakeKubeBinder()
	withFakeKubeBinder(t, fake)

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
	if len(fake.deleted) != 1 || fake.deleted[0] != "team-a/db1" {
		t.Errorf("expected DeleteBinding(team-a/db1), got %+v", fake.deleted)
	}
}

// --- pure helpers ---

func TestResourceID(t *testing.T) {
	got := resourceID("default", "Service", "sample")
	want := "/planes/kubernetes/local/namespaces/default/providers/core/Service/sample"
	if got != want {
		t.Errorf("resourceID = %q, want %q", got, want)
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"abc":   "E'abc'",
		"a'b":   "E'a''b'",
		`a\b`:   `E'a\\b'`,
		"a'b\\": `E'a''b\\'`,
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdminDSNEscapesCredentials(t *testing.T) {
	dsn := adminDSN(PGEndpoint{Host: "h", Port: "5432", User: "u", Password: "p@ss:w/rd", Database: "postgres"})
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Errorf("expected sslmode=disable in DSN, got %q", dsn)
	}
	if !strings.HasPrefix(dsn, "postgres://u:") {
		t.Errorf("unexpected DSN prefix: %q", dsn)
	}
}

func TestConnectionURI(t *testing.T) {
	// Plain values round-trip verbatim.
	got := ConnectionURI("pguser_ab", "pw-123", "localhost", "5432", "orders_9f")
	if got != "postgresql://pguser_ab:pw-123@localhost:5432/orders_9f" {
		t.Errorf("ConnectionURI = %q", got)
	}
	// A database name with URL-special characters must be escaped, not injected
	// as spurious authority/query/path segments (finding #1).
	inj := ConnectionURI("u", "p", "h", "5432", "db?sslmode=disable&x=/@ ")
	u, err := url.Parse(inj)
	if err != nil {
		t.Fatalf("produced an unparseable URI %q: %v", inj, err)
	}
	if u.Scheme != "postgresql" || u.Hostname() != "h" || u.Port() != "5432" {
		t.Errorf("authority corrupted by db name: %q (host=%q port=%q)", inj, u.Hostname(), u.Port())
	}
	if u.RawQuery != "" {
		t.Errorf("db name injected a query string: %q", u.RawQuery)
	}
	if u.Path != "/db?sslmode=disable&x=/@ " {
		t.Errorf("db name not preserved after decode: %q", u.Path)
	}
}

func TestAdminEndpointDefaults(t *testing.T) {
	// With no PG_ADMIN_*/POSTGRES_* env set, defaults must be the documented dev values.
	for _, k := range []string{
		"PG_ADMIN_HOST", "PG_ADMIN_PORT", "PG_ADMIN_USER", "PG_ADMIN_PASSWORD", "PG_ADMIN_DB",
		"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_USER", "POSTGRES_PASSWORD",
	} {
		t.Setenv(k, "")
	}
	ep := adminEndpoint()
	if ep.Host != "localhost" || ep.Port != "5432" || ep.User != "postgres" || ep.Database != "postgres" {
		t.Errorf("unexpected default endpoint: %+v", ep)
	}
}
