# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Go service that drives **Radius Recipes** via **Dapr durable workflows**. It exposes a small REST API that schedules Dapr workflows; each workflow orchestrates activities to provision (or tear down) a PostgreSQL database and its backing Kubernetes resources, then returns recipe outputs (values, secrets, resources) in the shape Radius expects.

**Owner:** AndriyKalashnykov/dapr-go-workflow-k8s

> **The activities deploy a real PostgreSQL workload.** `pkg/activities/kubernetes.go` uses **client-go** to deploy a dedicated PostgreSQL **Deployment + Service (NodePort) + Secret** per resource and waits for the rollout; `pkg/activities/postgres.go` then runs real DDL via **pgx/v5** (`CREATE ROLE` / `CREATE DATABASE`) on that deployed instance, reached from the host via the Service's node-IP:NodePort. The recipe advertises the in-cluster Service DNS (`<name>.<ns>.svc.cluster.local`) and a genuinely-connectable URI (`activities.ConnectionURI`, `url.URL`-escaped). Backend clients are built through injectable constructors (`newPGAdmin`, `newKubeDeployer`) so activities are unit-tested hermetically (fakes + client-go's fake clientset); the real path is exercised by `make e2e`. Delete destroys the workload (and with it the database/roles). The remaining extension point is wiring the cloud-provider scopes (`recipes.ProviderAzure`/`ProviderAWS`).

### Update idempotency & timeouts

`PostgresSQLDatabasesPut` is **update-idempotent**: the workload deploy reuses the
existing Deployment/Service/Secret (an existing Secret's superuser password is
kept, since a running pod's env can't change), and it reads the prior
`status.binding` (`username`/`database`) from the recipe context to reuse those
role/database names on the redeployed instance (the role password is rotated, the
database ensured). A first Put (empty binding) generates fresh unique names.

Individual DDL statements are bounded by `PG_OP_TIMEOUT_SECONDS` (default 30s) so a
statement blocked on a lock cannot hang the activity; the pgx dial is bounded by
`PG_CONNECT_TIMEOUT_SECONDS`, and the workload rollout wait by
`PG_WORKLOAD_ROLLOUT_TIMEOUT_SECONDS`.

### Known limitations (scaffold)

- **Ephemeral storage.** The deployed Postgres has no PersistentVolume, so its data lives in the pod and is lost if the pod restarts — fine for the demo lifecycle, not for real durability.
- **Host-reachability of the deployed instance is kind-specific.** Provisioning DDL reaches the workload via node-IP:NodePort, which is host-routable on kind (Linux/CI). A different cluster topology would need port-forward or in-cluster execution.

## Architecture

Two cooperating runtimes started from `cmd/main.go`, both sharing one `daprclient.Client`:

1. **Dapr workflow worker** (`registerWorkflows` in `main.go`) — builds a `workflow.NewRegistry()`, registers workflows/activities with `r.AddWorkflow`/`r.AddActivity`, then `wfClient.StartWorker(ctx, r)`. The workflow authoring/management API lives in **`github.com/dapr/durabletask-go/workflow`** (NOT the old `github.com/dapr/go-sdk/workflow`, which was removed in go-sdk v1.14). The client is `workflow.NewClient(dapr.GrpcClientConn())`. Workflows register by their **Go function name** (e.g. `PostgresSQLDatabasesPut`), which is the name clients must use to schedule them.
2. **HTTP server** (`pkg/server/server.go`) — listens on `:7999` by default (`server.Address()` honors `APP_PORT`) with three routes:
   - `GET /healthz` → `{"status":"ok"}`
   - `PUT /workflows` → decodes a `WorkflowRequest{name, input, id?}`, schedules the named workflow with `input` passed as raw JSON (`workflow.WithRawInput`), returns `201 {"id": <instanceID>}`
   - `GET /workflows/{id}` → `FetchWorkflowMetadata`, mapped to a stable `WorkflowStatus` DTO (`pkg/server/workflow.go`) that decouples the API from the durabletask protobuf type

Request/response flow: client `PUT /workflows` → server schedules it on the worker → worker runs the workflow → workflow calls activities → client polls `GET /workflows/{id}`. The DTO's `runtimeStatus` is a human-readable string (`RUNNING`/`COMPLETED`/`FAILED`/`CANCELED`/`TERMINATED`).

### Package layout & key patterns

- `pkg/workflows/postgresqldatabases.go` — the two workflows (`PostgresSQLDatabasesPut`, `PostgresSQLDatabasesDelete`). They deserialize a `recipes.Context` from workflow input, call activities, and (for Put) assemble a `recipes.Result`. Put order: **deploy the PostgreSQL workload (returns the reachable admin endpoint) → create role → create database** on it. Delete calls one activity that **destroys the workload**. Workflows must be **deterministic & replay-safe** — they branch on `ctx.IsReplaying()` only for logging; all side effects go through activities.
- `pkg/activities/{kubernetes,postgres}.go` — each activity has **two functions**: a `Call*` helper (invoked from a workflow; wraps `ctx.CallActivity(...).Await(&out)` with typed in/out structs) and the registered activity itself (`func(ctx workflow.ActivityContext) (any, error)`). When adding an activity, follow this pair pattern AND register it in `main.go`'s `registerWorkflows`.
- `pkg/activities/pgclient.go` / `kubeclient.go` — the real backend clients behind small interfaces (`pgAdmin`, `kubeDeployer`), each constructed via a package-level `var new*` so tests inject fakes (`kubeclient_test.go` exercises the real `clientsetDeployer` against client-go's in-memory fake clientset). `pkg/activities/config.go` holds the env-tunables (`workloadImage`, timeouts). The pgx admin endpoint is threaded from the deploy (`activities.AdminConn`), not static env.
- `pkg/recipes/` — the **Radius Recipe contract** types. `Context` mirrors a Radius recipe context (`Resource`, `Application`, `Environment`, `Runtime.Kubernetes`, plus `Azure`/`AWS` provider scopes); `Result` is the `{values, secrets, resources}` envelope Radius reads back. `Resource.GetStringValue` uses JSON-pointer lookups into `Properties`. `recipes/error.go` re-exports the `server` error types as aliases.
- `pkg/server/error.go` — the structured error envelope (`{error:{code,message,...}}`) used by `mustWriteError`.

## Build, Run & Test

Toolchain and quality tools are managed by **mise** (`.mise.toml`); `make deps` bootstraps them. The composite gate is `make static-check` (Go-version alignment + workflow/shell lint via actionlint/shellcheck + golangci-lint + govulncheck + gitleaks + trivy-fs + `mermaid-lint` via `minlag/mermaid-cli`). `make ci` runs the full local pipeline.

```bash
make ci          # deps + format + static-check + test + coverage-check + build
make build       # CGO_ENABLED=0 GOOS=linux GOARCH=amd64 → ./cmd/main
make test        # go test -race -coverprofile=coverage.out ./...
make static-check# composite quality gate
make e2e         # real end-to-end: kind + Postgres + Dapr, provisions & tears down
make check-ports # fail early if a guarded host port (Postgres/app/grpc) is taken
make ci-run      # run the GitHub Actions workflow locally via act
make run         # ./run-dapr.sh — dapr run, app-id sample, app-port 7999, grpc 50001
make release     # validate semver, commit version.txt, tag, push
```

Run a single test: `go test -run '^TestName$' ./pkg/<pkg>/...`. Skip the slow 5s backup simulation: `go test -short ./...`.

### Local end-to-end loop (manual)

```bash
make postgres-start  # docker postgres on :5432 (POSTGRES_PASSWORD default daprrulz)
make run             # starts the app under a Dapr sidecar (run-dapr.sh → go run ./cmd/main.go)
./start-workflow.sh  # health-check, PUT PostgresSQLDatabasesPut, poll runtimeStatus until terminal
make postgres-stop
```

`run-dapr.sh` points Dapr at `components/` (a `state.postgresql` statestore + a tracing `Configuration` wiring Zipkin at `localhost:9411`). All host/port values are env-driven (see `.env.example`), and `make` reads `.env` too (`-include .env`).

Because the recipe deploys a real workload, running it needs a **kubeconfig** for a cluster (`KUBECONFIG` / `~/.kube/config`; `make e2e` provisions its own kind cluster) whose node IP is host-reachable (kind on Linux). The app still needs its own state-store PostgreSQL (`make postgres-start`) and a Dapr sidecar. Fixed host ports are guarded before every bind by **`make check-ports`** (a prerequisite of `e2e`/`run`/`postgres-start`), which fails early naming the holder; override the matching `*_PORT` (or set it in `.env`) on a collision.

## Testing notes

- **Unit-tested**: `pkg/recipes` (100%), `pkg/activities` activity funcs (via a fake `ActivityContext`) + the real `clientsetDeployer` (object creation, idempotency, `waitAndDiscover`, delete — via client-go's in-memory fake clientset in `kubeclient_test.go`), `pkg/server`, `cmd/healthcheck`. `COVERAGE_THRESHOLD` defaults to **40%** because the pgx admin client, the deploy rollout, and the workflow-orchestration funcs are covered by `make e2e`, not unit tests.
- **`make e2e`** is the real end-to-end test (`e2e/e2e-test.sh`): it `dapr init`s a control plane, starts the state-store PostgreSQL, **creates a kind cluster**, runs the app under a Dapr sidecar (`KUBECONFIG` → kind), schedules `PostgresSQLDatabasesPut`, and verifies the recipe **actually deployed a PostgreSQL workload**: the Deployment has a ready replica, and the provisioned role authenticates to its database **on the deployed instance** (via node-IP:NodePort). It then re-Puts (asserting idempotent reuse with no orphan) and runs `PostgresSQLDatabasesDelete`, asserting the Deployment/Service/Secret are gone. It manages its own kind cluster (`E2E_KIND_KEEP=1` keeps it). Runs in CI (the `e2e` job; kind + kubectl come from mise) and locally.
- **Dapr runtime ≥ 1.18 is required** (`DAPR_RUNTIME_VERSION`, default 1.18.0). go-sdk v1.15 / durabletask-go v0.12 fail activity invocation on older runtimes with `required metadata dapr-callee-app-id ... not found`.

## Skills

Use the following skills when working on related files:

| File(s) | Skill |
|---------|-------|
| `Makefile` | `/makefile` |
| `renovate.json` | `/renovate` |
| `README.md` | `/readme` |
| `.github/workflows/*.{yml,yaml}` | `/ci-workflow` |

When spawning subagents, always pass conventions from the respective skill into the agent's prompt.
