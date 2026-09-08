package timescale

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giantswarm/mcp-timescale/internal/sqlguard"
)

// userQueryMode runs caller-supplied statements (and the cursor FETCH whose
// result shape changes with every statement) through the extended protocol
// with an unnamed prepared statement that is described on every call. The
// default statement cache would key the FETCH on its text and reuse a stale
// column description ("bind message has N result formats but query has M
// columns"), and caching one-off user statements only fills the cache.
// Parse still refuses more than one statement per string.
const userQueryMode = pgx.QueryExecModeDescribeExec

// cursorName is the transaction-scoped cursor every SELECT-like statement
// runs through so only max_rows+1 rows ever leave the server.
const cursorName = "mcp_timescale_cursor"

// Column describes one result column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// QueryResult is the shape of timescale_query and timescale_sample_rows
// results.
type QueryResult struct {
	Database   string   `json:"database"`
	Columns    []Column `json:"columns"`
	Rows       [][]any  `json:"rows"`
	RowCount   int      `json:"row_count"`
	Truncated  bool     `json:"truncated"`
	DurationMS int64    `json:"duration_ms"`
}

// ExplainResult is the shape of timescale_explain results. Plan is a string
// for FORMAT TEXT and the parsed JSON document for FORMAT JSON.
type ExplainResult struct {
	Database   string `json:"database"`
	Format     string `json:"format"`
	Analyzed   bool   `json:"analyzed"`
	Plan       any    `json:"plan"`
	DurationMS int64  `json:"duration_ms"`
}

// Query runs an already classified statement and returns at most maxRows
// rows (clamped to the database's maxRows), reporting whether more existed.
// SELECT-like statements are fetched through a NO SCROLL cursor so the
// server never streams more than maxRows+1 rows; EXPLAIN and SHOW run
// directly (their output is small).
func (d *Database) Query(ctx context.Context, caller string, stmt sqlguard.Statement, maxRows int, timeout time.Duration) (*QueryResult, error) {
	maxRows = d.clampRows(maxRows)
	res := &QueryResult{Database: d.cfg.Name, Columns: []Column{}, Rows: [][]any{}}
	start := time.Now()
	err := d.withTx(ctx, caller, timeout, func(ctx context.Context, tx pgx.Tx) error {
		var (
			rows pgx.Rows
			err  error
		)
		if stmt.Explainable() {
			if err := execExtended(ctx, tx, "DECLARE "+cursorName+" NO SCROLL CURSOR FOR "+stmt.SQL); err != nil {
				return err
			}
			rows, err = tx.Query(ctx, fmt.Sprintf("FETCH FORWARD %d FROM %s", maxRows+1, cursorName), userQueryMode)
		} else {
			rows, err = tx.Query(ctx, stmt.SQL, userQueryMode)
		}
		if err != nil {
			return err
		}
		cols, data, truncated, err := collectRows(ctx, tx, rows, maxRows)
		if err != nil {
			return err
		}
		res.Columns, res.Rows, res.Truncated = cols, data, truncated
		return nil
	})
	res.DurationMS = time.Since(start).Milliseconds()
	res.RowCount = len(res.Rows)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Explain runs EXPLAIN on a SELECT-like statement. With analyze the
// statement is executed (inside the read-only transaction) and BUFFERS is
// reported as well.
func (d *Database) Explain(ctx context.Context, caller string, stmt sqlguard.Statement, analyze bool, format string, timeout time.Duration) (*ExplainResult, error) {
	if !stmt.Explainable() {
		return nil, fmt.Errorf("%w: only SELECT, WITH ... SELECT, VALUES and TABLE statements can be explained", sqlguard.ErrRejected)
	}
	format = strings.ToLower(format)
	if format != "text" && format != "json" {
		return nil, fmt.Errorf("format must be text or json, got %q", format)
	}
	res := &ExplainResult{Database: d.cfg.Name, Format: format, Analyzed: analyze}
	start := time.Now()
	err := d.withTx(ctx, caller, timeout, func(ctx context.Context, tx pgx.Tx) error {
		sql := fmt.Sprintf("EXPLAIN (ANALYZE %t, BUFFERS %t, FORMAT %s) %s", analyze, analyze, strings.ToUpper(format), stmt.SQL)
		rows, err := tx.Query(ctx, sql, userQueryMode)
		if err != nil {
			return err
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			if len(vals) == 0 {
				continue
			}
			switch v := vals[0].(type) {
			case string:
				lines = append(lines, v)
			case []byte:
				lines = append(lines, string(v))
			default:
				// FORMAT JSON: pgx decodes json into Go values already.
				res.Plan = encodeValue(v, "")
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if res.Plan == nil {
			text := strings.Join(lines, "\n")
			if format == "json" {
				var doc any
				if err := json.Unmarshal([]byte(text), &doc); err != nil {
					return fmt.Errorf("decode plan: %w", err)
				}
				res.Plan = doc
			} else {
				res.Plan = text
			}
		}
		return nil
	})
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}
	return res, nil
}

// clampRows bounds a requested row count to [1, cfg.MaxRows]; 0 or less
// means the database maximum.
func (d *Database) clampRows(n int) int {
	if n <= 0 || n > d.cfg.MaxRows {
		return d.cfg.MaxRows
	}
	return n
}

// collectRows drains rows into encoded values, stopping after maxRows+1
// rows. It returns the columns, at most maxRows rows and whether more rows
// existed. Result column types unknown to pgx are resolved through
// pg_catalog.format_type inside the same transaction.
func collectRows(ctx context.Context, tx pgx.Tx, rows pgx.Rows, maxRows int) ([]Column, [][]any, bool, error) {
	defer rows.Close()

	// FieldDescriptions() is backed by a buffer the connection reuses for
	// the next query, so copy what we need before anything else runs.
	fds := rows.FieldDescriptions()
	tm := tx.Conn().TypeMap()
	cols := make([]Column, len(fds))
	oids := make([]uint32, len(fds))
	unknown := map[uint32]struct{}{}
	for i, fd := range fds {
		oids[i] = fd.DataTypeOID
		cols[i] = Column{Name: fd.Name, Type: typeName(tm, fd.DataTypeOID)}
		if cols[i].Type == "" {
			unknown[fd.DataTypeOID] = struct{}{}
		}
	}

	data := make([][]any, 0, min(maxRows, 1024))
	truncated := false
	for rows.Next() {
		if len(data) >= maxRows {
			truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return nil, nil, false, fmt.Errorf("read row: %w", err)
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = encodeValue(v, cols[i].Type)
		}
		data = append(data, row)
	}
	// Close before rows.Err() and before running the follow-up query on
	// the same connection.
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}

	if len(unknown) > 0 {
		names, err := resolveTypeNames(ctx, tx, unknown)
		if err != nil {
			return nil, nil, false, err
		}
		for i := range cols {
			if cols[i].Type == "" {
				cols[i].Type = names[oids[i]]
			}
		}
	}
	return cols, data, truncated, nil
}

// typeName returns pgx's name for a type OID, or "" when unknown.
func typeName(tm *pgtype.Map, oid uint32) string {
	if t, ok := tm.TypeForOID(oid); ok {
		return t.Name
	}
	return ""
}

// resolveTypeNames looks up type names for OIDs pgx does not know
// (user-defined enums, composite and domain types, extension types).
func resolveTypeNames(ctx context.Context, tx pgx.Tx, oids map[uint32]struct{}) (map[uint32]string, error) {
	list := make([]uint32, 0, len(oids))
	for oid := range oids {
		list = append(list, oid)
	}
	rows, err := tx.Query(ctx, "SELECT oid::bigint, format_type(oid, NULL) FROM pg_catalog.pg_type WHERE oid = ANY($1::oid[])", list)
	if err != nil {
		return nil, fmt.Errorf("resolve column types: %w", err)
	}
	defer rows.Close()
	out := make(map[uint32]string, len(list))
	for rows.Next() {
		var (
			oid  int64
			name string
		)
		if err := rows.Scan(&oid, &name); err != nil {
			return nil, err
		}
		out[uint32(oid)] = name // #nosec G115 -- OIDs are 32-bit unsigned by definition
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for oid := range oids {
		if _, ok := out[oid]; !ok {
			out[oid] = fmt.Sprintf("oid:%d", oid)
		}
	}
	return out, nil
}
