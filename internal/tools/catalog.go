package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

const (
	defaultChunkLimit  = 50
	defaultSampleLimit = 20
	maxChunkLimit      = 1000
	maxListLimit       = 1000
)

// listTool registers a catalog list tool that takes only the database
// argument.
func listTool(s *mcpsrv.MCPServer, deps Deps, name, description string, fetch func(ctx context.Context, db *timescale.Database, caller string) ([]timescale.Row, error)) {
	tool := readOnlyTool(name, description, databaseArg(deps))
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		rows, err := fetch(ctx, db, callerName(caller))
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		return listResult{Database: db.Name(), Items: rows, Count: len(rows)}, callStats{database: db.Name(), rows: len(rows)}, nil
	}))
}

func registerListSchemas(s *mcpsrv.MCPServer, deps Deps) {
	listTool(s, deps, "timescale_list_schemas",
		"List non-system schemas with owner, relation count and comment. TimescaleDB-internal schemas (_timescaledb_*) holding chunks are hidden; "+
			"pass them explicitly to timescale_list_tables when needed.",
		func(ctx context.Context, db *timescale.Database, caller string) ([]timescale.Row, error) {
			return db.Schemas(ctx, caller)
		})
}

// pageArgs are the arguments the paged listings (tables, hypertables)
// share: limit, offset and include_sizes.
func pageArgs(noun string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithInteger(argLimit, mcp.Description(fmt.Sprintf("Maximum %s to return (default %d, max %d). The listing is ordered by schema, name.", noun, timescale.DefaultListLimit, maxListLimit)),
			mcp.DefaultNumber(timescale.DefaultListLimit), mcp.Min(1), mcp.Max(maxListLimit)),
		mcp.WithInteger(argOffset, mcp.Description(fmt.Sprintf("Number of %s to skip, for paging (default 0).", noun)), mcp.DefaultNumber(0), mcp.Min(0)),
		mcp.WithBoolean(argSizes, mcp.Description(fmt.Sprintf("Compute total_bytes and total_size for the page. Default: true when the page holds fewer than %d %s, otherwise false and the result says so. "+
			"Sizing stats every chunk of the listed hypertables, so an explicit true costs time proportional to the page's chunks; timescale_describe_table sizes one hypertable in detail.", timescale.SizeThreshold, noun))),
	}
}

// pageOptions reads limit, offset and include_sizes.
func pageOptions(req mcp.CallToolRequest) (timescale.ListOptions, error) {
	var opts timescale.ListOptions
	var err error
	if opts.Limit, err = argInt(req, argLimit, timescale.DefaultListLimit); err != nil {
		return opts, err
	}
	if opts.Limit < 1 || opts.Limit > maxListLimit {
		return opts, &argError{fmt.Sprintf("argument limit must be between 1 and %d", maxListLimit)}
	}
	if opts.Offset, err = argInt(req, argOffset, 0); err != nil {
		return opts, err
	}
	if opts.Offset < 0 {
		return opts, &argError{"argument offset must be at least 0"}
	}
	sizes, set, err := argBoolSet(req, argSizes)
	if err != nil {
		return opts, err
	}
	switch {
	case set && sizes:
		opts.Sizes = timescale.SizesAlways
	case set:
		opts.Sizes = timescale.SizesNever
	}
	return opts, nil
}

// pagePayload wraps one listing page and spells out what was left out:
// rows beyond the page and sizes the threshold skipped.
func pagePayload(db *timescale.Database, l *timescale.Listing, opts timescale.ListOptions, noun string) pageResult {
	res := pageResult{Database: db.Name(), Items: l.Items, Count: len(l.Items), TotalCount: l.TotalCount, Offset: opts.Offset,
		Truncated: opts.Offset+len(l.Items) < l.TotalCount, SizesIncluded: l.SizesIncluded}
	var notes []string
	if res.Truncated {
		notes = append(notes, fmt.Sprintf("%d of %d %s shown, ordered by schema, name; page with offset or narrow with schema", len(l.Items), l.TotalCount, noun))
	}
	const describeHint = "timescale_describe_table has one hypertable's detailed size"
	switch {
	case l.SizesError != nil:
		notes = append(notes, fmt.Sprintf("sizes skipped: %s; retry with include_sizes=true or a smaller limit, or %s", timescale.Describe(db.Name(), l.SizesError), describeHint))
	case !l.SizesIncluded && opts.Sizes == timescale.SizesAuto:
		notes = append(notes, fmt.Sprintf("sizes skipped: the page holds %d %s (threshold %d); pass include_sizes=true to size this page in one pass, use a smaller limit, or %s",
			len(l.Items), noun, timescale.SizeThreshold, describeHint))
	}
	res.Note = strings.Join(notes, ". ")
	return res
}

func registerListTables(s *mcpsrv.MCPServer, deps Deps) {
	const noun = "relations"
	tool := readOnlyTool("timescale_list_tables",
		fmt.Sprintf("List tables (and by default views) with kind, owner, row estimate and comment, ordered by schema and name and paged with limit/offset. "+
			"is_hypertable marks TimescaleDB hypertables; is_continuous_aggregate marks continuous aggregate views. "+
			"total_bytes/total_size are included when the page holds fewer than %d relations or include_sizes is true "+
			"(hypertables are sized like hypertable_size, chunks included, in one pass for the whole page); the result says when sizes were skipped and "+
			"timescale_describe_table has one hypertable's detailed size. Without schema, system and _timescaledb_* schemas are skipped.", timescale.SizeThreshold),
		append([]mcp.ToolOption{
			databaseArg(deps),
			mcp.WithString(argSchema, mcp.Description("Restrict to one schema. Naming a schema also lists internal ones such as _timescaledb_internal.")),
			mcp.WithBoolean("include_views", mcp.Description("Include views and materialized views (default true)."), mcp.DefaultBool(true)),
		}, pageArgs(noun)...)...,
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSchema, "include_views", argLimit, argOffset, argSizes}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		opts, err := pageOptions(req)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		if opts.Schema, _, err = argString(req, argSchema); err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		if opts.IncludeViews, err = argBool(req, "include_views", true); err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		l, err := db.Tables(ctx, callerName(caller), opts)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		res := pagePayload(db, l, opts, noun)
		return res, callStats{database: db.Name(), rows: res.Count, truncated: res.Truncated}, nil
	}))
}

func registerDescribeTable(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_describe_table",
		"Describe a table, view or hypertable: columns (type, nullable, default, comment), primary key, indexes, foreign keys, view definition. "+
			"For hypertables also the dimensions (time column and chunk interval), chunk count, compression settings, detailed size and the retention/compression/reorder policies; "+
			"for continuous aggregates the definition, source hypertable and refresh policy.",
		databaseArg(deps),
		mcp.WithString(argSchema, mcp.Required(), mcp.Description("Schema name, e.g. public.")),
		mcp.WithString(argTable, mcp.Required(), mcp.Description("Table, view or hypertable name.")),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSchema, argTable}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		schema, err := requireString(req, argSchema)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		table, err := requireString(req, argTable)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		desc, err := db.DescribeTable(ctx, callerName(caller), schema, table)
		return desc, callStats{database: db.Name()}, err
	}))
}

func registerListHypertables(s *mcpsrv.MCPServer, deps Deps) {
	const noun = "hypertables"
	tool := readOnlyTool("timescale_list_hypertables",
		fmt.Sprintf("List TimescaleDB hypertables with owner, dimensions, chunk count, compression flag and the time column with its type and chunk interval, "+
			"ordered by schema and name and paged with limit/offset. total_bytes/total_size (hypertable_size: the table plus every chunk, compressed data included) "+
			"are included when the page holds fewer than %d hypertables or include_sizes is true, computed in one pass for the whole page; the result says when sizes were skipped "+
			"and timescale_describe_table has one hypertable's detailed size. Hypertables that materialize a continuous aggregate are marked. "+
			"Errors when the timescaledb extension is not installed.", timescale.SizeThreshold),
		append([]mcp.ToolOption{
			databaseArg(deps),
			mcp.WithString(argSchema, mcp.Description("Restrict to the hypertables of one schema.")),
		}, pageArgs(noun)...)...,
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSchema, argLimit, argOffset, argSizes}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		opts, err := pageOptions(req)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		if opts.Schema, _, err = argString(req, argSchema); err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		l, err := db.Hypertables(ctx, callerName(caller), opts)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		res := pagePayload(db, l, opts, noun)
		return res, callStats{database: db.Name(), rows: res.Count, truncated: res.Truncated}, nil
	}))
}

func registerListChunks(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_list_chunks",
		"List the chunks of one hypertable with their time (or integer) range, compression state, tablespace and size. Newest first by default.",
		databaseArg(deps),
		mcp.WithString(argSchema, mcp.Required(), mcp.Description("Schema of the hypertable.")),
		mcp.WithString(argHypertable, mcp.Required(), mcp.Description("Hypertable name.")),
		mcp.WithInteger(argLimit, mcp.Description("Maximum chunks to return (default 50, max 1000)."), mcp.DefaultNumber(defaultChunkLimit), mcp.Min(1), mcp.Max(maxChunkLimit)),
		mcp.WithBoolean("newest_first", mcp.Description("Order by range start descending (default true)."), mcp.DefaultBool(true)),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSchema, argHypertable, argLimit, "newest_first"}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		schema, err := requireString(req, argSchema)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		hypertable, err := requireString(req, argHypertable)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		limit, err := argInt(req, argLimit, defaultChunkLimit)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		if limit < 1 || limit > maxChunkLimit {
			return nil, callStats{database: db.Name()}, &argError{"argument limit must be between 1 and 1000"}
		}
		newestFirst, err := argBool(req, "newest_first", true)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		rows, err := db.Chunks(ctx, callerName(caller), schema, hypertable, limit, newestFirst)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		return listResult{Database: db.Name(), Items: rows, Count: len(rows)}, callStats{database: db.Name(), rows: len(rows)}, nil
	}))
}

func registerListContinuousAggregates(s *mcpsrv.MCPServer, deps Deps) {
	listTool(s, deps, "timescale_list_continuous_aggregates",
		"List continuous aggregates (pre-computed time_bucket rollups) with their definition, source and materialization hypertable, materialized_only/finalized flags, "+
			"compression and refresh policy. Query them like tables for fast long-range analysis. Empty on the Apache-licensed TimescaleDB build.",
		func(ctx context.Context, db *timescale.Database, caller string) ([]timescale.Row, error) {
			return db.ContinuousAggregates(ctx, caller)
		})
}

func registerListJobs(s *mcpsrv.MCPServer, deps Deps) {
	listTool(s, deps, "timescale_list_jobs",
		"List TimescaleDB background jobs (retention, compression, reorder and refresh policies, user-defined actions) with schedule, config, target hypertable "+
			"and statistics: last run status, last successful finish, next start, total runs and failures.",
		func(ctx context.Context, db *timescale.Database, caller string) ([]timescale.Row, error) {
			return db.Jobs(ctx, caller)
		})
}

func registerSampleRows(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_sample_rows",
		"Return the newest rows of a table: hypertables are ordered by their time dimension descending, other relations in storage order. "+
			"Identifiers are validated against the catalog. Result shape matches timescale_query.",
		databaseArg(deps),
		mcp.WithString(argSchema, mcp.Required(), mcp.Description("Schema name.")),
		mcp.WithString(argTable, mcp.Required(), mcp.Description("Table, view or hypertable name.")),
		mcp.WithInteger(argLimit, mcp.Description("Rows to return (default 20, capped by the database max_rows)."), mcp.DefaultNumber(defaultSampleLimit), mcp.Min(1)),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSchema, argTable, argLimit}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		schema, err := requireString(req, argSchema)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		table, err := requireString(req, argTable)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		limit, err := argInt(req, argLimit, defaultSampleLimit)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		if limit < 1 {
			return nil, callStats{database: db.Name()}, &argError{"argument limit must be at least 1"}
		}
		res, err := db.SampleRows(ctx, callerName(caller), schema, table, limit)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		return res, callStats{database: db.Name(), rows: res.RowCount, truncated: res.Truncated}, nil
	}))
}
