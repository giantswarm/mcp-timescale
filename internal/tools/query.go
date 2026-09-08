package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/sqlguard"
)

const defaultQueryRows = 100

func registerQuery(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_query",
		"Run one read-only SQL statement (SELECT, WITH ... SELECT, VALUES, TABLE, EXPLAIN, SHOW) and return columns, rows and whether the result was truncated. "+
			"Data-modifying statements, SET/RESET, row locks, multiple statements and side-effect functions are refused; the transaction is READ ONLY regardless. "+
			"TimescaleDB idioms: time_bucket('5 minutes', ts) AS bucket ... GROUP BY bucket; first()/last() aggregates; always bound the time range "+
			"(WHERE ts > now() - interval '1 day') so only the relevant chunks are scanned. Values: timestamps as RFC 3339 strings, numerics as strings, bytea as base64.",
		databaseArg(deps),
		mcp.WithString(argSQL, mcp.Required(), mcp.Description("A single SQL statement. A trailing semicolon is allowed.")),
		mcp.WithInteger("max_rows", mcp.Description("Rows to return (default 100). Capped by the database max_rows; truncated is true when more rows existed."), mcp.DefaultNumber(defaultQueryRows), mcp.Min(1)),
		mcp.WithInteger(argTimeoutSec, mcp.Description("Statement timeout for this call in seconds; at most the database statement timeout."), mcp.Min(1)),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSQL, "max_rows", argTimeoutSec}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		stats := callStats{database: db.Name()}
		sql, err := requireString(req, argSQL)
		if err != nil {
			return nil, stats, err
		}
		maxRows, err := argInt(req, "max_rows", defaultQueryRows)
		if err != nil {
			return nil, stats, err
		}
		if maxRows < 1 {
			return nil, stats, &argError{"argument max_rows must be at least 1"}
		}
		timeout, err := timeoutArg(req, db.StatementTimeout())
		if err != nil {
			return nil, stats, err
		}
		stmt, err := sqlguard.Classify(sql)
		if err != nil {
			return nil, stats, err
		}
		res, err := db.Query(ctx, callerName(caller), stmt, maxRows, timeout)
		if err != nil {
			return nil, stats, err
		}
		stats.rows, stats.truncated = res.RowCount, res.Truncated
		return res, stats, nil
	}))
}

func registerExplain(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_explain",
		"Show the execution plan of a SELECT-like statement: EXPLAIN (ANALYZE <analyze>, BUFFERS <analyze>, FORMAT <format>). With analyze the statement runs "+
			"(read-only) and real timings are reported. Use it to check chunk exclusion (few chunks scanned when the time range is bounded) and index use.",
		databaseArg(deps),
		mcp.WithString(argSQL, mcp.Required(), mcp.Description("The SELECT, WITH ... SELECT, VALUES or TABLE statement to explain.")),
		mcp.WithBoolean("analyze", mcp.Description("Execute the statement and report actual timings and buffers (default false)."), mcp.DefaultBool(false)),
		mcp.WithString("format", mcp.Description("Plan format: text (default) or json."), mcp.Enum("text", "json"), mcp.DefaultString("text")),
		mcp.WithInteger(argTimeoutSec, mcp.Description("Statement timeout for this call in seconds; at most the database statement timeout."), mcp.Min(1)),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argSQL, "analyze", "format", argTimeoutSec}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		stats := callStats{database: db.Name()}
		sql, err := requireString(req, argSQL)
		if err != nil {
			return nil, stats, err
		}
		analyze, err := argBool(req, "analyze", false)
		if err != nil {
			return nil, stats, err
		}
		format, _, err := argString(req, "format")
		if err != nil {
			return nil, stats, err
		}
		format = strings.ToLower(strings.TrimSpace(format))
		if format == "" {
			format = "text"
		}
		if format != "text" && format != "json" {
			return nil, stats, &argError{fmt.Sprintf("argument format must be text or json, got %q", format)}
		}
		timeout, err := timeoutArg(req, db.StatementTimeout())
		if err != nil {
			return nil, stats, err
		}
		stmt, err := sqlguard.Classify(sql)
		if err != nil {
			return nil, stats, err
		}
		res, err := db.Explain(ctx, callerName(caller), stmt, analyze, format, timeout)
		return res, stats, err
	}))
}

// timeoutArg reads timeout_seconds and bounds it by the database statement
// timeout. Zero means "use the database default".
func timeoutArg(req mcp.CallToolRequest, maxTimeout time.Duration) (time.Duration, error) {
	secs, err := argInt(req, argTimeoutSec, 0)
	if err != nil {
		return 0, err
	}
	if secs == 0 {
		return 0, nil
	}
	if secs < 1 {
		return 0, &argError{"argument timeout_seconds must be at least 1"}
	}
	d := time.Duration(secs) * time.Second
	if d > maxTimeout {
		return 0, &argError{fmt.Sprintf("argument timeout_seconds must not exceed the database statement timeout of %d seconds", int64(maxTimeout.Seconds()))}
	}
	return d, nil
}
