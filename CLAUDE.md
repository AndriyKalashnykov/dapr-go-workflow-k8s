# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Go service that drives **Radius Recipes** via **Dapr durable workflows**. It exposes a small REST API that schedules Dapr workflows; each workflow orchestrates activities to provision (or tear down) a PostgreSQL database and its backing Kubernetes resources, then returns recipe outputs (values, secrets, resources) in the shape Radius expects.

**Owner:** AndriyKalashnykov/dapr-go-workflow-k8s

> **The activities call real backends.** `pkg/activities/postgres.go` runs real DDL via **pgx/v5** against a PostgreSQL admin endpoint (`CREATE ROLE` / `CREATE DATABASE` / `DROP … WITH (FORCE)` / `DROP ROLE`), and `pkg/activities/kubernetes.go` publishes a real **Service + Secret** binding into the cluster via **client-go** (removed on delete). The returned recipe URI is genuinely connectable (built with `url.URL` escaping via `activities.ConnectionURI`). Backend clients are constructed through injectable package-level constructors (`newPGAdmin`, `newKubeBinder`) so activities are unit-tested hermetically against fakes; the real path is exercised by `make e2e`. The remaining extension point is swapping the Kubernetes binding for a full workload deploy (Deployment/StatefulSet) and wiring cloud-provider scopes (`recipes.ProviderAzure`/`ProviderAWS`).

### Update idempotency & timeouts

`PostgresSQLDatabasesPut` is **update-idempotent**: it reads the prior
`status.binding` (`username`/`database`) from the recipe context — the same shape
`PostgresSQLDatabasesDelete` reads — and reuses those role/database names on a
redeploy (the role password is rotated, the database ensured) instead of creating
fresh objects and orphaning the old ones. A first Put (empty binding) generates
fresh unique names.

Individual DDL statements are bounded by `PG_OP_TIMEOUT_SECONDS` (default 30s) so a
statement blocked on a lock cannot hang the activity; the admin dial is bounded
separately by `PG_CONNECT_TIMEOUT_SECONDS`.

### Known limitations (scaffold)

- **Delete's backup is best-effort.** `DeletePostgresDatabase` runs `pg_dump` only if the binary is on `PATH`; a missing/failed backup is logged (`Warn`) and the `DROP DATABASE … WITH (FORCE)` proceeds regardless — the drop is the required side effect, the backup is advisory.

## Architecture

Two cooperating runtimes started from `cmd/main.go`, both sharing one `daprclient.Client`:

1. **Dapr workflow worker** (`registerWorkflows` in `main.go`) — builds a `workflow.NewRegistry()`, registers workflows/activities with `r.AddWorkflow`/`r.AddActivity`, then `wfClient.StartWorker(ctx, r)`. The workflow authoring/management API lives in **`github.com/dapr/durabletask-go/workflow`** (NOT the old `github.com/dapr/go-sdk/workflow`, which was removed in go-sdk v1.14). The client is `workflow.NewClient(dapr.GrpcClientConn())`. Workflows register by their **Go function name** (e.g. `PostgresSQLDatabasesPut`), which is the name clients must use to schedule them.
2. **HTTP server** (`pkg/server/server.go`) — listens on `:7999` by default (`server.Address()` honors `APP_PORT`) with three routes:
   - `GET /healthz` → `{"status":"ok"}`
   - `PUT /workflows` → decodes a `WorkflowRequest{name, input, id?}`, schedules the named workflow with `input` passed as raw JSON (`workflow.WithRawInput`), returns `201 {"id": <instanceID>}`
   - `GET /workflows/{id}` → `FetchWorkflowMetadata`, mapped to a stable `WorkflowStatus` DTO (`pkg/server/workflow.go`) that decouples the API from the durabletask protobuf type

Request/response flow: client `PUT /workflows` → server schedules it on the worker → worker runs the workflow → workflow calls activities → client polls `GET /workflows/{id}`. The DTO's `runtimeStatus` is a human-readable string (`RUNNING`/`COMPLETED`/`FAILED`/`CANCELED`/`TERMINATED`).

### Package layout & key patterns

- `pkg/workflows/postgresqldatabases.go` — the two workflows (`PostgresSQLDatabasesPut`, `PostgresSQLDatabasesDelete`). They deserialize a `recipes.Context` from workflow input, call activities, and (for Put) assemble a `recipes.Result`. Put order: **create role → create database (returns the reachable host/port) → publish the K8s Service+Secret binding** (published last because the Secret carries the final credentials). Workflows must be **deterministic & replay-safe** — they branch on `ctx.IsReplaying()` only for logging; all side effects go through activities.
- `pkg/activities/{kubernetes,postgres}.go` — each activity has **two functions**: a `Call*` helper (invoked from a workflow; wraps `ctx.CallActivity(...).Await(&out)` with typed in/out structs) and the registered activity itself (`func(ctx workflow.ActivityContext) (any, error)`). When adding an activity, follow this pair pattern AND register it in `main.go`'s `registerWorkflows`.
- `pkg/activities/pgclient.go` / `kubeclient.go` — the real backend clients behind small interfaces (`pgAdmin`, `kubeBinder`), each constructed via a package-level `var new*` so tests inject fakes (`kubeclient_test.go` exercises the real binder against client-go's in-memory fake clientset). `pkg/activities/config.go` resolves the PostgreSQL admin endpoint from `PG_ADMIN_*` (falling back to `POSTGRES_*`).
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

Because the activities call real backends, running the workflow needs: a reachable **PostgreSQL admin endpoint** (`PG_ADMIN_*`, defaulting to the `POSTGRES_*` dev container) and a **kubeconfig** for a cluster (`KUBECONFIG` / `~/.kube/config`; `make e2e` provisions its own kind cluster). Fixed host ports are guarded before every bind by **`make check-ports`** (a prerequisite of `e2e`/`run`/`postgres-start`), which fails early naming the holder; override the matching `*_PORT` (or set it in `.env`) on a collision.

## Testing notes

- **Unit-tested**: `pkg/recipes` (100%), `pkg/activities` activity funcs (via a fake `ActivityContext`) + the real `kubeBinder` (via client-go's in-memory fake clientset in `kubeclient_test.go`), `pkg/server` (DTO, `Address`, handlers, `Start` lifecycle), `cmd/healthcheck`. `COVERAGE_THRESHOLD` defaults to **40%** because the pgx admin client, the workflow-orchestration funcs and their `Call*` wrappers are covered by `make e2e`, not unit tests.
- **`make e2e`** is the real end-to-end test (`e2e/e2e-test.sh`): it `dapr init`s a control plane, starts PostgreSQL, **creates a kind cluster**, runs the app under a Dapr sidecar (with `KUBECONFIG` pointing at kind), schedules `PostgresSQLDatabasesPut`, and — beyond asserting the output shape — verifies the recipe **actually provisioned real backends**: the Postgres role + database exist and the returned URI authenticates, and a real Kubernetes Service + Secret were created. It then runs `PostgresSQLDatabasesDelete` and asserts the role, database, Service, and Secret are all gone. It manages (creates + deletes) its own kind cluster; `E2E_KIND_KEEP=1` keeps it. Runs in CI (the `e2e` job; kind + kubectl come from mise) and locally.
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
