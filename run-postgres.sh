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

echo "Starting database..."

docker run \
  --name sample-postgres \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  -p "$POSTGRES_PORT":5432 \
  -v "$SCRIPT_DIR/db/init-db.sh:/docker-entrypoint-initdb.d/init-db.sh" \
  --rm \
  -d \
  postgres

echo "Database started successfully."
echo "Connect: psql -h localhost -U $POSTGRES_USER -p $POSTGRES_PORT -d $POSTGRES_DB"
