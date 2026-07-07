package activities

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

// pgAdmin performs the privileged PostgreSQL provisioning operations a recipe
// needs. It is a small interface (accept-interfaces-return-structs) so activity
// functions can be unit-tested against a fake without a live database.
type pgAdmin interface {
	CreateRole(ctx context.Context, username, password string) error
	CreateDatabase(ctx context.Context, database, owner string) error
	Close(ctx context.Context) error
}

// newPGAdmin constructs a pgAdmin connected to the admin endpoint. It is a
// package-level variable so tests can substitute a fake (hermetic unit tests,
// no network); production code never reassigns it.
var newPGAdmin = dialPGAdmin

// connectTimeout bounds the admin connection dial (env-tunable).
func connectTimeout() time.Duration {
	return time.Duration(atoiOr(env("PG_CONNECT_TIMEOUT_SECONDS", "10"), 10)) * time.Second
}

// pgxAdmin is the real pgx-backed pgAdmin.
type pgxAdmin struct {
	conn     *pgx.Conn
	endpoint PGEndpoint
}

func dialPGAdmin(ctx context.Context, ep PGEndpoint) (pgAdmin, error) {
	dctx, cancel := context.WithTimeout(ctx, connectTimeout())
	defer cancel()

	conn, err := pgx.Connect(dctx, adminDSN(ep))
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres admin endpoint %s:%s: %w", ep.Host, ep.Port, err)
	}
	return &pgxAdmin{conn: conn, endpoint: ep}, nil
}

// ConnectionURI builds the postgresql:// URI advertised to recipe consumers.
// url.URL escapes every component, so a resource-derived database name (or any
// value) containing URL-special characters (?, #, @, /, whitespace) cannot
// corrupt the connection string or inject spurious query parameters.
func ConnectionURI(user, password, host, port, database string) string {
	u := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + database,
	}
	return u.String()
}

// adminDSN builds a libpq URL. url.UserPassword escapes credentials safely, so
// a generated password with URL-special characters cannot corrupt the DSN.
func adminDSN(ep PGEndpoint) string {
	u := url.URL{
		Scheme: postgresName,
		User:   url.UserPassword(ep.User, ep.Password),
		Host:   net.JoinHostPort(ep.Host, ep.Port),
		Path:   "/" + ep.Database,
	}
	q := u.Query()
	q.Set("sslmode", env("PG_SSLMODE", "disable"))
	u.RawQuery = q.Encode()
	return u.String()
}

func (a *pgxAdmin) Close(ctx context.Context) error { return a.conn.Close(ctx) }

// CreateRole creates a LOGIN role idempotently. Identifiers are sanitized via
// pgx.Identifier; the password literal is single-quote escaped (DDL cannot be
// parameterized). CREATE ROLE errors if the role exists, so we check first.
func (a *pgxAdmin) CreateRole(ctx context.Context, username, password string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout())
	defer cancel()

	var exists bool
	if err := a.conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", username,
	).Scan(&exists); err != nil {
		return fmt.Errorf("checking role %q: %w", username, err)
	}
	if exists {
		// Reset the password so the returned credentials are valid.
		_, err := a.conn.Exec(ctx, fmt.Sprintf(
			"ALTER ROLE %s WITH LOGIN PASSWORD %s",
			pgx.Identifier{username}.Sanitize(), quoteLiteral(password)))
		if err != nil {
			return fmt.Errorf("altering role %q: %w", username, err)
		}
		return nil
	}
	_, err := a.conn.Exec(ctx, fmt.Sprintf(
		"CREATE ROLE %s WITH LOGIN PASSWORD %s",
		pgx.Identifier{username}.Sanitize(), quoteLiteral(password)))
	if err != nil {
		return fmt.Errorf("creating role %q: %w", username, err)
	}
	return nil
}

// CreateDatabase creates the database owned by owner (idempotent).
func (a *pgxAdmin) CreateDatabase(ctx context.Context, database, owner string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout())
	defer cancel()

	var exists bool
	if err := a.conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", database,
	).Scan(&exists); err != nil {
		return fmt.Errorf("checking database %q: %w", database, err)
	}
	if exists {
		return nil
	}
	// CREATE DATABASE cannot run inside a transaction block; conn.Exec runs it
	// in autocommit mode.
	_, err := a.conn.Exec(ctx, fmt.Sprintf(
		"CREATE DATABASE %s OWNER %s",
		pgx.Identifier{database}.Sanitize(), pgx.Identifier{owner}.Sanitize()))
	if err != nil {
		return fmt.Errorf("creating database %q: %w", database, err)
	}
	return nil
}

// quoteLiteral single-quotes a string literal for inline DDL, escaping embedded
// quotes and backslashes. Used only for the password in CREATE/ALTER ROLE, which
// cannot be parameterized.
func quoteLiteral(s string) string {
	escaped := ""
	for _, r := range s {
		switch r {
		case '\'':
			escaped += "''"
		case '\\':
			escaped += `\\`
		default:
			escaped += string(r)
		}
	}
	// E'' escaping is only needed when backslashes are present; prefix with E to
	// be safe with the doubled backslash above.
	return "E'" + escaped + "'"
}
