#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
cd "$SCRIPT_DIR"

# Load committed defaults from .env.example, then an optional .env override.
# shellcheck source=/dev/null
if [ -f .env.example ]; then set -a; . ./.env.example; set +a; fi
# shellcheck source=/dev/null
if [ -f .env ]; then set -a; . ./.env; set +a; fi

POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-daprrulz}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_DB="${POSTGRES_DB:-sample_state}"
POSTGRES_STATE_IMAGE="${POSTGRES_STATE_IMAGE:-postgres:18-alpine}"

echo "Starting database..."

# Idempotent: remove any prior container so repeated runs (e.g. make e2e) don't
# collide on the fixed container name.
docker rm -f sample-postgres >/dev/null 2>&1 || true

docker run \
  --name sample-postgres \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  -p "$POSTGRES_PORT":5432 \
  -v "$SCRIPT_DIR/db/init-db.sh:/docker-entrypoint-initdb.d/init-db.sh" \
  --rm \
  -d \
  "$POSTGRES_STATE_IMAGE"

# Wait until the init scripts have run and the target DB accepts connections —
# daprd's state.postgresql component pings the DB on startup and fatals if it
# isn't ready yet (connection reset during boot).
echo "Waiting for PostgreSQL ($POSTGRES_DB) to be ready..."
ready=""
for _ in $(seq 1 "${POSTGRES_READY_TIMEOUT_SECONDS:-30}"); do
  if docker exec sample-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c 'SELECT 1' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ -z "$ready" ]; then
  echo "Error: PostgreSQL did not become ready in time" >&2
  docker logs --tail 30 sample-postgres >&2 2>/dev/null || true
  exit 1
fi

echo "Database started successfully."
echo "Connect: psql -h localhost -U $POSTGRES_USER -p $POSTGRES_PORT -d $POSTGRES_DB"
