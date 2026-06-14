#!/usr/bin/env bash
# End-to-end test: runs the app under a real Dapr sidecar, schedules the
# PostgresSQLDatabasesPut workflow over the REST API, polls until it reaches a
# terminal state, and asserts the recipe output. Requires Dapr to be initialized
# (placement + scheduler + a state store) and PostgreSQL to be reachable — the
# Makefile `e2e` target wires both up.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR/.."

# Committed defaults, then optional .env override.
# shellcheck source=/dev/null
if [ -f .env.example ]; then set -a; . ./.env.example; set +a; fi
# shellcheck source=/dev/null
if [ -f .env ]; then set -a; . ./.env; set +a; fi

APP_HOST="${APP_HOST:-localhost}"
APP_PORT="${APP_PORT:-7999}"
DAPR_APP_ID="${DAPR_APP_ID:-sample}"
DAPR_GRPC_PORT="${DAPR_GRPC_PORT:-50001}"
E2E_READY_TIMEOUT_SECONDS="${E2E_READY_TIMEOUT_SECONDS:-60}"
E2E_WORKFLOW_TIMEOUT_SECONDS="${E2E_WORKFLOW_TIMEOUT_SECONDS:-90}"
E2E_POLL_INTERVAL_SECONDS="${E2E_POLL_INTERVAL_SECONDS:-2}"
BASE_URL="http://${APP_HOST}:${APP_PORT}"

LOG=$(mktemp -t dapr-e2e.XXXXXX.log)

# Clean up the Dapr-launched app on any exit. `dapr stop` gracefully terminates
# the app + its sidecar; the kill is a fallback. Registered BEFORE the app
# starts so a failure during readiness still tears down (Makefile safety rule).
DAPR_RUN_PID=""
cleanup() {
  dapr stop --app-id "$DAPR_APP_ID" >/dev/null 2>&1 || true
  [ -n "$DAPR_RUN_PID" ] && kill "$DAPR_RUN_PID" 2>/dev/null || true
  rm -f "$LOG"
}
trap cleanup EXIT INT TERM

echo "==> Launching app under Dapr sidecar (app-id=$DAPR_APP_ID, port=$APP_PORT)"
dapr run --app-id "$DAPR_APP_ID" --app-port "$APP_PORT" \
  --resources-path components --config components/config.yaml \
  --dapr-grpc-port "$DAPR_GRPC_PORT" -- ./cmd/main >"$LOG" 2>&1 &
DAPR_RUN_PID=$!

echo "==> Waiting for /healthz (up to ${E2E_READY_TIMEOUT_SECONDS}s)"
ready=""
deadline=$(( $(date +%s) + E2E_READY_TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  if curl -sf --max-time 2 "${BASE_URL}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [ -z "$ready" ]; then
  echo "FAIL: app did not become healthy within ${E2E_READY_TIMEOUT_SECONDS}s"
  tail -40 "$LOG" || true
  exit 1
fi

echo "==> Scheduling PostgresSQLDatabasesPut"
PUT=$(curl -s --fail-with-body -X PUT "${BASE_URL}/workflows" \
  -H 'Content-Type: application/json' \
  -d '{"name":"PostgresSQLDatabasesPut","input":{"resource":{"name":"sample"},"runtime":{"kubernetes":{"namespace":"default"}}}}')
ID=$(jq -r '.id' <<<"$PUT")
if [ -z "$ID" ] || [ "$ID" = "null" ]; then
  echo "FAIL: could not schedule workflow; response: $PUT"
  exit 1
fi
echo "    instance id: $ID"

echo "==> Polling for terminal status (up to ${E2E_WORKFLOW_TIMEOUT_SECONDS}s)"
FINAL=""
deadline=$(( $(date +%s) + E2E_WORKFLOW_TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  R=$(curl -s "${BASE_URL}/workflows/${ID}")
  ST=$(jq -r '.runtimeStatus' <<<"$R")
  echo "    runtimeStatus=$ST"
  case "$ST" in COMPLETED|FAILED|TERMINATED|CANCELED) FINAL="$R"; break;; esac
  sleep "$E2E_POLL_INTERVAL_SECONDS"
done

if [ -z "$FINAL" ]; then
  echo "FAIL: workflow did not reach a terminal state within ${E2E_WORKFLOW_TIMEOUT_SECONDS}s"
  tail -40 "$LOG" || true
  exit 1
fi

STATUS=$(jq -r '.runtimeStatus' <<<"$FINAL")
if [ "$STATUS" != "COMPLETED" ]; then
  echo "FAIL: workflow ended as $STATUS (expected COMPLETED)"
  echo "$FINAL" | jq .
  tail -40 "$LOG" || true
  exit 1
fi

# Assert the recipe output shape: values (database/host/port/username),
# secrets (password/uri), and exactly two provisioned resources.
if ! jq -e '
  (.output | fromjson) as $o
  | ($o.values.username == "pguser")
  and ($o.values.port == 5432)
  and (($o.values.database // "") | startswith("sample_"))
  and (($o.secrets.password // "") | length > 0)
  and (($o.secrets.uri // "") | startswith("postgresql://"))
  and (($o.resources | length) == 2)
' <<<"$FINAL" >/dev/null; then
  echo "FAIL: workflow output did not match the expected recipe shape"
  echo "$FINAL" | jq '.output | fromjson'
  exit 1
fi

echo "==> E2E PASSED — workflow COMPLETED with the expected recipe output"
echo "$FINAL" | jq '.output | fromjson'
