package activities

import (
	"os"
	"strconv"
	"time"
)

// env returns the value of the environment variable named by key, or fallback
// when it is unset or empty.
func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// atoiOr parses s as an int, returning fallback on any parse error.
func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return fallback
}

// opTimeout bounds an individual DDL statement (CREATE/DROP ROLE/DATABASE) so a
// statement blocked on a lock cannot hang the activity indefinitely. Env-tunable
// via PG_OP_TIMEOUT_SECONDS.
func opTimeout() time.Duration {
	return time.Duration(atoiOr(env("PG_OP_TIMEOUT_SECONDS", "30"), 30)) * time.Second
}

// workloadImage is the PostgreSQL image the recipe deploys as the database
// workload. Renovate-tracked via the .go customManager in renovate.json.
func workloadImage() string {
	// renovate: datasource=docker depName=postgres
	const defaultImage = "postgres:18-alpine"
	return env("POSTGRES_WORKLOAD_IMAGE", defaultImage)
}

// workloadRolloutTimeout bounds how long DeployPostgres waits for the deployed
// Postgres to become ready. Env-tunable via PG_WORKLOAD_ROLLOUT_TIMEOUT_SECONDS.
func workloadRolloutTimeout() time.Duration {
	return time.Duration(atoiOr(env("PG_WORKLOAD_ROLLOUT_TIMEOUT_SECONDS", "120"), 120)) * time.Second
}

// postgresName is the literal "postgres" used, by PostgreSQL convention, as the
// superuser, the connection URL scheme, the Service port name, and the workload
// label. Single-sourced so the value is not repeated.
const postgresName = "postgres"

// PGEndpoint is a reachable PostgreSQL admin endpoint used to run provisioning
// DDL. In the workload-deploy model it is built from the deployed workload's
// node-reachable NodePort address and superuser credentials, not from static env.
type PGEndpoint struct {
	Host     string
	Port     string
	User     string
	Password string
	// Database is the maintenance database the admin connection targets.
	Database string
}
