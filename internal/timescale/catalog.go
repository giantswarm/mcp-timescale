package timescale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// probeTimeout bounds the reachability check of list_databases.
const probeTimeout = 3 * time.Second

// Row is a result row keyed by column name. Catalog tools return rows as
// objects so an LLM client does not have to line up positions.
type Row = map[string]any

// relationKinds maps pg_class.relkind to the kind names the tools report.
var relationKinds = map[string]string{
	"r": "table",
	"p": "partitioned table",
	"v": "view",
	"m": "materialized view",
	"f": "foreign table",
}

// hiddenSchemaFilter excludes system and TimescaleDB-internal schemas
// (chunks live in _timescaledb_internal) unless a schema is named
// explicitly. The column expression is substituted in.
const hiddenSchemaFilter = `%[1]s NOT IN ('pg_catalog', 'information_schema') AND %[1]s NOT LIKE 'pg\_%%' AND %[1]s NOT LIKE '\_timescaledb\_%%'`

// ProbeResult is what list_databases learns about one database.
type ProbeResult struct {
	Reachable          bool   `json:"reachable"`
	Error              string `json:"error,omitempty"`
	ErrorClass         string `json:"error_class,omitempty"`
	PostgresVersion    string `json:"postgres_version,omitempty"`
	TimescaleDBVersion string `json:"timescaledb_version,omitempty"`
}

// Probe checks reachability with a short deadline and reports the server
// and extension versions.
func (d *Database) Probe(ctx context.Context, caller string) ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var res ProbeResult
	err := d.withTx(ctx, caller, probeTimeout, func(ctx context.Context, tx pgx.Tx) error {
		var ts *string
		if err := tx.QueryRow(ctx, "SELECT current_setting('server_version'), (SELECT extversion FROM pg_catalog.pg_extension WHERE extname = 'timescaledb')").Scan(&res.PostgresVersion, &ts); err != nil {
			return err
		}
		if ts != nil {
			res.TimescaleDBVersion = *ts
		}
		return nil
	})
	if err != nil {
		res.Error = Describe(d.cfg.Name, err)
		res.ErrorClass = Class(err)
		return res
	}
	res.Reachable = true
	return res
}

// Info is the result of timescale_get_database_info.
type Info struct {
	Database                  string `json:"database"`
	DBName                    string `json:"dbname"`
	Version                   string `json:"version"`
	PostgresVersion           string `json:"postgres_version"`
	TimescaleDBInstalled      bool   `json:"timescaledb_installed"`
	TimescaleDBVersion        string `json:"timescaledb_version,omitempty"`
	TimescaleDBLicense        string `json:"timescaledb_license,omitempty"`
	TimescaleDBInformation    bool   `json:"timescaledb_information_available"`
	CurrentUser               string `json:"current_user"`
	TransactionReadOnly       string `json:"transaction_read_only"`
	DatabaseSizeBytes         int64  `json:"database_size_bytes"`
	DatabaseSize              string `json:"database_size"`
	Timezone                  string `json:"timezone"`
	ServerTime                string `json:"server_time"`
	Extensions                []Row  `json:"extensions"`
	Hypertables               int64  `json:"hypertables"`
	ContinuousAggregates      int64  `json:"continuous_aggregates"`
	Jobs                      int64  `json:"jobs"`
	MaxRows                   int    `json:"max_rows"`
	StatementTimeoutSeconds   int64  `json:"statement_timeout_seconds"`
	AllowedGroupsAndUsersOnly bool   `json:"restricted_to_allowlist"`
}

// Info collects server, extension and TimescaleDB summary facts.
func (d *Database) Info(ctx context.Context, caller string) (*Info, error) {
	info := &Info{Database: d.cfg.Name, MaxRows: d.cfg.MaxRows, StatementTimeoutSeconds: int64(d.cfg.StatementTimeout / time.Second), AllowedGroupsAndUsersOnly: d.cfg.Restricted()}
	err := d.withTx(ctx, caller, 0, func(ctx context.Context, tx pgx.Tx) error {
		var serverTime time.Time
		err := tx.QueryRow(ctx, `SELECT current_database(), version(), current_setting('server_version'), current_user,
			current_setting('transaction_read_only'), pg_database_size(current_database()),
			pg_size_pretty(pg_database_size(current_database())), current_setting('TimeZone'), now()`).
			Scan(&info.DBName, &info.Version, &info.PostgresVersion, &info.CurrentUser, &info.TransactionReadOnly,
				&info.DatabaseSizeBytes, &info.DatabaseSize, &info.Timezone, &serverTime)
		if err != nil {
			return err
		}
		info.ServerTime = serverTime.Format(time.RFC3339Nano)

		info.Extensions, err = queryRows(ctx, tx, "SELECT extname AS name, extversion AS version FROM pg_catalog.pg_extension ORDER BY extname")
		if err != nil {
			return err
		}
		version, installed, err := timescaleVersion(ctx, tx)
		if err != nil {
			return err
		}
		info.TimescaleDBInstalled, info.TimescaleDBVersion = installed, version
		if !installed {
			return nil
		}
		var license *string
		if err := tx.QueryRow(ctx, "SELECT current_setting('timescaledb.license', true)").Scan(&license); err != nil {
			return err
		}
		if license != nil {
			info.TimescaleDBLicense = *license
		}
		if err := tx.QueryRow(ctx, "SELECT to_regclass('timescaledb_information.hypertables') IS NOT NULL").Scan(&info.TimescaleDBInformation); err != nil {
			return err
		}
		if !info.TimescaleDBInformation {
			return nil
		}
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM timescaledb_information.hypertables),
			(SELECT count(*) FROM timescaledb_information.continuous_aggregates),
			(SELECT count(*) FROM timescaledb_information.jobs)`).
			Scan(&info.Hypertables, &info.ContinuousAggregates, &info.Jobs)
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// Schemas lists non-system schemas with owner and relation count.
func (d *Database) Schemas(ctx context.Context, caller string) ([]Row, error) {
	return d.rows(ctx, caller, fmt.Sprintf(`SELECT n.nspname AS name, pg_get_userbyid(n.nspowner) AS owner,
		(SELECT count(*) FROM pg_catalog.pg_class c WHERE c.relnamespace = n.oid AND c.relkind IN ('r','p','v','m','f')) AS relations,
		obj_description(n.oid, 'pg_namespace') AS comment
		FROM pg_catalog.pg_namespace n WHERE `+hiddenSchemaFilter+` ORDER BY n.nspname`, "n.nspname"))
}

// SizeMode says whether a catalog listing carries total sizes.
type SizeMode int

const (
	// SizesAuto includes sizes when the page holds fewer than SizeThreshold
	// relations and skips them otherwise.
	SizesAuto SizeMode = iota
	// SizesAlways sizes every relation of the page.
	SizesAlways
	// SizesNever lists without sizes.
	SizesNever
)

// SizeThreshold is the page size from which SizesAuto stops computing
// sizes: sizing means stat()-ing every chunk file, and a large catalog
// can spend the whole statement timeout on it.
const SizeThreshold = 50

// DefaultListLimit bounds a listing page when the caller names no limit.
const DefaultListLimit = 200

// ListOptions bounds and shapes a catalog listing.
type ListOptions struct {
	// Schema restricts the listing to one schema; "" lists every visible
	// schema (Tables skips the hidden ones then).
	Schema string
	// IncludeViews adds views and materialized views to Tables.
	IncludeViews bool
	// Limit and Offset page the listing, which is ordered by schema, name.
	Limit  int
	Offset int
	Sizes  SizeMode
}

// Listing is one page of a catalog listing.
type Listing struct {
	Items []Row
	// TotalCount is the number of relations the listing matched before
	// paging.
	TotalCount int
	// SizesIncluded reports whether the rows carry total_bytes/total_size.
	SizesIncluded bool
	// SizesError is set when SizesAuto wanted sizes but computing them
	// failed (for example on the statement timeout); the listing itself is
	// intact and SizesIncluded is false.
	SizesError error
}

// Tables lists tables and optionally views. With no schema the hidden
// schemas are skipped; a named schema is listed even when internal.
func (d *Database) Tables(ctx context.Context, caller string, opts ListOptions) (*Listing, error) {
	kinds := "('r','p','f')"
	if opts.IncludeViews {
		kinds = "('r','p','v','m','f')"
	}
	return d.listTS(ctx, caller, func(ctx context.Context, tx pgx.Tx, ts bool) (*Listing, error) {
		var where string
		var args []any
		if opts.Schema == "" {
			where = fmt.Sprintf(hiddenSchemaFilter, "n.nspname")
		} else {
			where = "n.nspname = $1"
			args = append(args, opts.Schema)
		}
		joins, tsCols := "", "false AS is_hypertable, false AS is_continuous_aggregate,"
		if ts {
			joins = `LEFT JOIN timescaledb_information.hypertables h ON h.hypertable_schema = n.nspname AND h.hypertable_name = c.relname
				LEFT JOIN timescaledb_information.continuous_aggregates ca ON ca.view_schema = n.nspname AND ca.view_name = c.relname`
			tsCols = "h.hypertable_name IS NOT NULL AS is_hypertable, ca.view_name IS NOT NULL AS is_continuous_aggregate, h.num_chunks, h.compression_enabled,"
		}
		from := fmt.Sprintf(`FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN %s AND %s`, kinds, where)
		listSQL := fmt.Sprintf(`SELECT n.nspname AS schema, c.relname AS name, c.relkind::text AS relkind,
			pg_get_userbyid(c.relowner) AS owner, c.reltuples::bigint AS row_estimate, %s
			obj_description(c.oid, 'pg_class') AS comment
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace %s
			WHERE c.relkind IN %s AND %s ORDER BY n.nspname, c.relname`, tsCols, joins, kinds, where)
		l, err := page(ctx, tx, ts, opts, "SELECT count(*) "+from, listSQL, args)
		if err != nil {
			return nil, err
		}
		for _, r := range l.Items {
			if rk, ok := r["relkind"].(string); ok {
				r["kind"] = relationKinds[rk]
				delete(r, "relkind")
			}
		}
		return l, nil
	})
}

// page runs one catalog listing: the count, the ordered page (LIMIT/OFFSET
// are appended to listSQL) and, when opts ask for them, the sizes of the
// page's relations in one set-based statement.
func page(ctx context.Context, tx pgx.Tx, ts bool, opts ListOptions, countSQL, listSQL string, args []any) (*Listing, error) {
	limit, offset := opts.Limit, opts.Offset
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if offset < 0 {
		offset = 0
	}
	l := &Listing{Items: []Row{}}
	if err := tx.QueryRow(ctx, countSQL, args...).Scan(&l.TotalCount); err != nil {
		return nil, err
	}
	n := len(args)
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := queryRows(ctx, tx, fmt.Sprintf("%s LIMIT $%d OFFSET $%d", listSQL, n+1, n+2), pageArgs...)
	if err != nil {
		return nil, err
	}
	l.Items = rows
	switch {
	case opts.Sizes == SizesNever:
		return l, nil
	case opts.Sizes == SizesAuto && len(rows) >= SizeThreshold:
		return l, nil
	}
	l.SizesIncluded = true
	if len(rows) == 0 {
		return l, nil
	}
	if err := attachSizes(ctx, tx, ts, rows); err != nil {
		if opts.Sizes == SizesAlways {
			return nil, err
		}
		l.SizesIncluded, l.SizesError = false, err
	}
	return l, nil
}

// attachSizes adds total_bytes and total_size to rows (keyed by schema and
// name) from one statement over the page. Hypertables are sized like
// hypertable_size() does — the relation plus every chunk, compressed data
// included — but for the whole page at once from TimescaleDB's per-chunk
// size view instead of one function call per row.
func attachSizes(ctx context.Context, tx pgx.Tx, ts bool, rows []Row) error {
	schemas, names := make([]string, 0, len(rows)), make([]string, 0, len(rows))
	byKey := make(map[string]Row, len(rows))
	for _, r := range rows {
		s, _ := r["schema"].(string)
		n, _ := r["name"].(string)
		schemas, names = append(schemas, s), append(names, n)
		byKey[s+"\x00"+n] = r
	}
	view := ""
	if ts {
		var err error
		if view, err = chunkSizeView(ctx, tx); err != nil {
			return err
		}
	}
	sizes, err := queryRows(ctx, tx, sizesSQL(ts, view), schemas, names)
	if err != nil {
		return err
	}
	for _, s := range sizes {
		schema, _ := s["schema"].(string)
		name, _ := s["name"].(string)
		if r, ok := byKey[schema+"\x00"+name]; ok {
			r["total_bytes"], r["total_size"] = s["total_bytes"], s["total_size"]
		}
	}
	return nil
}

// chunkSizeViews are the names TimescaleDB has given its per-chunk size
// view (the one hypertable_size() itself reads), newest first.
var chunkSizeViews = []string{"_timescaledb_internal.hypertable_chunk_local_size", "_timescaledb_functions.hypertable_chunk_local_size"}

// chunkSizeView returns the installed per-chunk size view, or "" when this
// TimescaleDB has none (sizes then fall back to hypertable_size per row).
func chunkSizeView(ctx context.Context, tx pgx.Tx) (string, error) {
	for _, v := range chunkSizeViews {
		var present bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", v).Scan(&present); err != nil {
			return "", err
		}
		if present {
			return v, nil
		}
	}
	return "", nil
}

// sizesSQL builds the set-based size statement for a page of relations
// given as parallel arrays $1 (schemas) and $2 (names).
func sizesSQL(ts bool, chunkView string) string {
	from := `FROM unnest($1::text[], $2::text[]) AS p(schema, name)
		JOIN pg_catalog.pg_namespace n ON n.nspname = p.schema::name
		JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = p.name::name`
	with, expr := "", "pg_total_relation_size(c.oid)"
	switch {
	case chunkView != "":
		// One pass over the page's chunks; MATERIALIZED keeps the planner
		// from re-running the aggregate for every relation.
		with = fmt.Sprintf(`WITH chunks AS MATERIALIZED (
			SELECT v.hypertable_schema AS schema, v.hypertable_name AS name, sum(v.total_bytes + v.compressed_total_size)::bigint AS bytes
			FROM unnest($1::text[], $2::text[]) AS p(schema, name)
			JOIN %s v ON v.hypertable_schema = p.schema::name AND v.hypertable_name = p.name::name
			GROUP BY 1, 2) `, chunkView)
		from += " LEFT JOIN chunks ch ON ch.schema = n.nspname AND ch.name = c.relname"
		expr = "pg_total_relation_size(c.oid) + COALESCE(ch.bytes, 0)"
	case ts:
		from += " LEFT JOIN timescaledb_information.hypertables h ON h.hypertable_schema = n.nspname AND h.hypertable_name = c.relname"
		expr = "CASE WHEN h.hypertable_name IS NOT NULL THEN hypertable_size(c.oid) ELSE pg_total_relation_size(c.oid) END"
	}
	return fmt.Sprintf(`%sSELECT schema, name, total_bytes, pg_size_pretty(total_bytes) AS total_size
		FROM (SELECT p.schema, p.name, %s AS total_bytes %s) s`, with, expr, from)
}

// TableDescription is the result of timescale_describe_table.
type TableDescription struct {
	Database    string  `json:"database"`
	Schema      string  `json:"schema"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Owner       string  `json:"owner"`
	Comment     *string `json:"comment"`
	RowEstimate int64   `json:"row_estimate"`
	Columns     []Row   `json:"columns"`
	PrimaryKey  *Row    `json:"primary_key"`
	Indexes     []Row   `json:"indexes"`
	ForeignKeys []Row   `json:"foreign_keys"`
	// ViewDefinition is set for plain views and materialized views.
	ViewDefinition *string `json:"view_definition,omitempty"`
	// Hypertable is set when the relation is a TimescaleDB hypertable.
	Hypertable *HypertableDetails `json:"hypertable,omitempty"`
	// ContinuousAggregate is set when the relation is a continuous
	// aggregate view.
	ContinuousAggregate *Row `json:"continuous_aggregate,omitempty"`
}

// HypertableDetails describes the TimescaleDB side of a hypertable.
type HypertableDetails struct {
	Dimensions          []Row `json:"dimensions"`
	NumChunks           any   `json:"num_chunks"`
	CompressionEnabled  any   `json:"compression_enabled"`
	CompressionSettings *Row  `json:"compression_settings"`
	Size                *Row  `json:"size"`
	Policies            []Row `json:"policies"`
}

// DescribeTable returns columns, keys, indexes and TimescaleDB details.
func (d *Database) DescribeTable(ctx context.Context, caller, schema, table string) (*TableDescription, error) {
	desc := &TableDescription{Database: d.cfg.Name, Schema: schema, Name: table, Columns: []Row{}, Indexes: []Row{}, ForeignKeys: []Row{}}
	err := d.withTx(ctx, caller, 0, func(ctx context.Context, tx pgx.Tx) error {
		rel, err := lookupRelation(ctx, tx, schema, table)
		if err != nil {
			return err
		}
		desc.Kind, desc.Owner, desc.Comment, desc.RowEstimate = rel.kind, rel.owner, rel.comment, rel.rowEstimate

		desc.Columns, err = queryRows(ctx, tx, `SELECT a.attname AS name, format_type(a.atttypid, a.atttypmod) AS type,
			NOT a.attnotnull AS nullable, pg_get_expr(ad.adbin, ad.adrelid) AS "default", col_description(a.attrelid, a.attnum) AS comment
			FROM pg_catalog.pg_attribute a LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
			WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, rel.oid)
		if err != nil {
			return err
		}
		pk, err := queryRows(ctx, tx, `SELECT conname AS name, pg_get_constraintdef(oid) AS definition FROM pg_catalog.pg_constraint WHERE conrelid = $1 AND contype = 'p'`, rel.oid)
		if err != nil {
			return err
		}
		if len(pk) > 0 {
			desc.PrimaryKey = &pk[0]
		}
		desc.Indexes, err = queryRows(ctx, tx, `SELECT i.relname AS name, pg_get_indexdef(i.oid) AS definition, ix.indisunique AS "unique", ix.indisprimary AS primary
			FROM pg_catalog.pg_index ix JOIN pg_catalog.pg_class i ON i.oid = ix.indexrelid WHERE ix.indrelid = $1 ORDER BY i.relname`, rel.oid)
		if err != nil {
			return err
		}
		desc.ForeignKeys, err = queryRows(ctx, tx, `SELECT conname AS name, pg_get_constraintdef(oid) AS definition FROM pg_catalog.pg_constraint WHERE conrelid = $1 AND contype = 'f' ORDER BY conname`, rel.oid)
		if err != nil {
			return err
		}
		if rel.relkind == "v" || rel.relkind == "m" {
			var def string
			if err := tx.QueryRow(ctx, "SELECT pg_get_viewdef($1::oid, true)", rel.oid).Scan(&def); err != nil {
				return err
			}
			desc.ViewDefinition = &def
		}

		_, ts, err := timescaleVersion(ctx, tx)
		if err != nil || !ts {
			return err
		}
		regclass := pgx.Identifier{schema, table}.Sanitize()

		ht, err := queryRows(ctx, tx, `SELECT num_chunks, compression_enabled FROM timescaledb_information.hypertables WHERE hypertable_schema = $1 AND hypertable_name = $2`, schema, table)
		if err != nil {
			return err
		}
		if len(ht) == 1 {
			h := &HypertableDetails{NumChunks: ht[0]["num_chunks"], CompressionEnabled: ht[0]["compression_enabled"], Policies: []Row{}}
			h.Dimensions, err = queryRows(ctx, tx, `SELECT dimension_number, column_name, column_type::text AS column_type, dimension_type,
				time_interval::text AS time_interval, integer_interval, integer_now_func, num_partitions
				FROM timescaledb_information.dimensions WHERE hypertable_schema = $1 AND hypertable_name = $2 ORDER BY dimension_number`, schema, table)
			if err != nil {
				return err
			}
			size, err := queryRows(ctx, tx, `SELECT table_bytes, index_bytes, toast_bytes, total_bytes, pg_size_pretty(total_bytes) AS total_size FROM hypertable_detailed_size($1::regclass)`, regclass)
			if err != nil {
				return err
			}
			if len(size) > 0 {
				h.Size = &size[0]
			}
			var hasCompressionView bool
			if err := tx.QueryRow(ctx, "SELECT to_regclass('timescaledb_information.hypertable_compression_settings') IS NOT NULL").Scan(&hasCompressionView); err != nil {
				return err
			}
			if hasCompressionView {
				cs, err := queryRows(ctx, tx, `SELECT segmentby, orderby, compress_interval_length FROM timescaledb_information.hypertable_compression_settings WHERE hypertable = $1::regclass`, regclass)
				if err != nil {
					return err
				}
				if len(cs) > 0 {
					h.CompressionSettings = &cs[0]
				}
			}
			h.Policies, err = queryRows(ctx, tx, `SELECT job_id, application_name, proc_name, schedule_interval::text AS schedule_interval, scheduled, config
				FROM timescaledb_information.jobs WHERE hypertable_schema = $1 AND hypertable_name = $2 ORDER BY job_id`, schema, table)
			if err != nil {
				return err
			}
			desc.Hypertable = h
		}

		cagg, err := queryRows(ctx, tx, `SELECT ca.view_definition, ca.materialized_only, ca.compression_enabled,
			ca.hypertable_schema AS source_hypertable_schema, ca.hypertable_name AS source_hypertable_name,
			ca.materialization_hypertable_schema, ca.materialization_hypertable_name,
			j.job_id AS refresh_job_id, j.schedule_interval::text AS refresh_schedule_interval, j.config AS refresh_config, j.scheduled AS refresh_scheduled
			FROM timescaledb_information.continuous_aggregates ca
			LEFT JOIN timescaledb_information.jobs j ON j.proc_name = 'policy_refresh_continuous_aggregate'
				AND j.hypertable_schema = ca.view_schema AND j.hypertable_name = ca.view_name
			WHERE ca.view_schema = $1 AND ca.view_name = $2`, schema, table)
		if err != nil {
			return err
		}
		if len(cagg) > 0 {
			desc.ContinuousAggregate = &cagg[0]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return desc, nil
}

// Hypertables lists hypertables with chunk count, compression, the primary
// (time) dimension and, per opts, their size.
func (d *Database) Hypertables(ctx context.Context, caller string, opts ListOptions) (*Listing, error) {
	return d.listTS(ctx, caller, func(ctx context.Context, tx pgx.Tx, ts bool) (*Listing, error) {
		if !ts {
			return nil, ErrNoTimescaleDB
		}
		where := ""
		var args []any
		if opts.Schema != "" {
			where = " WHERE h.hypertable_schema = $1"
			args = append(args, opts.Schema)
		}
		return page(ctx, tx, ts, opts, "SELECT count(*) FROM timescaledb_information.hypertables h"+where, `SELECT h.hypertable_schema AS schema, h.hypertable_name AS name, h.owner, h.num_dimensions, h.num_chunks, h.compression_enabled,
			d.column_name AS time_column, d.column_type::text AS time_column_type, d.time_interval::text AS chunk_time_interval, d.integer_interval AS chunk_integer_interval,
			CASE WHEN ca.view_name IS NOT NULL THEN format('%I.%I', ca.view_schema, ca.view_name) END AS materializes_continuous_aggregate
			FROM timescaledb_information.hypertables h
			LEFT JOIN timescaledb_information.dimensions d ON d.hypertable_schema = h.hypertable_schema AND d.hypertable_name = h.hypertable_name AND d.dimension_number = 1
			LEFT JOIN timescaledb_information.continuous_aggregates ca ON ca.materialization_hypertable_schema = h.hypertable_schema AND ca.materialization_hypertable_name = h.hypertable_name`+
			where+" ORDER BY h.hypertable_schema, h.hypertable_name", args)
	})
}

// Chunks lists the chunks of one hypertable with their ranges and sizes.
func (d *Database) Chunks(ctx context.Context, caller, schema, hypertable string, limit int, newestFirst bool) ([]Row, error) {
	if limit <= 0 {
		limit = 50
	}
	return d.rowsTS(ctx, caller, func(ctx context.Context, tx pgx.Tx, ts bool) ([]Row, error) {
		if !ts {
			return nil, ErrNoTimescaleDB
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM timescaledb_information.hypertables WHERE hypertable_schema = $1 AND hypertable_name = $2)", schema, hypertable).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("hypertable %s.%s: %w", schema, hypertable, ErrNotFound)
		}
		order := "DESC NULLS LAST"
		if !newestFirst {
			order = "ASC NULLS LAST"
		}
		return queryRows(ctx, tx, fmt.Sprintf(`SELECT c.chunk_schema, c.chunk_name, c.primary_dimension, c.primary_dimension_type::text AS primary_dimension_type,
			c.range_start, c.range_end, c.range_start_integer, c.range_end_integer, c.is_compressed, c.chunk_tablespace, c.chunk_creation_time,
			s.total_bytes, pg_size_pretty(s.total_bytes) AS total_size, s.table_bytes, s.index_bytes, s.toast_bytes
			FROM timescaledb_information.chunks c
			LEFT JOIN chunks_detailed_size($1::regclass) s ON s.chunk_schema = c.chunk_schema AND s.chunk_name = c.chunk_name
			WHERE c.hypertable_schema = $2 AND c.hypertable_name = $3
			ORDER BY c.range_start %s, c.range_start_integer %s, c.chunk_name LIMIT $4`, order, order),
			pgx.Identifier{schema, hypertable}.Sanitize(), schema, hypertable, limit)
	})
}

// ContinuousAggregates lists continuous aggregates with their refresh policy.
func (d *Database) ContinuousAggregates(ctx context.Context, caller string) ([]Row, error) {
	return d.rowsTS(ctx, caller, func(ctx context.Context, tx pgx.Tx, ts bool) ([]Row, error) {
		if !ts {
			return nil, ErrNoTimescaleDB
		}
		return queryRows(ctx, tx, `SELECT ca.view_schema, ca.view_name, ca.view_owner, ca.materialized_only, ca.compression_enabled,
			ca.hypertable_schema AS source_hypertable_schema, ca.hypertable_name AS source_hypertable_name,
			ca.materialization_hypertable_schema, ca.materialization_hypertable_name, ca.view_definition,
			j.job_id AS refresh_job_id, j.schedule_interval::text AS refresh_schedule_interval, j.config AS refresh_config, j.scheduled AS refresh_scheduled
			FROM timescaledb_information.continuous_aggregates ca
			LEFT JOIN timescaledb_information.jobs j ON j.proc_name = 'policy_refresh_continuous_aggregate'
				AND j.hypertable_schema = ca.view_schema AND j.hypertable_name = ca.view_name
			ORDER BY 1, 2`)
	})
}

// Jobs lists background jobs (policies and user-defined actions) with
// their statistics.
func (d *Database) Jobs(ctx context.Context, caller string) ([]Row, error) {
	return d.rowsTS(ctx, caller, func(ctx context.Context, tx pgx.Tx, ts bool) ([]Row, error) {
		if !ts {
			return nil, ErrNoTimescaleDB
		}
		return queryRows(ctx, tx, `SELECT j.job_id, j.application_name, j.proc_schema, j.proc_name, j.owner::text AS owner, j.scheduled, j.fixed_schedule,
			j.schedule_interval::text AS schedule_interval, j.max_runtime::text AS max_runtime, j.max_retries, j.retry_period::text AS retry_period,
			j.config, j.hypertable_schema, j.hypertable_name, j.next_start,
			s.last_run_started_at, s.last_successful_finish, s.last_run_status, s.job_status, s.last_run_duration::text AS last_run_duration,
			s.total_runs, s.total_successes, s.total_failures
			FROM timescaledb_information.jobs j LEFT JOIN timescaledb_information.job_stats s ON s.job_id = j.job_id
			ORDER BY j.job_id`)
	})
}

// SampleRows returns the newest rows of a table (by the hypertable time
// dimension when there is one). Identifiers are validated against the
// catalog and quoted; nothing from the caller is interpolated unvalidated.
func (d *Database) SampleRows(ctx context.Context, caller, schema, table string, limit int) (*QueryResult, error) {
	limit = d.clampRows(limit)
	res := &QueryResult{Database: d.cfg.Name, Columns: []Column{}, Rows: [][]any{}}
	start := time.Now()
	err := d.withTx(ctx, caller, 0, func(ctx context.Context, tx pgx.Tx) error {
		rel, err := lookupRelation(ctx, tx, schema, table)
		if err != nil {
			return err
		}
		orderBy := ""
		if _, ts, err := timescaleVersion(ctx, tx); err != nil {
			return err
		} else if ts {
			var timeCol *string
			err := tx.QueryRow(ctx, `SELECT column_name FROM timescaledb_information.dimensions WHERE hypertable_schema = $1 AND hypertable_name = $2 AND dimension_number = 1`, schema, table).Scan(&timeCol)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if timeCol != nil {
				orderBy = " ORDER BY " + pgx.Identifier{*timeCol}.Sanitize() + " DESC"
			}
		}
		sql := "SELECT * FROM " + pgx.Identifier{rel.schema, rel.name}.Sanitize() + orderBy + " LIMIT $1"
		rows, err := tx.Query(ctx, sql, limit+1)
		if err != nil {
			return err
		}
		cols, data, truncated, err := collectRows(ctx, tx, rows, limit)
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

// relation is what lookupRelation learns about a table-like object.
type relation struct {
	oid         uint32
	schema      string
	name        string
	relkind     string
	kind        string
	owner       string
	comment     *string
	rowEstimate int64
}

// lookupRelation resolves schema.table to a table, view, materialized view,
// foreign or partitioned table, or ErrNotFound.
func lookupRelation(ctx context.Context, tx pgx.Tx, schema, table string) (*relation, error) {
	if schema == "" || table == "" {
		return nil, fmt.Errorf("schema and table are required: %w", ErrNotFound)
	}
	r := &relation{schema: schema, name: table}
	err := tx.QueryRow(ctx, `SELECT c.oid, c.relkind::text, pg_get_userbyid(c.relowner), obj_description(c.oid, 'pg_class'), c.reltuples::bigint
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m','f')`, schema, table).
		Scan(&r.oid, &r.relkind, &r.owner, &r.comment, &r.rowEstimate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("relation %s.%s: %w", schema, table, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	r.kind = relationKinds[r.relkind]
	return r, nil
}

// timescaleVersion reports the installed timescaledb extension version.
func timescaleVersion(ctx context.Context, tx pgx.Tx) (string, bool, error) {
	var v *string
	if err := tx.QueryRow(ctx, "SELECT extversion FROM pg_catalog.pg_extension WHERE extname = 'timescaledb'").Scan(&v); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return *v, true, nil
}

// rows runs one catalog query inside an attributed read-only transaction.
func (d *Database) rows(ctx context.Context, caller, sql string, args ...any) ([]Row, error) {
	var out []Row
	err := d.withTx(ctx, caller, 0, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = queryRows(ctx, tx, sql, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// rowsTS is rows with the TimescaleDB-installed flag resolved first.
func (d *Database) rowsTS(ctx context.Context, caller string, fn func(ctx context.Context, tx pgx.Tx, ts bool) ([]Row, error)) ([]Row, error) {
	var out []Row
	err := d.withTx(ctx, caller, 0, func(ctx context.Context, tx pgx.Tx) error {
		_, ts, err := timescaleVersion(ctx, tx)
		if err != nil {
			return err
		}
		out, err = fn(ctx, tx, ts)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Row{}
	}
	return out, nil
}

// listTS is rowsTS for paged listings.
func (d *Database) listTS(ctx context.Context, caller string, fn func(ctx context.Context, tx pgx.Tx, ts bool) (*Listing, error)) (*Listing, error) {
	var out *Listing
	err := d.withTx(ctx, caller, 0, func(ctx context.Context, tx pgx.Tx) error {
		_, ts, err := timescaleVersion(ctx, tx)
		if err != nil {
			return err
		}
		out, err = fn(ctx, tx, ts)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryRows runs a query and returns every row as a column-name keyed map
// with encoded values. Used for catalog queries whose size is bounded by
// the schema, not by data.
func queryRows(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]Row, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	tm := tx.Conn().TypeMap()
	names := make([]string, len(fds))
	types := make([]string, len(fds))
	for i, fd := range fds {
		names[i] = fd.Name
		types[i] = typeName(tm, fd.DataTypeOID)
	}
	out := []Row{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := make(Row, len(vals))
		for i, v := range vals {
			r[names[i]] = encodeValue(v, types[i])
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SplitQualified splits "schema.table" into its parts; a bare name gets the
// public schema. Quoted identifiers are not supported: callers pass schema
// and table separately.
func SplitQualified(name string) (string, string) {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "public", name
}
