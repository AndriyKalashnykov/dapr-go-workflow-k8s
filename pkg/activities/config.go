package activities

import (
	"os"
	"strconv"
	"time"
)

// opTimeout bounds an individual DDL statement (CREATE/DROP ROLE/DATABASE) so a
// statement blocked on a lock cannot hang the activity indefinitely. Env-tunable
// via PG_OP_TIMEOUT_SECONDS.
func opTimeout() time.Duration {
	return time.Duration(atoiOr(env("PG_OP_TIMEOUT_SECONDS", "30"), 30)) * time.Second
}

// postgresName is the literal "postgres" used, by PostgreSQL convention, as the
// default superuser, the maintenance database, the connection URL scheme, and
// the binding Service's port name. Single-sourced so the value is not repeated.
const postgresName = "postgres"

// atoiOr parses s as an int, returning fallback on any parse error.
func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return fallback
}

// env returns the value of the environment variable named by key, or fallback
// when it is unset or empty. Mirrors the os.LookupEnv-with-default pattern used
// across the project (see .env.example / Makefile `?=` defaults).
func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// PGEndpoint is a reachable PostgreSQL endpoint. The admin endpoint (used to run
// provisioning DDL) is read from PG_ADMIN_* with sensible dev defaults that fall
// back to the local POSTGRES_* dev container (see .env.example). The recipe
// advertises this same endpoint to consumers, so the returned connection URI is
// genuinely connectable.
type PGEndpoint struct {
	Host     string
	Port     string
	User     string
	Password string
	// Database is the maintenance database the admin connection targets
	// (e.g. "postgres"); provisioned databases are created on the same server.
	Database string
}

// adminEndpoint resolves the PostgreSQL admin endpoint from the environment.
// PG_ADMIN_* take precedence; each falls back to the POSTGRES_* dev defaults so
// a bare `make run` / `make e2e` works against the local dev container.
func adminEndpoint() PGEndpoint {
	return PGEndpoint{
		Host:     env("PG_ADMIN_HOST", env("POSTGRES_HOST", "localhost")),
		Port:     env("PG_ADMIN_PORT", env("POSTGRES_PORT", "5432")),
		User:     env("PG_ADMIN_USER", env("POSTGRES_USER", postgresName)),
		Password: env("PG_ADMIN_PASSWORD", env("POSTGRES_PASSWORD", "daprrulz")),
		Database: env("PG_ADMIN_DB", postgresName),
	}
}
