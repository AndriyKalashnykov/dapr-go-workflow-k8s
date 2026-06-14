.DEFAULT_GOAL := help
SHELL := /bin/bash

APP_NAME       := dapr-go-workflow-k8s
CURRENTTAG     := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
GOFLAGS        ?= -mod=mod

# Go version is the single source of truth in go.mod; .mise.toml and the
# Dockerfile mirror it (enforced by `make check-go-alignment`).
GO_VERSION     := $(shell grep -oP '^go \K[0-9]+\.[0-9]+\.[0-9]+' go.mod)

# Coverage gate (percentage). The unit-testable surface (recipes, activities,
# server, healthcheck) is well covered; the workflow-orchestration funcs and
# their `Call*` activity wrappers are exercised by `make e2e` against a live
# Dapr sidecar, not by unit tests, so the unit floor is set accordingly.
# Tunable via env or `make COVERAGE_THRESHOLD=85`.
COVERAGE_THRESHOLD ?= 40

# Dapr runtime version installed by `make e2e-deps` (see .env.example).
DAPR_RUNTIME_VERSION ?= 1.18.0

# App HTTP port (mirrors .env.example APP_PORT). `?=` lets the env / CLI override.
APP_PORT ?= 7999

# renovate: datasource=docker depName=minlag/mermaid-cli
MERMAID_CLI_VERSION := 11.15.0

# Container image — `:=` (not `?=`) so an exported DOCKER_REGISTRY/REGISTRY_OWNER
# in the shell can't silently redirect a publish to the wrong registry. A
# command-line `make IMAGE_REGISTRY=...` still overrides.
IMAGE_REGISTRY := ghcr.io
REGISTRY_OWNER := andriykalashnykov
IMAGE_NAME     := $(IMAGE_REGISTRY)/$(REGISTRY_OWNER)/$(APP_NAME)
IMAGE_TAG      ?= $(CURRENTTAG)

# mise installs tools under its shims dir; Make recipes don't source shell rc
# files, so put the shims dir (and ~/.local/bin) on PATH for every recipe.
export PATH := $(HOME)/.local/share/mise/shims:$(HOME)/.local/bin:$(PATH)

#help: @ List available tasks
help:
	@echo "Usage: make COMMAND"
	@echo "Commands :"
	@grep -E '[a-zA-Z\.\-]+:.*?@ .*$$' $(MAKEFILE_LIST) | tr -d '#' | awk 'BEGIN {FS = ":.*?@ "}; {printf "\033[32m%-22s\033[0m - %s\n", $$1, $$2}'

#deps: @ Install toolchain (Go + quality tools) via mise
deps:
	@if [ -z "$$CI" ] && ! command -v mise >/dev/null 2>&1; then \
		echo "Installing mise (no root; installs to ~/.local/bin)..."; \
		curl -fsSL https://mise.run | sh; \
		echo "mise installed — activate it, then re-run 'make deps':"; \
		echo '  echo '\''eval "$$(~/.local/bin/mise activate bash)"'\'' >> ~/.bashrc'; \
		exit 0; \
	fi
	@mise install --yes
	@export GOFLAGS=$(GOFLAGS); go mod download

#deps-check: @ Show Go version and mise-managed tool status
deps-check:
	@echo "Go version (go.mod): $(GO_VERSION)"
	@command -v mise >/dev/null 2>&1 && mise list || echo "mise not installed — run 'make deps'"

#check-go-alignment: @ Verify the Go version matches across go.mod, .mise.toml, Dockerfile
check-go-alignment:
	@set -e; \
	gomod=$$(grep -oP '^go \K[0-9]+\.[0-9]+\.[0-9]+' go.mod); \
	misefile=$$(grep -oP '^go\s*=\s*"\K[0-9]+\.[0-9]+\.[0-9]+' .mise.toml); \
	dockerfile=$$(grep -oP 'FROM golang:\K[0-9]+\.[0-9]+\.[0-9]+' Dockerfile); \
	if [ "$$gomod" != "$$misefile" ] || [ "$$gomod" != "$$dockerfile" ]; then \
		echo "ERROR: Go version disagrees across files:"; \
		printf "  %-12s %s\n" go.mod "$$gomod" .mise.toml "$$misefile" Dockerfile "$$dockerfile"; \
		exit 1; \
	fi

#clean: @ Remove build artifacts
clean:
	@rm -f ./cmd/main coverage.out

#get: @ Download and tidy dependencies
get: deps
	@export GOFLAGS=$(GOFLAGS); go get ./... && go mod tidy

#update: @ Update dependencies to latest versions
update: deps
	@export GOFLAGS=$(GOFLAGS); go get -u ./... && go mod tidy

#format: @ Auto-format Go source (gofmt + goimports via golangci-lint)
format: deps
	@golangci-lint fmt ./...

#lint: @ Run golangci-lint, go mod tidy check, hadolint, and script +x guard
lint: deps
	@golangci-lint run ./...
	@export GOFLAGS=$(GOFLAGS); go mod tidy && git diff --exit-code go.mod go.sum || { echo "ERROR: go.mod/go.sum not tidy. Run 'go mod tidy'."; exit 1; }
	@hadolint Dockerfile
	@nonexec=$$(find . -path ./.git -prune -o -name '*.sh' -not -executable -print); \
		if [ -n "$$nonexec" ]; then echo "ERROR: shell scripts missing +x:"; echo "$$nonexec" | sed 's/^/  /'; exit 1; fi

#lint-ci: @ Lint GitHub Actions workflows (actionlint + shellcheck)
lint-ci: deps
	@actionlint
	@shellcheck run-dapr.sh run-postgres.sh start-workflow.sh db/init-db.sh e2e/e2e-test.sh

#vulncheck: @ Check for known vulnerabilities in dependencies
vulncheck: deps
	@govulncheck ./...

#secrets: @ Scan for hardcoded secrets
secrets: deps
	@gitleaks detect --source . --no-banner --redact

#trivy-fs: @ Scan filesystem for vulnerabilities, secrets, and misconfigurations
trivy-fs: deps
	@trivy fs --scanners vuln,secret,misconfig --severity CRITICAL,HIGH --exit-code 1 .

#mermaid-lint: @ Validate Mermaid diagrams in markdown (same engine GitHub renders with)
mermaid-lint:
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker required for mermaid-lint"; exit 1; }
	@set -euo pipefail; \
	MD=$$(grep -lF '```mermaid' README.md CLAUDE.md 2>/dev/null || true); \
	if [ -z "$$MD" ]; then echo "No Mermaid blocks found — skipping."; exit 0; fi; \
	IMG=minlag/mermaid-cli:$(MERMAID_CLI_VERSION); \
	for a in 1 2 3; do docker pull --quiet "$$IMG" >/dev/null 2>&1 && break; [ $$a -eq 3 ] && { echo "ERROR: docker pull $$IMG failed"; exit 1; }; sleep $$((a*5)); done; \
	rc=0; \
	for md in $$MD; do \
		log=$$(mktemp); \
		if docker run --rm -v "$$PWD:/data:ro" "$$IMG" -i "/data/$$md" -o "/tmp/$$(basename $$md .md).svg" >"$$log" 2>&1; then \
			echo "  ✓ $$md"; \
		else echo "  ✗ $$md"; sed 's/^/    /' "$$log"; rc=1; fi; \
		rm -f "$$log"; \
	done; \
	exit $$rc

#static-check: @ Composite quality gate (alignment + workflow lint + lint + vuln + secrets + trivy + mermaid)
static-check: check-go-alignment lint-ci lint vulncheck secrets trivy-fs mermaid-lint
	@echo "Static check passed."

#test: @ Run unit tests with race detector and coverage
test: deps
	@export GOFLAGS=$(GOFLAGS); go test -race -coverprofile=coverage.out -covermode=atomic ./...

#coverage-check: @ Verify coverage meets COVERAGE_THRESHOLD (default 70%)
coverage-check: deps
	@if [ ! -s coverage.out ]; then echo "ERROR: coverage.out missing or empty. Run 'make test' first."; exit 1; fi
	@total=$$(go tool cover -func=coverage.out | grep '^total:' | grep -oE '[0-9]+\.[0-9]+'); \
	echo "Total coverage: $$total% (threshold: $(COVERAGE_THRESHOLD)%)"; \
	awk -v t="$$total" -v g="$(COVERAGE_THRESHOLD)" 'BEGIN { exit (t+0 >= g+0) ? 0 : 1 }' || { echo "ERROR: coverage below threshold"; exit 1; }

#e2e-deps: @ Ensure the Dapr control plane (placement + scheduler containers) is up
e2e-deps: deps
	@command -v dapr >/dev/null 2>&1 || { echo "Error: dapr CLI required (mise install)."; exit 1; }
	@# `dapr init` (Docker mode) runs placement/scheduler as CONTAINERS, so detect
	@# the control plane by container, not by binary. If it's down, init; if a prior
	@# install left the daprd binary (init refuses), self-heal with uninstall+reinit.
	@if ! docker ps --format '{{.Names}}' | grep -qE '^dapr_scheduler$$'; then \
		echo "Initializing Dapr runtime $(DAPR_RUNTIME_VERSION)..."; \
		ok=""; \
		for a in 1 2 3; do \
			if dapr init --runtime-version $(DAPR_RUNTIME_VERSION); then ok=1; break; fi; \
			echo "dapr init attempt $$a failed (often a transient Docker Hub pull timeout) — cleaning up and retrying..."; \
			dapr uninstall --all >/dev/null 2>&1 || true; \
			sleep $$((a * 10)); \
		done; \
		[ -n "$$ok" ] || { echo "ERROR: dapr init failed after 3 attempts"; exit 1; }; \
	fi

#e2e: @ End-to-end test: run the workflow through a real Dapr sidecar
e2e: build e2e-deps postgres-start
	@bash e2e/e2e-test.sh; rc=$$?; $(MAKE) --no-print-directory postgres-stop; exit $$rc

#build: @ Build linux/amd64 binary to ./cmd/main
build: deps
	@export GOFLAGS=$(GOFLAGS); CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ./cmd/main ./cmd/main.go

#run: @ Run the app via the Dapr sidecar
run: deps
	@./run-dapr.sh

#postgres-start: @ Start a local PostgreSQL container (state store)
postgres-start:
	@./run-postgres.sh

#postgres-stop: @ Stop the local PostgreSQL container
postgres-stop:
	@docker rm -f sample-postgres 2>/dev/null || true

#image-build: @ Build the container image
image-build:
	@docker buildx build --load -t $(IMAGE_NAME):$(IMAGE_TAG) -t $(IMAGE_NAME):latest .

#image-run: @ Run the container image locally (needs a Dapr sidecar to be healthy)
image-run: image-stop
	@docker run --rm -p $(APP_PORT):$(APP_PORT) -e APP_PORT=$(APP_PORT) --name $(APP_NAME) $(IMAGE_NAME):$(IMAGE_TAG)

#image-stop: @ Stop the local container
image-stop:
	@docker stop $(APP_NAME) 2>/dev/null || true

#image-push: @ Push the container image to the registry
image-push: image-build
	@if [ -n "$$GH_ACCESS_TOKEN" ] && echo "$(IMAGE_REGISTRY)" | grep -q "ghcr.io"; then \
		echo "$$GH_ACCESS_TOKEN" | docker login ghcr.io -u "$(REGISTRY_OWNER)" --password-stdin; \
	fi
	@docker push $(IMAGE_NAME):$(IMAGE_TAG)
	@docker push $(IMAGE_NAME):latest

#ci: @ Full local CI pipeline (mirrors GitHub Actions)
ci: deps format static-check test coverage-check build
	@echo "Local CI pipeline passed."

#ci-run: @ Run the GitHub Actions workflow locally via act
ci-run: deps
	@docker container prune -f 2>/dev/null || true
	@evt=$$(mktemp /tmp/act-push-event.XXXXXX.json); \
	 printf '{"ref":"refs/heads/main","repository":{"default_branch":"main","name":"$(APP_NAME)","full_name":"$(REGISTRY_OWNER)/$(APP_NAME)"}}' > "$$evt"; \
	 ACT_PORT=$$(shuf -i 40000-59999 -n 1); \
	 ARTIFACT_PATH=$$(mktemp -d -t act-artifacts.XXXXXX); \
	 rc=0; \
	 for j in static-check build test; do \
		echo "==== act push --job $$j ===="; \
		act push --job $$j --container-architecture linux/amd64 --pull=false \
			--eventpath "$$evt" \
			--artifact-server-port "$$ACT_PORT" \
			--artifact-server-path "$$ARTIFACT_PATH" || { rc=1; break; }; \
	 done; \
	 rm -f "$$evt"; exit $$rc

#release: @ Create and push a new release tag (vN.N.N)
release:
	@bash -c 'read -p "New tag (current: $(CURRENTTAG)): " newtag && \
		echo "$$newtag" | grep -qE "^v[0-9]+\.[0-9]+\.[0-9]+$$" || { echo "Error: Tag must match vN.N.N"; exit 1; } && \
		echo -n "Create and push $$newtag? [y/N] " && read ans && [ "$${ans:-N}" = y ] && \
		echo $$newtag > ./version.txt && \
		git add -A && \
		git commit -a -s -m "Cut $$newtag release" && \
		git tag -a -m "Cut $$newtag release" $$newtag && \
		git push origin $$newtag && \
		git push && \
		echo "Done."'

#version: @ Print the current version (git tag)
version:
	@echo $(CURRENTTAG)

.PHONY: help deps deps-check check-go-alignment clean get update format lint lint-ci \
	vulncheck secrets trivy-fs mermaid-lint static-check test coverage-check e2e-deps e2e build run \
	postgres-start postgres-stop image-build image-run image-stop image-push \
	ci ci-run release version
