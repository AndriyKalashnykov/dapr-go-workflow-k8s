# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Go service that drives **Radius Recipes** via **Dapr durable workflows**. It exposes a small REST API that schedules Dapr workflows; each workflow orchestrates activities to provision (or tear down) a PostgreSQL database and its backing Kubernetes resources, then returns recipe outputs (values, secrets, resources) in the shape Radius expects.

**Owner:** AndriyKalashnykov/dapr-go-workflow-k8s

> **The activities are demo stubs, not real implementations.** `pkg/activities/*.go` only log and `time.Sleep` ("Pretend we are deploying resources..."), generate a UUID password, and return synthetic hostnames/ports. There is no real `kubectl`, Postgres client, or cloud SDK call anywhere. Treat this repo as a Radius-recipe-on-Dapr-workflows *scaffold*; wiring activities to real backends is the intended extension point.

## Architecture

Two cooperating runtimes started from `cmd/main.go`, both sharing one `daprclient.Client`:

1. **Dapr workflow worker** (`registerWorkflows` in `main.go`) — builds a `workflow.NewRegistry()`, registers workflows/activities with `r.AddWorkflow`/`r.AddActivity`, then `wfClient.StartWorker(ctx, r)`. The workflow authoring/management API lives in **`github.com/dapr/durabletask-go/workflow`** (NOT the old `github.com/dapr/go-sdk/workflow`, which was removed in go-sdk v1.14). The client is `workflow.NewClient(dapr.GrpcClientConn())`. Workflows register by their **Go function name** (e.g. `PostgresSQLDatabasesPut`), which is the name clients must use to schedule them.
2. **HTTP server** (`pkg/server/server.go`) — listens on `:7999` by default (`server.Address()` honors `APP_PORT`) with three routes:
   - `GET /healthz` → `{"status":"ok"}`
   - `PUT /workflows` → decodes a `WorkflowRequest{name, input, id?}`, schedules the named workflow with `input` passed as raw JSON (`workflow.WithRawInput`), returns `201 {"id": <instanceID>}`
   - `GET /workflows/{id}` → `FetchWorkflowMetadata`, mapped to a stable `WorkflowStatus` DTO (`pkg/server/workflow.go`) that decouples the API from the durabletask protobuf type

Request/response flow: client `PUT /workflows` → server schedules it on the worker → worker runs the workflow → workflow calls activities → client polls `GET /workflows/{id}`. The DTO's `runtimeStatus` is a human-readable string (`RUNNING`/`COMPLETED`/`FAILED`/`CANCELED`/`TERMINATED`).

### Package layout & key patterns

- `pkg/workflows/postgresqldatabases.go` — the two workflows (`PostgresSQLDatabasesPut`, `PostgresSQLDatabasesDelete`). They deserialize a `recipes.Context` from workflow input, call activities, and (for Put) assemble a `recipes.Result`. Workflows must be **deterministic & replay-safe** — they branch on `ctx.IsReplaying()` only for logging; all side effects go through activities.
- `pkg/activities/{kubernetes,postgres}.go` — each activity has **two functions**: a `Call*` helper (invoked from a workflow; wraps `ctx.CallActivity(...).Await(&out)` with typed in/out structs) and the registered activity itself (`func(ctx workflow.ActivityContext) (any, error)`). When adding an activity, follow this pair pattern AND register it in `main.go`'s `registerWorkflows`.
- `pkg/recipes/` — the **Radius Recipe contract** types. `Context` mirrors a Radius recipe context (`Resource`, `Application`, `Environment`, `Runtime.Kubernetes`, plus `Azure`/`AWS` provider scopes); `Result` is the `{values, secrets, resources}` envelope Radius reads back. `Resource.GetStringValue` uses JSON-pointer lookups into `Properties`. `recipes/error.go` re-exports the `server` error types as aliases.
- `pkg/server/error.go` — the structured error envelope (`{error:{code,message,...}}`) used by `mustWriteError`.

## Build, Run & Test

Toolchain and quality tools are managed by **mise** (`.mise.toml`); `make deps` bootstraps them. The composite gate is `make static-check` (Go-version alignment + workflow/shell lint via actionlint/shellcheck + golangci-lint + govulncheck + gitleaks + trivy-fs + `mermaid-lint` via `minlag/mermaid-cli`). `make ci` runs the full local pipeline.

```bash
make ci          # deps + format + static-check + test + coverage-check + build
make build       # CGO_ENABLED=0 GOOS=linux GOARCH=amd64 → ./cmd/main
make test        # go test -race -coverprofile=coverage.out ./...
make static-check# composite quality gate
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

`run-dapr.sh` points Dapr at `components/` (a `state.postgresql` statestore + a tracing `Configuration` wiring Zipkin at `localhost:9411`). All host/port values are env-driven (see `.env.example`).

## Testing notes

- **Unit-tested**: `pkg/recipes` (100%), `pkg/activities` activity funcs (via a fake `ActivityContext`), `pkg/server` (DTO, `Address`, handlers, `Start` lifecycle), `cmd/healthcheck`. `COVERAGE_THRESHOLD` defaults to **40%** because the workflow-orchestration funcs and their `Call*` activity wrappers are covered by `make e2e`, not unit tests.
- **`make e2e`** is the real end-to-end test: it `dapr init`s a control plane (placement + scheduler + state infra), starts PostgreSQL (waits for readiness), runs the app under a Dapr sidecar, schedules `PostgresSQLDatabasesPut` over the REST API, polls to `COMPLETED`, and asserts the recipe output shape (`e2e/e2e-test.sh`). It runs in CI (the `e2e` job) and locally.
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
