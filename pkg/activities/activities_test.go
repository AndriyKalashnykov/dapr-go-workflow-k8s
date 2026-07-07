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

// fakeActivityCtx implements workflow.ActivityContext for unit-testing activity
// functions in isolation. GetInput JSON-round-trips the supplied input, matching
// how the real runtime serializes activity inputs.
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
	gotEndpoint  PGEndpoint
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
func (f *fakePGAdmin) Close(context.Context) error { f.closed = true; return nil }

func withFakePGAdmin(t *testing.T, fake *fakePGAdmin) {
	t.Helper()
	prev := newPGAdmin
	newPGAdmin = func(_ context.Context, ep PGEndpoint) (pgAdmin, error) {
		fake.gotEndpoint = ep
		return fake, nil
	}
	t.Cleanup(func() { newPGAdmin = prev })
}

type fakeKubeDeployer struct {
	deployed map[string]bool // "ns/name" -> true
	deleted  []string
	ret      PostgresDeployment
}

func newFakeKubeDeployer(ret PostgresDeployment) *fakeKubeDeployer {
	return &fakeKubeDeployer{deployed: map[string]bool{}, ret: ret}
}

func (f *fakeKubeDeployer) DeployPostgres(_ context.Context, namespace, name string) (PostgresDeployment, error) {
	f.deployed[namespace+"/"+name] = true
	return f.ret, nil
}
func (f *fakeKubeDeployer) DeletePostgres(_ context.Context, namespace, name string) error {
	f.deleted = append(f.deleted, namespace+"/"+name)
	return nil
}

func withFakeKubeDeployer(t *testing.T, fake *fakeKubeDeployer) {
	t.Helper()
	prev := newKubeDeployer
	newKubeDeployer = func(context.Context) (kubeDeployer, error) { return fake, nil }
	t.Cleanup(func() { newKubeDeployer = prev })
}

func testAdminConn() AdminConn {
	return AdminConn{AdminHost: "172.18.0.4", AdminPort: "32187", AdminUser: "postgres", AdminPassword: "superpw"}
}

// --- Postgres activities ---

func TestCreatePostgresUser(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	out, err := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{AdminConn: testAdminConn()}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresUserOutput)
	if !strings.HasPrefix(got.Username, "pguser_") {
		t.Errorf("username = %q, want prefix pguser_", got.Username)
	}
	if got.Password == "" {
		t.Error("expected a generated password")
	}
	if fake.createdRoles[got.Username] != got.Password {
		t.Errorf("CreateRole not called with returned credentials: %+v", fake.createdRoles)
	}
	if fake.gotEndpoint.Host != "172.18.0.4" || fake.gotEndpoint.Port != "32187" || fake.gotEndpoint.User != "postgres" {
		t.Errorf("admin endpoint not threaded from input: %+v", fake.gotEndpoint)
	}
	if !fake.closed {
		t.Error("expected admin connection to be closed")
	}
}

func TestCreatePostgresUserReusesExistingRole(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	out, err := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{AdminConn: testAdminConn(), Username: "pguser_keep"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresUserOutput)
	if got.Username != "pguser_keep" {
		t.Errorf("username = %q, want reused pguser_keep", got.Username)
	}
	if got.Password == "" || fake.createdRoles["pguser_keep"] != got.Password {
		t.Errorf("expected reused username + rotated password: %+v", fake.createdRoles)
	}
}

func TestCreatePostgresUserCredentialsAreUnique(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	a, _ := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{AdminConn: testAdminConn()}})
	b, _ := CreatePostgresUser(fakeActivityCtx{input: CreatePostgresUserInput{AdminConn: testAdminConn()}})
	ao, bo := a.(CreatePostgresUserOutput), b.(CreatePostgresUserOutput)
	if ao.Username == bo.Username || ao.Password == bo.Password {
		t.Error("expected distinct usernames and passwords across invocations")
	}
}

func TestCreatePostgresDatabase(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	out, err := CreatePostgresDatabase(fakeActivityCtx{input: CreatePostgresDatabaseInput{
		AdminConn: testAdminConn(), Username: "u", DatabasePrefix: "orders",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresDatabaseOutput)
	if !strings.HasPrefix(got.Database, "orders_") {
		t.Errorf("database = %q, want prefix orders_", got.Database)
	}
	if fake.createdDBs[got.Database] != "u" {
		t.Errorf("CreateDatabase not called with owner u: %+v", fake.createdDBs)
	}
}

func TestCreatePostgresDatabaseReusesExisting(t *testing.T) {
	fake := newFakePGAdmin()
	withFakePGAdmin(t, fake)

	out, err := CreatePostgresDatabase(fakeActivityCtx{input: CreatePostgresDatabaseInput{
		AdminConn: testAdminConn(), Username: "u", DatabasePrefix: "orders", Database: "orders_keep",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(CreatePostgresDatabaseOutput)
	if got.Database != "orders_keep" || fake.createdDBs["orders_keep"] != "u" {
		t.Errorf("expected reused database orders_keep owned by u: got %q, %+v", got.Database, fake.createdDBs)
	}
}

// --- Kubernetes activities ---

func TestDeployKubernetesResources(t *testing.T) {
	ret := PostgresDeployment{
		Resources:     []string{"a", "b", "c"},
		InClusterHost: "sample.default.svc.cluster.local",
		Port:          "5432",
		AdminUser:     "postgres",
		AdminPassword: "pw",
		ReachableHost: "172.18.0.4",
		ReachablePort: "32187",
	}
	fake := newFakeKubeDeployer(ret)
	withFakeKubeDeployer(t, fake)

	out, err := DeployKubernetesResources(fakeActivityCtx{input: DeployKubernetesResourcesInput{
		Namespace: "default", Name: "sample",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.(PostgresDeployment)
	if len(got.Resources) != 3 || got.InClusterHost != ret.InClusterHost ||
		got.ReachableHost != "172.18.0.4" || got.ReachablePort != "32187" || got.AdminPassword != "pw" {
		t.Errorf("deploy output not threaded through: %+v", got)
	}
	if !fake.deployed["default/sample"] {
		t.Errorf("DeployPostgres not called for default/sample: %+v", fake.deployed)
	}
}

func TestDeleteKubernetesResources(t *testing.T) {
	fake := newFakeKubeDeployer(PostgresDeployment{})
	withFakeKubeDeployer(t, fake)

	_, err := DeleteKubernetesResources(fakeActivityCtx{input: DeleteKubernetesResourcesInput{
		Namespace: "default", Name: "sample",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "default/sample" {
		t.Errorf("expected DeletePostgres(default/sample), got %+v", fake.deleted)
	}
}

// --- pure helpers ---

func TestResourceID(t *testing.T) {
	got := resourceID("default", "apps", "Deployment", "sample")
	want := "/planes/kubernetes/local/namespaces/default/providers/apps/Deployment/sample"
	if got != want {
		t.Errorf("resourceID = %q, want %q", got, want)
	}
}

func TestConnectionURI(t *testing.T) {
	got := ConnectionURI("pguser_ab", "pw-123", "sample.default.svc.cluster.local", "5432", "orders_9f")
	if got != "postgresql://pguser_ab:pw-123@sample.default.svc.cluster.local:5432/orders_9f" {
		t.Errorf("ConnectionURI = %q", got)
	}
	inj := ConnectionURI("u", "p", "h", "5432", "db?sslmode=disable&x=/@ ")
	u, err := url.Parse(inj)
	if err != nil {
		t.Fatalf("produced an unparseable URI %q: %v", inj, err)
	}
	if u.Scheme != "postgresql" || u.Hostname() != "h" || u.Port() != "5432" || u.RawQuery != "" {
		t.Errorf("db name corrupted the URI: %q", inj)
	}
	if u.Path != "/db?sslmode=disable&x=/@ " {
		t.Errorf("db name not preserved after decode: %q", u.Path)
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

func TestWorkloadImageDefault(t *testing.T) {
	t.Setenv("POSTGRES_WORKLOAD_IMAGE", "")
	if img := workloadImage(); img != "postgres:18-alpine" {
		t.Errorf("workloadImage default = %q, want postgres:18-alpine", img)
	}
	t.Setenv("POSTGRES_WORKLOAD_IMAGE", "postgres:17")
	if img := workloadImage(); img != "postgres:17" {
		t.Errorf("workloadImage override = %q, want postgres:17", img)
	}
}
