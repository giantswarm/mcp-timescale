package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

const (
	toolInsertRow = "timescale_insert_row"
	argRow        = "row"
)

// WriteTools names the tools that write. They are registered only when a
// database has an insert path configured; without one the server stays
// read-only (ReadOnlyTools).
var WriteTools = []string{toolInsertRow}

// registerInsertRow installs timescale_insert_row when at least one
// database has an insert path. The description names the writable tables so
// a model knows the whole write surface up front.
func registerInsertRow(s *mcpsrv.MCPServer, deps Deps) bool {
	if deps.Registry == nil || !deps.Registry.Insertable() {
		return false
	}
	var writable []string
	for _, db := range deps.Registry.All() {
		if t := db.InsertTables(); len(t) > 0 {
			writable = append(writable, fmt.Sprintf("%s: %s", db.Name(), strings.Join(t, ", ")))
		}
	}
	sort.Strings(writable)
	tool := mcp.NewTool(toolInsertRow,
		mcp.WithDescription("Insert one row into a writable table and commit it. This is the only write this server offers: no UPDATE, DELETE or DDL, "+
			"and only the tables configured as writable (by database: "+strings.Join(writable, "; ")+"). "+
			"row maps column names to values; PostgreSQL converts them to the column types (timestamps as RFC 3339 strings, arrays as JSON arrays, "+
			"json/jsonb columns as JSON values). Columns left out keep their defaults. The insert runs as a role that may only INSERT into these tables "+
			"and is attributed to you via application_name. Read the table's columns first with timescale_describe_table."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithSchemaAdditionalProperties(false),
		databaseArg(deps),
		mcp.WithString(argTable, mcp.Required(), mcp.Description("Schema-qualified writable table, for example werk.cases.")),
		mcp.WithObject(argRow, mcp.Required(), mcp.Description("Column name to value. Column names are lower-case identifiers.")),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase, argTable, argRow}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		stats := callStats{database: db.Name()}
		table, err := requireString(req, argTable)
		if err != nil {
			return nil, stats, err
		}
		v, ok := req.GetArguments()[argRow]
		if !ok || v == nil {
			return nil, stats, &argError{"argument row is required"}
		}
		row, isObj := v.(map[string]any)
		if !isObj {
			return nil, stats, &argError{"argument row must be an object of column name to value"}
		}
		res, err := db.Insert(ctx, callerName(caller), strings.TrimSpace(table), row)
		if err != nil {
			if errors.Is(err, timescale.ErrInsertNotAllowed) {
				return nil, stats, &argError{err.Error()}
			}
			return nil, stats, err
		}
		stats.rows = int(res.Inserted)
		return res, stats, nil
	}))
	return true
}
