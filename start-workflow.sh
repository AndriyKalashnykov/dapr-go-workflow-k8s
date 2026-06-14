#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR"

# Load committed defaults from .env.example, then an optional .env override.
# shellcheck source=/dev/null
if [ -f .env.example ]; then set -a; . ./.env.example; set +a; fi
# shellcheck source=/dev/null
if [ -f .env ]; then set -a; . ./.env; set +a; fi

APP_HOST="${APP_HOST:-localhost}"
APP_PORT="${APP_PORT:-7999}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-3}"
BASE_URL="http://${APP_HOST}:${APP_PORT}"

echo "Checking server health..."
curl --silent --fail "${BASE_URL}/healthz"
echo ""
echo ""

echo "Starting workflow..."
# The workflow name MUST match the registered Go function name
# (workflows.PostgresSQLDatabasesPut). The input is a recipes.Context.
RESULT=$(curl "${BASE_URL}/workflows" \
  --silent \
  --fail-with-body \
  -X PUT \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "PostgresSQLDatabasesPut",
    "input": {
      "resource": { "name": "sample" },
      "runtime": { "kubernetes": { "namespace": "default" } }
    }
  }')

echo "$RESULT"
echo ""

ID=$(jq -r '.id' <<<"$RESULT")
echo "Started workflow with id: $ID"

# GET /workflows/{id} returns a WorkflowStatus DTO whose `runtimeStatus` is a
# human-readable string (RUNNING / COMPLETED / FAILED / CANCELED / TERMINATED).
while true; do
  echo "Checking workflow status..."

  RESULT=$(curl "${BASE_URL}/workflows/${ID}" \
    --silent \
    --fail-with-body \
    -X GET)
  echo "$RESULT"
  echo ""

  STATUS=$(jq -r '.runtimeStatus' <<<"$RESULT")
  case "$STATUS" in
    COMPLETED)  echo "Workflow completed!";  break ;;
    FAILED)     echo "Workflow failed!";     break ;;
    CANCELED)   echo "Workflow canceled!";   break ;;
    TERMINATED) echo "Workflow terminated!"; break ;;
  esac

  sleep "$POLL_INTERVAL_SECONDS"
done
