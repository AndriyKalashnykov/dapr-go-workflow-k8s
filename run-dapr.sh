#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR"

# Load committed defaults from .env.example (source of truth), then an optional
# .env override. `set -a` exports everything so the dapr CLI inherits them.
# shellcheck source=/dev/null
if [ -f .env.example ]; then set -a; . ./.env.example; set +a; fi
# shellcheck source=/dev/null
if [ -f .env ]; then set -a; . ./.env; set +a; fi

APP_ID="${DAPR_APP_ID:-sample}"
APP_PORT="${APP_PORT:-7999}"
DAPR_GRPC_PORT="${DAPR_GRPC_PORT:-50001}"

dapr run \
  --app-id "$APP_ID" \
  --app-port "$APP_PORT" \
  --config "$SCRIPT_DIR/components/config.yaml" \
  --resources-path "$SCRIPT_DIR/components" \
  --dapr-grpc-port "$DAPR_GRPC_PORT" \
  -- go run ./cmd/main.go
