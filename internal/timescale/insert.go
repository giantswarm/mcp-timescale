package timescale

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// columnRe is the column name shape Insert accepts; the name is quoted as
// an identifier as well.
var columnRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ErrInsertNotAllowed is returned for a table outside the database's insert
// allowlist, and for a database without an insert path.
var ErrInsertNotAllowed = fmt.Errorf("insert not allowed")

// InsertResult is the shape of timescale_insert_row results.
type InsertResult struct {
	Database   string   `json:"database"`
	Table      string   `json:"table"`
	Columns    []string `json:"columns"`
	Inserted   int64    `json:"inserted"`
	DurationMS int64    `json:"duration_ms"`
}

// Insert writes one row into an allowlisted table through the insert role,
// in its own committed transaction attributed to the caller. PostgreSQL
// converts the JSON values to the column types (json_populate_record), so
// timestamps, numerics, arrays and json columns take their usual text or
// JSON forms; columns left out keep their defaults. The statement text is
// built from the allowlisted table and validated column names only; every
// value travels as the single bind parameter.
func (d *Database) Insert(ctx context.Context, caller, table string, row map[string]any) (*InsertResult, error) {
	if d.insert == nil {
		return nil, fmt.Errorf("%w: database %s has no insert path", ErrInsertNotAllowed, d.cfg.Name)
	}
	if !slices.Contains(d.cfg.Insert.Tables, table) {
		return nil, fmt.Errorf("%w: table %q is not writable in database %s (writable: %s)", ErrInsertNotAllowed, table, d.cfg.Name, strings.Join(d.cfg.Insert.Tables, ", "))
	}
	if len(row) == 0 {
		return nil, fmt.Errorf("%w: row has no columns", ErrInsertNotAllowed)
	}
	cols := make([]string, 0, len(row))
	for c := range row {
		if !columnRe.MatchString(c) {
			return nil, fmt.Errorf("%w: column name %q must match %s", ErrInsertNotAllowed, c, columnRe.String())
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)
	doc, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("encode row: %w", err)
	}

	schema, name, _ := strings.Cut(table, ".")
	ident := pgx.Identifier{schema, name}.Sanitize()
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	list := strings.Join(quoted, ", ")
	sql := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM json_populate_record(NULL::%s, $1::json)", ident, list, list, ident)

	pool, err := d.insert.get(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.StatementTimeout+sessionSlack)
	defer cancel()

	res := &InsertResult{Database: d.cfg.Name, Table: table, Columns: cols}
	start := time.Now()
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('application_name', $1, true)", applicationName(caller)); err != nil {
			return fmt.Errorf("attribute session: %w", err)
		}
		tag, err := tx.Exec(ctx, sql, string(doc))
		if err != nil {
			return err
		}
		res.Inserted = tag.RowsAffected()
		return nil
	})
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}
	return res, nil
}
