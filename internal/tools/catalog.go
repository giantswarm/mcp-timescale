package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

const (
	defaultChunkLimit  = 50
	defaultSampleLimit = 20
	maxChunkLimit      = 1000
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

func registerListTables(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_list_tables",
		"List tables (and by default views) with kind, owner, row estimate, total size and comment. is_hypertable marks TimescaleDB hypertables "+
			"(sized with hypertable_size, chunks included); is_continuous_aggregate marks continuous aggregate views. Without schema, system and _timescaledb_* schemas are skipped.",
		databaseArg(deps),
		mcp.WithString(argSchema, mcp.Description("Restrict to one schema. Naming a schema also lists internal ones such as _timescaledb_internal.")),
		mcp.WithBoolean("include_views", mcp.Description("Include views and materialized views (default true)."), mcp.DefaultBool(true)),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSchema, "include_views"}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		schema, _, err := argString(req, argSchema)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		includeViews, err := argBool(req, "include_views", true)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		rows, err := db.Tables(ctx, callerName(caller), schema, includeViews)
		if err != nil {
			return nil, callStats{database: db.Name()}, err
		}
		return listResult{Database: db.Name(), Items: rows, Count: len(rows)}, callStats{database: db.Name(), rows: len(rows)}, nil
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
	listTool(s, deps, "timescale_list_hypertables",
		"List TimescaleDB hypertables with owner, dimensions, chunk count, compression flag, the time column with its type and chunk interval, and total size. "+
			"Hypertables that materialize a continuous aggregate are marked. Errors when the timescaledb extension is not installed.",
		func(ctx context.Context, db *timescale.Database, caller string) ([]timescale.Row, error) {
			return db.Hypertables(ctx, caller)
		})
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
