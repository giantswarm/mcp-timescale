package timescale

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// maxApplicationName is PostgreSQL's NAMEDATALEN-1: longer values are
// truncated by the server with a NOTICE, so we truncate ourselves.
const maxApplicationName = 63

// sessionSlack is added to the statement timeout as the deadline for a
// whole call (acquire + setup + query), so a hung network path cannot hold
// a handler forever.
const sessionSlack = 10 * time.Second

// applicationName builds "mcp-timescale/<caller>" truncated to 63 bytes on a
// UTF-8 boundary.
func applicationName(caller string) string {
	if caller == "" {
		caller = "anonymous"
	}
	s := ApplicationName + "/" + caller
	for len(s) > maxApplicationName {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// withTx runs fn inside a READ ONLY, READ COMMITTED transaction whose
// application_name identifies the caller. timeout, when > 0, becomes the
// statement_timeout for this transaction only (SET LOCAL semantics via
// set_config). The transaction is always rolled back: nothing it does may
// persist.
func (d *Database) withTx(ctx context.Context, caller string, timeout time.Duration, fn func(ctx context.Context, tx pgx.Tx) error) error {
	pool, err := d.Pool(ctx)
	if err != nil {
		return err
	}

	deadline := d.cfg.StatementTimeout + sessionSlack
	if timeout > 0 && timeout < d.cfg.StatementTimeout {
		deadline = timeout + sessionSlack
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// application_name cannot take a bind parameter in SET; set_config with
	// is_local=true is the parameterized equivalent of SET LOCAL.
	if _, err := tx.Exec(ctx, "SELECT set_config('application_name', $1, true)", applicationName(caller)); err != nil {
		return fmt.Errorf("attribute session: %w", err)
	}
	if timeout > 0 {
		ms := strconv.FormatInt(timeout.Milliseconds(), 10)
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", ms); err != nil {
			return fmt.Errorf("set statement timeout: %w", err)
		}
	}
	return fn(ctx, tx)
}

// execExtended runs a statement without arguments through the extended
// protocol. pgx switches argument-less Exec to the simple protocol, which
// would accept several statements in one string; Query always prepares, so
// PostgreSQL rejects anything but a single statement.
func execExtended(ctx context.Context, tx pgx.Tx, sql string) error {
	rows, err := tx.Query(ctx, sql, userQueryMode)
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}
