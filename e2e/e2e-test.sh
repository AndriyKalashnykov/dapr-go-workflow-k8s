#!/usr/bin/env bash
# End-to-end test of the REAL backends: runs the app under a real Dapr sidecar
# against a real PostgreSQL and a real Kubernetes cluster (kind), schedules the
# PostgresSQLDatabasesPut workflow, and asserts the recipe actually provisioned:
#   * a real Postgres role + database (the returned URI authenticates), and
#   * a real Kubernetes Service + Secret binding.
# It then runs PostgresSQLDatabasesDelete and asserts everything is torn down.
#
# Requires: dapr (initialized), docker, kubectl, kind, jq. The Makefile `e2e`
# target wires up the Dapr control plane + PostgreSQL; this script manages the
# kind cluster.
set -uo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR/.." || exit 1

# Committed defaults, then optional .env override.
# shellcheck source=/dev/null
if [ -f .env.example ]; then set -a; . ./.env.example; set +a; fi
# shellcheck source=/dev/null
if [ -f .env ]; then set -a; . ./.env; set +a; fi

APP_HOST="${APP_HOST:-localhost}"
APP_PORT="${APP_PORT:-7999}"
DAPR_APP_ID="${DAPR_APP_ID:-sample}"
DAPR_GRPC_PORT="${DAPR_GRPC_PORT:-50001}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
E2E_READY_TIMEOUT_SECONDS="${E2E_READY_TIMEOUT_SECONDS:-60}"
E2E_WORKFLOW_TIMEOUT_SECONDS="${E2E_WORKFLOW_TIMEOUT_SECONDS:-90}"
E2E_POLL_INTERVAL_SECONDS="${E2E_POLL_INTERVAL_SECONDS:-2}"
E2E_READY_POLL_INTERVAL_SECONDS="${E2E_READY_POLL_INTERVAL_SECONDS:-1}"
E2E_CURL_MAX_TIME_SECONDS="${E2E_CURL_MAX_TIME_SECONDS:-2}"
E2E_NAMESPACE="${E2E_NAMESPACE:-default}"
E2E_RESOURCE_NAME="${E2E_RESOURCE_NAME:-sample}"
E2E_KIND_CLUSTER="${E2E_KIND_CLUSTER:-dapr-go-workflow-k8s}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-sample-postgres}"
BASE_URL="http://${APP_HOST}:${APP_PORT}"

# The recipe activities provision against the PostgreSQL admin endpoint; default
# it to the local dev container this e2e starts.
export PG_ADMIN_HOST="${PG_ADMIN_HOST:-${POSTGRES_HOST:-localhost}}"
export PG_ADMIN_PORT="${PG_ADMIN_PORT:-$POSTGRES_PORT}"
export PG_ADMIN_USER="${PG_ADMIN_USER:-${POSTGRES_USER:-postgres}}"
export PG_ADMIN_PASSWORD="${PG_ADMIN_PASSWORD:-${POSTGRES_PASSWORD:-daprrulz}}"
export PG_ADMIN_DB="${PG_ADMIN_DB:-postgres}"

LOG=$(mktemp -t dapr-e2e.XXXXXX.log)
KCFG=$(mktemp -t dapr-e2e-kubeconfig.XXXXXX)
export KUBECONFIG="$KCFG"
COMP=$(mktemp -d -t dapr-e2e-components.XXXXXX)
DAPR_RUN_PID=""
KIND_CREATED=""

cleanup() {
  dapr stop --app-id "$DAPR_APP_ID" >/dev/null 2>&1 || true
  [ -n "$DAPR_RUN_PID" ] && kill "$DAPR_RUN_PID" 2>/dev/null || true
  if [ -n "$KIND_CREATED" ] && [ -z "${E2E_KIND_KEEP:-}" ]; then
    kind delete cluster --name "$E2E_KIND_CLUSTER" >/dev/null 2>&1 || true
  fi
  rm -f "$LOG" "$KCFG"
  rm -rf "$COMP"
}
trap cleanup EXIT INT TERM

fail() { echo "FAIL: $*"; [ -f "$LOG" ] && tail -40 "$LOG"; exit 1; }

# --- Kubernetes: ensure a kind cluster and a dedicated kubeconfig ---
echo "==> Ensuring kind cluster '$E2E_KIND_CLUSTER'"
if kind get clusters 2>/dev/null | grep -qx "$E2E_KIND_CLUSTER"; then
  echo "    reusing existing cluster"
else
  kind create cluster --name "$E2E_KIND_CLUSTER" >/dev/null 2>&1 || fail "kind create cluster"
  KIND_CREATED=1
fi
kind get kubeconfig --name "$E2E_KIND_CLUSTER" >"$KCFG" 2>/dev/null || fail "kind get kubeconfig"
kubectl --request-timeout=10s get ns "$E2E_NAMESPACE" >/dev/null 2>&1 || fail "namespace $E2E_NAMESPACE not reachable"

# --- Dapr components: render statestore with the effective POSTGRES_PORT ---
# Dapr component metadata has no {env:} substitution, so render at runtime.
sed "s/port=5432/port=${POSTGRES_PORT}/" components/statestore.yaml >"$COMP/statestore.yaml"
cp components/config.yaml "$COMP/config.yaml"

echo "==> Launching app under Dapr sidecar (app-id=$DAPR_APP_ID, port=$APP_PORT, kube-ctx=$(kubectl config current-context))"
dapr run --app-id "$DAPR_APP_ID" --app-port "$APP_PORT" \
  --resources-path "$COMP" --config "$COMP/config.yaml" \
  --dapr-grpc-port "$DAPR_GRPC_PORT" -- ./cmd/main >"$LOG" 2>&1 &
DAPR_RUN_PID=$!

echo "==> Waiting for /healthz (up to ${E2E_READY_TIMEOUT_SECONDS}s)"
ready=""
deadline=$(( $(date +%s) + E2E_READY_TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  if curl -sf --max-time "$E2E_CURL_MAX_TIME_SECONDS" "${BASE_URL}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep "$E2E_READY_POLL_INTERVAL_SECONDS"
done
[ -n "$ready" ] || fail "app did not become healthy within ${E2E_READY_TIMEOUT_SECONDS}s"

# --- Put: provision the database ---
echo "==> Scheduling PostgresSQLDatabasesPut"
PUT=$(curl -s --fail-with-body -X PUT "${BASE_URL}/workflows" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"PostgresSQLDatabasesPut\",\"input\":{\"resource\":{\"name\":\"${E2E_RESOURCE_NAME}\"},\"runtime\":{\"kubernetes\":{\"namespace\":\"${E2E_NAMESPACE}\"}}}}")
ID=$(jq -r '.id' <<<"$PUT")
[ -n "$ID" ] && [ "$ID" != null ] || fail "could not schedule workflow; response: $PUT"
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
[ -n "$FINAL" ] || fail "workflow did not reach a terminal state within ${E2E_WORKFLOW_TIMEOUT_SECONDS}s"
STATUS=$(jq -r '.runtimeStatus' <<<"$FINAL")
if [ "$STATUS" != COMPLETED ]; then
  echo "FAIL: workflow ended as $STATUS (expected COMPLETED); failureReason:"
  jq -r '.failureReason // "(none)"' <<<"$FINAL"
  tail -40 "$LOG" || true
  exit 1
fi

# Assert the recipe output shape.
if ! jq -e --arg port "$POSTGRES_PORT" --arg name "$E2E_RESOURCE_NAME" '
  (.output | fromjson) as $o
  | (($o.values.username // "") | startswith("pguser_"))
  and ($o.values.port == $port)
  and (($o.values.database // "") | startswith($name + "_"))
  and (($o.secrets.password // "") | length > 0)
  and (($o.secrets.uri // "") | startswith("postgresql://"))
  and (($o.resources | length) == 2)
' <<<"$FINAL" >/dev/null; then
  echo "FAIL: workflow output did not match the expected recipe shape"
  jq '.output | fromjson' <<<"$FINAL"
  exit 1
fi
echo "==> Recipe output shape OK"
jq '.output | fromjson' <<<"$FINAL"

DB=$(jq -r '.output|fromjson|.values.database' <<<"$FINAL")
USER=$(jq -r '.output|fromjson|.values.username' <<<"$FINAL")
PASS=$(jq -r '.output|fromjson|.secrets.password' <<<"$FINAL")
URI=$(jq -r '.output|fromjson|.secrets.uri' <<<"$FINAL")

echo "==> Verifying REAL PostgreSQL provisioning"
docker exec "$POSTGRES_CONTAINER" psql -U "$PG_ADMIN_USER" -tAc \
  "SELECT 1 FROM pg_roles WHERE rolname='$USER'" | grep -q 1 || fail "role $USER not found in postgres"
echo "    role $USER exists ✓"
docker exec "$POSTGRES_CONTAINER" psql -U "$PG_ADMIN_USER" -tAc \
  "SELECT 1 FROM pg_database WHERE datname='$DB'" | grep -q 1 || fail "database $DB not found in postgres"
echo "    database $DB exists ✓"
# The returned credentials must authenticate as the provisioned user into the
# provisioned db. Reconnect from inside the container (localhost:5432 there is
# the same server the URI advertises) using the exact returned user/password/db.
CURI="postgresql://${USER}:${PASS}@localhost:5432/${DB}"
docker exec "$POSTGRES_CONTAINER" psql "$CURI" -tAc "SELECT current_database()||'/'||current_user" \
  | grep -qx "$DB/$USER" || fail "returned credentials did not authenticate to $DB as $USER"
echo "    returned credentials authenticate ✓"

echo "==> Verifying REAL Kubernetes binding in ns/$E2E_NAMESPACE"
kubectl get service "$E2E_RESOURCE_NAME" -n "$E2E_NAMESPACE" >/dev/null 2>&1 || fail "Service not created"
echo "    Service $E2E_RESOURCE_NAME exists ✓"
SEC_URI=$(kubectl get secret "$E2E_RESOURCE_NAME" -n "$E2E_NAMESPACE" -o jsonpath='{.data.uri}' 2>/dev/null | base64 -d)
[ "$SEC_URI" = "$URI" ] || fail "Secret uri mismatch (got '$SEC_URI')"
echo "    Secret $E2E_RESOURCE_NAME carries the connection uri ✓"

# --- Delete: tear the database down ---
echo "==> Scheduling PostgresSQLDatabasesDelete"
DEL=$(curl -s --fail-with-body -X PUT "${BASE_URL}/workflows" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"PostgresSQLDatabasesDelete\",\"input\":{\"resource\":{\"name\":\"${E2E_RESOURCE_NAME}\",\"properties\":{\"status\":{\"binding\":{\"database\":\"${DB}\",\"username\":\"${USER}\"}}}},\"runtime\":{\"kubernetes\":{\"namespace\":\"${E2E_NAMESPACE}\"}}}}")
DID=$(jq -r '.id' <<<"$DEL")
[ -n "$DID" ] && [ "$DID" != null ] || fail "could not schedule delete; response: $DEL"
echo "    instance id: $DID"

DFINAL=""
deadline=$(( $(date +%s) + E2E_WORKFLOW_TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  ST=$(jq -r '.runtimeStatus' <<<"$(curl -s "${BASE_URL}/workflows/${DID}")")
  echo "    runtimeStatus=$ST"
  case "$ST" in COMPLETED|FAILED|TERMINATED|CANCELED) DFINAL="$ST"; break;; esac
  sleep "$E2E_POLL_INTERVAL_SECONDS"
done
[ "$DFINAL" = COMPLETED ] || fail "delete workflow ended as ${DFINAL:-<timeout>} (expected COMPLETED)"

echo "==> Verifying teardown"
docker exec "$POSTGRES_CONTAINER" psql -U "$PG_ADMIN_USER" -tAc \
  "SELECT 1 FROM pg_database WHERE datname='$DB'" | grep -q 1 && fail "database $DB still present" || echo "    database dropped ✓"
docker exec "$POSTGRES_CONTAINER" psql -U "$PG_ADMIN_USER" -tAc \
  "SELECT 1 FROM pg_roles WHERE rolname='$USER'" | grep -q 1 && fail "role $USER still present" || echo "    role dropped ✓"
kubectl get service "$E2E_RESOURCE_NAME" -n "$E2E_NAMESPACE" >/dev/null 2>&1 && fail "Service still present" || echo "    Service deleted ✓"
kubectl get secret "$E2E_RESOURCE_NAME" -n "$E2E_NAMESPACE" >/dev/null 2>&1 && fail "Secret still present" || echo "    Secret deleted ✓"

echo "==> E2E PASSED — real Postgres + real Kubernetes provisioning and teardown verified end-to-end"
