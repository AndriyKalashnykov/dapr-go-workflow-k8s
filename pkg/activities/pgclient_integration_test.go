//go:build integration

package activities

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// These tests exercise the REAL pgx-backed pgAdmin (dialPGAdmin / pgxAdmin)
// against a REAL PostgreSQL started via Testcontainers — proving the DDL, the
// idempotent CREATE-vs-ALTER role paths, and the quoteLiteral escaping against
// an actual PostgreSQL parser (things a fake pgAdmin cannot verify). They are
// gated behind the `integration` build tag (Docker required) so `make test` /
// `make ci` stay hermetic; run them with `make integration-test`.
//
// The Postgres image comes from the package's own workloadImage() helper — the
// single Renovate-tracked source of truth (its const in config.go + .env.example),
// so no untracked image literal is duplicated here. The container's host/port are
// dynamic (Testcontainers ephemeral mapped port), never a hardcoded 5432, so the
// test is parallel-safe.

// itSuperUser / itMaintenance reuse the "postgres" convention (superuser +
// maintenance DB); itSuperPass is a throwaway container password (gosec is
// excluded for _test.go in .golangci.yml).
const (
	itSuperUser   = postgresName
	itSuperPass   = "it-superuser-pw"
	itMaintenance = postgresName
)

// startPostgres launches a throwaway PostgreSQL and returns its superuser admin
// endpoint (host + dynamic mapped port). The container is terminated on cleanup.
func startPostgres(ctx context.Context, t *testing.T) PGEndpoint {
	t.Helper()

	pgC, err := postgres.Run(ctx, workloadImage(),
		postgres.WithDatabase(itMaintenance),
		postgres.WithUsername(itSuperUser),
		postgres.WithPassword(itSuperPass),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container (%s): %v", workloadImage(), err)
	}
	t.Cleanup(func() {
		if err := pgC.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	host, err := pgC.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	mapped, err := pgC.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container mapped port: %v", err)
	}
	return PGEndpoint{
		Host:     host,
		Port:     mapped.Port(),
		User:     itSuperUser,
		Password: itSuperPass,
		Database: itMaintenance,
	}
}

// dialAs opens a fresh pgx connection as (user/pass) to db on the same endpoint,
// pings, and closes it — returning any error. It proves credentials actually
// authenticate against the real server (the only true test of CreateRole and the
// quoteLiteral escaping).
func dialAs(ctx context.Context, base PGEndpoint, user, pass, db string) error {
	ep := base
	ep.User, ep.Password, ep.Database = user, pass, db
	conn, err := pgx.Connect(ctx, adminDSN(ep))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return conn.Ping(ctx)
}

func TestPgxAdminIntegration(t *testing.T) {
	ctx := context.Background()
	ep := startPostgres(ctx, t)

	admin, err := dialPGAdmin(ctx, ep)
	if err != nil {
		t.Fatalf("dialPGAdmin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })

	// A separate superuser connection for verification queries (owner lookups)
	// that the pgAdmin interface does not expose.
	super, err := pgx.Connect(ctx, adminDSN(ep))
	if err != nil {
		t.Fatalf("verification connect: %v", err)
	}
	t.Cleanup(func() { _ = super.Close(ctx) })

	t.Run("CreateRole new role authenticates", func(t *testing.T) {
		const role = "pguser_it_new"
		const pw = "role-pw-alpha-11111"
		if err := admin.CreateRole(ctx, role, pw); err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		if err := dialAs(ctx, ep, role, pw, itMaintenance); err != nil {
			t.Fatalf("newly-created role cannot authenticate: %v", err)
		}
	})

	t.Run("CreateRole again rotates password via ALTER path", func(t *testing.T) {
		const role = "pguser_it_rotate"
		const pw1 = "role-pw-first-11111"
		const pw2 = "role-pw-second-22222"
		if err := admin.CreateRole(ctx, role, pw1); err != nil {
			t.Fatalf("CreateRole (create): %v", err)
		}
		if err := dialAs(ctx, ep, role, pw1, itMaintenance); err != nil {
			t.Fatalf("first password does not authenticate: %v", err)
		}
		// Second call with a DIFFERENT password must take the existing-role ALTER
		// branch and rotate the password (not error on duplicate role).
		if err := admin.CreateRole(ctx, role, pw2); err != nil {
			t.Fatalf("CreateRole (rotate) errored instead of ALTERing: %v", err)
		}
		if err := dialAs(ctx, ep, role, pw2, itMaintenance); err != nil {
			t.Fatalf("rotated password does not authenticate: %v", err)
		}
		if err := dialAs(ctx, ep, role, pw1, itMaintenance); err == nil {
			t.Fatalf("stale password still authenticates after rotation (ALTER did not take)")
		}
	})

	t.Run("CreateDatabase creates a role-owned database, idempotently", func(t *testing.T) {
		const role = "pguser_it_dbowner"
		const pw = "role-pw-owner-33333"
		const db = "orders_it_9f3a1c"
		if err := admin.CreateRole(ctx, role, pw); err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		if err := admin.CreateDatabase(ctx, db, role); err != nil {
			t.Fatalf("CreateDatabase: %v", err)
		}

		// The created database must be owned by the role.
		var owner string
		if err := super.QueryRow(ctx,
			"SELECT pg_catalog.pg_get_userbyid(datdba) FROM pg_database WHERE datname = $1", db,
		).Scan(&owner); err != nil {
			t.Fatalf("owner lookup: %v", err)
		}
		if owner != role {
			t.Fatalf("database %q owner = %q, want %q", db, owner, role)
		}

		// The owner authenticates directly to its own database.
		if err := dialAs(ctx, ep, role, pw, db); err != nil {
			t.Fatalf("owner cannot connect to its database: %v", err)
		}

		// Second call is a no-op (database already exists), not an error.
		if err := admin.CreateDatabase(ctx, db, role); err != nil {
			t.Fatalf("second CreateDatabase should be a no-op, got: %v", err)
		}
	})

	t.Run("quoteLiteral escaping round-trips a password with ' and \\", func(t *testing.T) {
		const role = "pguser_it_quote"
		// A password containing a single quote AND a backslash — the exact input
		// quoteLiteral must escape for the inline CREATE ROLE ... PASSWORD literal.
		// If the escaping is wrong the CREATE ROLE fails or stores a corrupted
		// password, and authenticating with the original value fails.
		const tricky = `a'b\c d"e$f`
		if err := admin.CreateRole(ctx, role, tricky); err != nil {
			t.Fatalf("CreateRole with special-char password: %v", err)
		}
		if err := dialAs(ctx, ep, role, tricky, itMaintenance); err != nil {
			t.Fatalf("role with special-char password cannot authenticate — quoteLiteral escaping is wrong: %v", err)
		}
	})
}
