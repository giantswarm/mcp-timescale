package timescale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors returned by the catalog helpers.
var (
	// ErrNoTimescaleDB is returned by TimescaleDB-specific lookups against a
	// database without the extension.
	ErrNoTimescaleDB = errors.New("the timescaledb extension is not installed in this database")
	// ErrNotFound is returned when a schema, table or hypertable does not
	// exist (or is not visible to the configured role).
	ErrNotFound = errors.New("not found")
	// ErrNoDatabases is returned by the registry when nothing is configured.
	ErrNoDatabases = errors.New("no databases are configured")
)

// SQLSTATE codes worth naming.
const (
	sqlstateQueryCanceled = "57014" // statement_timeout hit
	sqlstateReadOnlySQLTx = "25006" // write attempted in a read-only transaction
)

// Error classes with more than one producer.
const (
	classTimeout = "timeout"
	classConnect = "connect"
)

// Class maps an error to a short, low-cardinality label for audit logs and
// metrics: "" for nil, "timeout", "canceled", "connect", "not_found",
// "no_timescaledb", "sql_error:<SQLSTATE>" or "internal".
func Class(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return classTimeout
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrNoTimescaleDB):
		return "no_timescaledb"
	case errors.Is(err, ErrNoDatabases):
		return "no_databases"
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return classConnect
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == sqlstateQueryCanceled {
			return classTimeout
		}
		return "sql_error:" + pgErr.Code
	}
	if pgconn.Timeout(err) {
		return classTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return classConnect
	}
	return "internal"
}

// Describe renders an error for the tool caller. PostgreSQL errors keep
// their SQLSTATE, message, detail and hint; connection errors never echo the
// connection string (pgx redacts the password, but the string still carries
// host and user, and one sentence is enough).
func Describe(dbName string, err error) string {
	if err == nil {
		return ""
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		inner := errors.Unwrap(connErr)
		if inner == nil {
			return fmt.Sprintf("cannot connect to database %s", dbName)
		}
		var pgErr *pgconn.PgError
		if errors.As(inner, &pgErr) {
			return fmt.Sprintf("cannot connect to database %s: %s", dbName, pgMessage(pgErr))
		}
		return fmt.Sprintf("cannot connect to database %s: %s", dbName, trimDial(inner.Error()))
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlstateQueryCanceled:
			return "query canceled: the statement timeout was exceeded (" + pgMessage(pgErr) + ")"
		case sqlstateReadOnlySQLTx:
			return "refused: this server only runs read-only transactions (" + pgMessage(pgErr) + ")"
		}
		return pgMessage(pgErr)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out waiting for database " + dbName
	}
	return err.Error()
}

func pgMessage(e *pgconn.PgError) string {
	var b strings.Builder
	b.WriteString("SQLSTATE ")
	b.WriteString(e.Code)
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Detail != "" {
		b.WriteString(" (detail: ")
		b.WriteString(e.Detail)
		b.WriteString(")")
	}
	if e.Hint != "" {
		b.WriteString(" (hint: ")
		b.WriteString(e.Hint)
		b.WriteString(")")
	}
	if e.Position > 0 {
		fmt.Fprintf(&b, " (at character %d)", e.Position)
	}
	return b.String()
}

// trimDial strips the "dial tcp host:port:" prefix Go's net package adds so
// the caller sees the reason ("connection refused", "i/o timeout", "no such
// host") without the address.
func trimDial(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 && strings.HasPrefix(msg, "dial ") {
		return msg[i+2:]
	}
	return msg
}
