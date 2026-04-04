# CLAUDE.md

## Project Overview

Go service that orchestrates Dapr workflows on Kubernetes. Uses the Dapr Go SDK to register durable workflows and activities for managing PostgreSQL databases and Kubernetes resources via a REST API.

**Owner:** AndriyKalashnykov/dapr-go-workflow-k8s

## Tech Stack

- **Language:** Go 1.26
- **Framework:** Dapr Go SDK (workflows + client)
- **Orchestration:** Dapr durable task framework
- **Database:** PostgreSQL (state store)
- **Infrastructure:** Kubernetes, Dapr sidecar
- **CI/CD:** GitHub Actions (ci, release, cleanup-runs)
- **Release:** GoReleaser
- **Dependency Management:** Renovate

## Project Structure

```
cmd/            - Application entrypoint (main.go)
pkg/
  activities/   - Dapr workflow activities (K8s deploy/delete, Postgres user/db CRUD)
  recipes/      - Recipe definitions
  server/       - HTTP server setup
  workflows/    - Dapr workflow definitions (PostgreSQL databases put/delete)
components/     - Dapr component configs (statestore, config)
db/             - Database initialization scripts
```

## Build & Test

```bash
make build      # Build linux/amd64 binary to ./cmd/main
make test       # Run tests
make get        # Download dependencies
make update     # Update dependencies to latest
make run        # Run via Dapr sidecar (run-dapr.sh)
make clean      # Remove built binary
make version    # Print current git tag
make release    # Tag and push a new release
```

## Key Dependencies

- `github.com/dapr/go-sdk` - Dapr client and workflow SDK
- `github.com/dapr/durabletask-go` - Durable task framework for workflows
- `github.com/google/uuid` - UUID generation

## Skills

Use the following skills when working on related files:

| File(s) | Skill |
|---------|-------|
| `Makefile` | `/makefile` |
| `renovate.json` | `/renovate` |
| `README.md` | `/readme` |
| `.github/workflows/*.yml` | `/ci-workflow` |

When spawning subagents, always pass conventions from the respective skill into the agent's prompt.
