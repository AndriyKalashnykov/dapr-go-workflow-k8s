#!/usr/bin/env bash
# End-to-end test of the workload-deploy recipe: runs the app under a real Dapr
# sidecar, schedules PostgresSQLDatabasesPut, and asserts the recipe actually
# DEPLOYED a PostgreSQL workload (Deployment + Service + Secret) into a real kind
# cluster and provisioned a role + database on it (reachable via the Service's
# NodePort). It then re-Puts (idempotent) and finally PostgresSQLDatabasesDelete,
# asserting the workload is destroyed.
#
# Requires: dapr (initialized), docker, kubectl, kind, jq. The Makefile `e2e`
# target wires up the Dapr control plane + the state-store PostgreSQL; this script
# manages the kind cluster.
set -uo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR/.." || exit 1

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
E2E_WORKFLOW_TIMEOUT_SECONDS="${E2E_WORKFLOW_TIMEOUT_SECONDS:-150}"
E2E_POLL_INTERVAL_SECONDS="${E2E_POLL_INTERVAL_SECONDS:-2}"
E2E_READY_POLL_INTERVAL_SECONDS="${E2E_READY_POLL_INTERVAL_SECONDS:-1}"
E2E_CURL_MAX_TIME_SECONDS="${E2E_CURL_MAX_TIME_SECONDS:-2}"
E2E_NAMESPACE="${E2E_NAMESPACE:-default}"
E2E_RESOURCE_NAME="${E2E_RESOURCE_NAME:-sample}"
E2E_KIND_CLUSTER="${E2E_KIND_CLUSTER:-dapr-go-workflow-k8s}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-sample-postgres}"
POSTGRES_WORKLOAD_IMAGE="${POSTGRES_WORKLOAD_IMAGE:-postgres:18-alpine}"
export POSTGRES_WORKLOAD_IMAGE
BASE_URL="http://${APP_HOST}:${APP_PORT}"

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

# psql against the deployed instance via the node-reachable NodePort. --network
# host makes the throwaway client see the kind node IP exactly as the host does.
deployed_psql() { # $1=user $2=password $3=db $4=query
  docker run --rm --network host -e PGPASSWORD="$2" "$POSTGRES_WORKLOAD_IMAGE" \
    psql "postgresql://$1@${NODEIP}:${NODEPORT}/$3" -tAc "$4"
}

# --- Kubernetes: ensure a kind cluster + load the workload image ---
echo "==> Ensuring kind cluster '$E2E_KIND_CLUSTER'"
if kind get clusters 2>/dev/null | grep -qx "$E2E_KIND_CLUSTER"; then
  echo "    reusing existing cluster"
else
  kind create cluster --name "$E2E_KIND_CLUSTER" >/dev/null 2>&1 || fail "kind create cluster"
  KIND_CREATED=1
fi
kind get kubeconfig --name "$E2E_KIND_CLUSTER" >"$KCFG" 2>/dev/null || fail "kind get kubeconfig"
kubectl --request-timeout=10s get ns "$E2E_NAMESPACE" >/dev/null 2>&1 || fail "namespace $E2E_NAMESPACE not reachable"

# The deployed pod pulls $POSTGRES_WORKLOAD_IMAGE from its registry; the rollout
# wait (PG_WORKLOAD_ROLLOUT_TIMEOUT_SECONDS) accommodates the pull time.

# --- Dapr state-store components: render statestore with the effective port ---
sed "s/port=5432/port=${POSTGRES_PORT}/" components/statestore.yaml >"$COMP/statestore.yaml"
cp components/config.yaml "$COMP/config.yaml"

echo "==> Launching app under Dapr sidecar (app-id=$DAPR_APP_ID, kube-ctx=$(kubectl config current-context))"
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

# --- Put: deploy the workload + provision ---
echo "==> Scheduling PostgresSQLDatabasesPut (deploys a PostgreSQL workload)"
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
if [ "$(jq -r '.runtimeStatus' <<<"$FINAL")" != COMPLETED ]; then
  echo "FAIL: workflow ended as $(jq -r '.runtimeStatus' <<<"$FINAL"); failureReason:"
  jq -r '.failureReason // "(none)"' <<<"$FINAL"; tail -40 "$LOG" || true; exit 1
fi

# Assert the recipe output shape.
if ! jq -e --arg name "$E2E_RESOURCE_NAME" '
  (.output | fromjson) as $o
  | (($o.values.username // "") | startswith("pguser_"))
  and ($o.values.port == "5432")
  and (($o.values.host // "") | endswith(".svc.cluster.local"))
  and (($o.values.database // "") | startswith($name + "_"))
  and (($o.secrets.password // "") | length > 0)
  and (($o.secrets.uri // "") | startswith("postgresql://"))
  and (($o.resources | length) == 3)
' <<<"$FINAL" >/dev/null; then
  echo "FAIL: workflow output did not match the expected recipe shape"; jq '.output | fromjson' <<<"$FINAL"; exit 1
fi
echo "==> Recipe output shape OK"; jq '.output | fromjson' <<<"$FINAL"

DB=$(jq -r '.output|fromjson|.values.database' <<<"$FINAL")
USER=$(jq -r '.output|fromjson|.values.username' <<<"$FINAL")
PASS=$(jq -r '.output|fromjson|.secrets.password' <<<"$FINAL")

echo "==> Verifying the REAL PostgreSQL workload in ns/$E2E_NAMESPACE"
kubectl -n "$E2E_NAMESPACE" get deployment "$E2E_RESOURCE_NAME" >/dev/null 2>&1 || fail "Deployment not created"
[ "$(kubectl -n "$E2E_NAMESPACE" get deployment "$E2E_RESOURCE_NAME" -o jsonpath='{.status.readyReplicas}')" = "1" ] || fail "Deployment not ready"
echo "    Deployment $E2E_RESOURCE_NAME has a ready replica ✓"
kubectl -n "$E2E_NAMESPACE" get service "$E2E_RESOURCE_NAME" >/dev/null 2>&1 || fail "Service not created"
kubectl -n "$E2E_NAMESPACE" get secret "$E2E_RESOURCE_NAME" >/dev/null 2>&1 || fail "Secret not created"
echo "    Service + Secret exist ✓"

# Discover the node-reachable endpoint and verify the provisioned role + database.
NODEIP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
NODEPORT=$(kubectl -n "$E2E_NAMESPACE" get svc "$E2E_RESOURCE_NAME" -o jsonpath='{.spec.ports[0].nodePort}')
[ -n "$NODEIP" ] && [ -n "$NODEPORT" ] || fail "could not discover node endpoint"
echo "==> Verifying the provisioned role + database on the deployed instance ($NODEIP:$NODEPORT)"
deployed_psql "$USER" "$PASS" "$DB" "SELECT current_database()||'/'||current_user" | grep -qx "$DB/$USER" \
  || fail "the returned credentials did not authenticate to $DB as $USER on the deployed instance"
echo "    role $USER authenticates to database $DB on the deployed workload ✓"

# --- Idempotent re-Put ---
echo "==> Re-Put with the existing binding (idempotent update, reuses the instance)"
REPUT=$(curl -s --fail-with-body -X PUT "${BASE_URL}/workflows" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"PostgresSQLDatabasesPut\",\"input\":{\"resource\":{\"name\":\"${E2E_RESOURCE_NAME}\",\"properties\":{\"status\":{\"binding\":{\"database\":\"${DB}\",\"username\":\"${USER}\"}}}},\"runtime\":{\"kubernetes\":{\"namespace\":\"${E2E_NAMESPACE}\"}}}}")
RID2=$(jq -r '.id' <<<"$REPUT")
[ -n "$RID2" ] && [ "$RID2" != null ] || fail "could not schedule re-Put; response: $REPUT"
RFINAL=""
deadline=$(( $(date +%s) + E2E_WORKFLOW_TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  R=$(curl -s "${BASE_URL}/workflows/${RID2}")
  case "$(jq -r '.runtimeStatus' <<<"$R")" in COMPLETED|FAILED|TERMINATED|CANCELED) RFINAL="$R"; break;; esac
  sleep "$E2E_POLL_INTERVAL_SECONDS"
done
[ "$(jq -r '.runtimeStatus' <<<"$RFINAL")" = COMPLETED ] || fail "re-Put status=$(jq -r '.runtimeStatus' <<<"$RFINAL")"
REDB=$(jq -r '.output|fromjson|.values.database' <<<"$RFINAL")
REUSER=$(jq -r '.output|fromjson|.values.username' <<<"$RFINAL")
{ [ "$REDB" = "$DB" ] && [ "$REUSER" = "$USER" ]; } || fail "re-Put did not reuse the binding (got $REUSER/$REDB, want $USER/$DB)"
SUPERPW=$(kubectl -n "$E2E_NAMESPACE" get secret "$E2E_RESOURCE_NAME" -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d)
COUNT=$(deployed_psql postgres "$SUPERPW" postgres "SELECT count(*) FROM pg_database WHERE datname ~ '^${E2E_RESOURCE_NAME}_[0-9a-f]{8}\$'")
[ "$COUNT" = "1" ] || fail "expected exactly 1 provisioned database after re-Put, got $COUNT (orphan leak)"
echo "    re-Put reused $USER / $DB with no orphan (1 database on the instance) ✓"

# --- Delete: destroy the workload ---
echo "==> Scheduling PostgresSQLDatabasesDelete (destroys the workload)"
DEL=$(curl -s --fail-with-body -X PUT "${BASE_URL}/workflows" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"PostgresSQLDatabasesDelete\",\"input\":{\"resource\":{\"name\":\"${E2E_RESOURCE_NAME}\"},\"runtime\":{\"kubernetes\":{\"namespace\":\"${E2E_NAMESPACE}\"}}}}")
DID=$(jq -r '.id' <<<"$DEL")
[ -n "$DID" ] && [ "$DID" != null ] || fail "could not schedule delete; response: $DEL"
DFINAL=""
deadline=$(( $(date +%s) + E2E_WORKFLOW_TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  ST=$(jq -r '.runtimeStatus' <<<"$(curl -s "${BASE_URL}/workflows/${DID}")")
  echo "    runtimeStatus=$ST"
  case "$ST" in COMPLETED|FAILED|TERMINATED|CANCELED) DFINAL="$ST"; break;; esac
  sleep "$E2E_POLL_INTERVAL_SECONDS"
done
[ "$DFINAL" = COMPLETED ] || fail "delete workflow ended as ${DFINAL:-<timeout>} (expected COMPLETED)"

echo "==> Verifying the workload was destroyed"
kubectl -n "$E2E_NAMESPACE" get deployment "$E2E_RESOURCE_NAME" >/dev/null 2>&1 && fail "Deployment still present" || echo "    Deployment deleted ✓"
kubectl -n "$E2E_NAMESPACE" get service "$E2E_RESOURCE_NAME" >/dev/null 2>&1 && fail "Service still present" || echo "    Service deleted ✓"
kubectl -n "$E2E_NAMESPACE" get secret "$E2E_RESOURCE_NAME" >/dev/null 2>&1 && fail "Secret still present" || echo "    Secret deleted ✓"

echo "==> E2E PASSED — real PostgreSQL workload deployed, provisioned, reused, and destroyed"
