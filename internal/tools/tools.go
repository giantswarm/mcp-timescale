// Package tools wires the MCP tools this server exposes. Every tool is
// read-only, resolves the caller's identity first (and refuses anonymous
// calls), checks the per-database allowlist, validates its arguments
// strictly and returns one JSON document as text content.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/sqlguard"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

// Instructions is the server-level hint clients show their model.
const Instructions = `mcp-timescale gives read-only access to TimescaleDB / PostgreSQL databases, acting as the calling person.
Start with timescale_list_databases, then timescale_list_hypertables or timescale_list_tables to see what exists and
timescale_describe_table for columns, the time dimension, chunk interval, compression, retention policies and detailed size.
Listings are ordered by schema and name and paged (limit, offset); they carry sizes only for small pages unless include_sizes is set.
Use timescale_query for analysis: time_bucket('1 hour', <time column>) with aggregates over a bounded WHERE <time column> > now() - interval '...' range
is the idiomatic TimescaleDB shape; prefer continuous aggregates (timescale_list_continuous_aggregates) for long ranges.
Every statement runs in a READ ONLY transaction, is capped by max_rows and a statement timeout, and is attributed to you via application_name.`

// Argument names shared by several tools.
const (
	argDatabase   = "database"
	argSchema     = "schema"
	argTable      = "table"
	argSQL        = "sql"
	argLimit      = "limit"
	argHypertable = "hypertable"
	argTimeoutSec = "timeout_seconds"
	argOffset     = "offset"
	argSizes      = "include_sizes"
)

// LocalCallerEmail is the identity used when the server runs without OAuth
// (stdio transport or OAUTH_ENABLED=false).
const LocalCallerEmail = "local"

// ErrNoCallerIdentity is returned when a tool is called without an
// authenticated caller. There is no fallback identity.
var ErrNoCallerIdentity = errors.New("no caller identity: this server acts on the caller's identity and refuses anonymous calls")

// Deps is the bag of dependencies tool handlers need.
type Deps struct {
	Registry *timescale.Registry
	Log      *slog.Logger
	// LocalCaller lets calls without an identity run as "local". Set only
	// for the stdio transport or when OAuth is disabled.
	LocalCaller bool
}

// ReadOnlyTools names every tool this server registers, in registration
// order. There is exactly one class: every tool reads, none writes, none hands
// out credentials — so unlike mcp-kubernetes (--non-destructive) or mcp-capi
// (--read-only) there is no write mode to switch on or off. The list is the
// classification a new tool has to join before it ships
// (TestEveryToolIsReadOnlyAndStrict compares it with what the server
// registers and with the annotations) and what the startup log reports.
var ReadOnlyTools = []string{
	"timescale_list_databases",
	"timescale_get_database_info",
	"timescale_list_schemas",
	"timescale_list_tables",
	"timescale_describe_table",
	"timescale_list_hypertables",
	"timescale_list_chunks",
	"timescale_list_continuous_aggregates",
	"timescale_list_jobs",
	"timescale_query",
	"timescale_explain",
	"timescale_sample_rows",
}

// Register installs every tool on s and returns their names (ReadOnlyTools).
func Register(s *mcpsrv.MCPServer, deps Deps) []string {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	registerListDatabases(s, deps)
	registerGetDatabaseInfo(s, deps)
	registerListSchemas(s, deps)
	registerListTables(s, deps)
	registerDescribeTable(s, deps)
	registerListHypertables(s, deps)
	registerListChunks(s, deps)
	registerListContinuousAggregates(s, deps)
	registerListJobs(s, deps)
	registerQuery(s, deps)
	registerExplain(s, deps)
	registerSampleRows(s, deps)
	return append([]string(nil), ReadOnlyTools...)
}

// readOnlyTool builds a tool with the annotations every tool here shares
// (read-only, non-destructive, idempotent, closed world) and a strict input
// schema that rejects unknown properties.
func readOnlyTool(name, description string, opts ...mcp.ToolOption) mcp.Tool {
	base := []mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithSchemaAdditionalProperties(false),
	}
	return mcp.NewTool(name, append(base, opts...)...)
}

// databaseArg is the shared "database" argument. It is required when more
// than one database is configured.
func databaseArg(deps Deps) mcp.ToolOption {
	opts := []mcp.PropertyOption{mcp.Description("Configured database name (see timescale_list_databases). Optional when exactly one database is configured.")}
	if deps.Registry != nil && deps.Registry.Len() > 1 {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithString(argDatabase, opts...)
}

// callStats is what a handler reports for the audit log.
type callStats struct {
	database  string
	rows      int
	truncated bool
}

// handler is the shape of every tool body: it gets the resolved caller and
// returns the JSON payload.
type handler func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error)

// wrap resolves the caller (fail closed), rejects unknown arguments, runs h,
// writes the audit line and encodes the result.
func (deps Deps) wrap(name string, allowedArgs []string, h handler) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		caller, err := deps.caller(ctx)
		if err != nil {
			deps.audit(name, caller, callStats{}, start, err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := checkArgs(req, allowedArgs); err != nil {
			deps.audit(name, caller, callStats{}, start, err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		payload, stats, err := h(ctx, req, caller)
		deps.audit(name, caller, stats, start, err)
		if err != nil {
			return mcp.NewToolResultError(describe(stats.database, err)), nil
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return mcp.NewToolResultError("encode result: " + err.Error()), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}

// caller resolves the identity for this call. Without OAuth (stdio or
// OAUTH_ENABLED=false) the caller is "local"; otherwise a missing identity
// is an error.
func (deps Deps) caller(ctx context.Context) (server.Caller, error) {
	if c, ok := server.CallerFromContext(ctx); ok && !c.Empty() {
		return c, nil
	}
	if deps.LocalCaller {
		return server.Caller{Subject: LocalCallerEmail, Email: LocalCallerEmail}, nil
	}
	return server.Caller{}, ErrNoCallerIdentity
}

// callerName is the attribution string: the email, else the subject.
func callerName(c server.Caller) string {
	if c.Email != "" {
		return c.Email
	}
	return c.Subject
}

// database resolves the "database" argument, defaulting to the only
// configured database, and enforces the allowlist.
func (deps Deps) database(req mcp.CallToolRequest, caller server.Caller) (*timescale.Database, error) {
	if deps.Registry == nil || deps.Registry.Len() == 0 {
		return nil, timescale.ErrNoDatabases
	}
	name, _, err := argString(req, argDatabase)
	if err != nil {
		return nil, err
	}
	if name == "" {
		if deps.Registry.Len() > 1 {
			return nil, &argError{fmt.Sprintf("database is required: %d databases are configured; timescale_list_databases lists the ones you may use", deps.Registry.Len())}
		}
		name = deps.Registry.Names()[0]
	}
	db, ok := deps.Registry.Get(name)
	if !ok {
		return nil, &argError{fmt.Sprintf("unknown database %q; timescale_list_databases lists the databases you may use", name)}
	}
	if !db.Allows(caller.Email, caller.Groups) {
		return nil, &forbiddenError{caller: callerName(caller), database: name}
	}
	return db, nil
}

// argError is a bad or unknown argument.
type argError struct{ msg string }

func (e *argError) Error() string { return e.msg }

// forbiddenError is an allowlist refusal.
type forbiddenError struct{ caller, database string }

func (e *forbiddenError) Error() string {
	return fmt.Sprintf("caller %s is not allowed to use database %s", e.caller, e.database)
}

// checkArgs rejects arguments outside the tool's schema. The MCP server
// also validates the schema; this keeps the guarantee when handlers are
// called directly.
func checkArgs(req mcp.CallToolRequest, allowed []string) error {
	args := req.GetArguments()
	var unknown []string
	for k := range args {
		found := false
		for _, a := range allowed {
			if a == k {
				found = true
				break
			}
		}
		if !found {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return &argError{fmt.Sprintf("unknown argument(s): %s (allowed: %s)", strings.Join(unknown, ", "), strings.Join(allowed, ", "))}
}

// argString reads an optional string argument; a value of another type is
// an error rather than silently ignored.
func argString(req mcp.CallToolRequest, key string) (string, bool, error) {
	v, ok := req.GetArguments()[key]
	if !ok || v == nil {
		return "", false, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return "", true, &argError{fmt.Sprintf("argument %s must be a string", key)}
	}
	return s, true, nil
}

// requireString reads a mandatory non-empty string argument.
func requireString(req mcp.CallToolRequest, key string) (string, error) {
	s, ok, err := argString(req, key)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(s) == "" {
		return "", &argError{fmt.Sprintf("argument %s is required", key)}
	}
	return s, nil
}

// argInt reads an optional integer argument. JSON numbers arrive as
// float64; non-integral values are rejected.
func argInt(req mcp.CallToolRequest, key string, def int) (int, error) {
	v, ok := req.GetArguments()[key]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != math.Trunc(n) || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > math.MaxInt32 {
			return 0, &argError{fmt.Sprintf("argument %s must be an integer", key)}
		}
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, &argError{fmt.Sprintf("argument %s must be an integer", key)}
		}
		return int(i), nil
	default:
		return 0, &argError{fmt.Sprintf("argument %s must be an integer", key)}
	}
}

// argBool reads an optional boolean argument.
func argBool(req mcp.CallToolRequest, key string, def bool) (bool, error) {
	b, set, err := argBoolSet(req, key)
	if err != nil || !set {
		return def, err
	}
	return b, nil
}

// argBoolSet reads an optional boolean argument and reports whether the
// caller set it, for arguments whose default is decided at run time.
func argBoolSet(req mcp.CallToolRequest, key string) (bool, bool, error) {
	v, ok := req.GetArguments()[key]
	if !ok || v == nil {
		return false, false, nil
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, true, &argError{fmt.Sprintf("argument %s must be a boolean", key)}
	}
	return b, true, nil
}

// errorClass labels an error for the audit log.
func errorClass(err error) string {
	var (
		ae *argError
		fe *forbiddenError
	)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoCallerIdentity):
		return "no_caller"
	case errors.As(err, &fe):
		return "forbidden"
	case errors.As(err, &ae):
		return "bad_argument"
	case errors.Is(err, sqlguard.ErrRejected):
		return "sql_rejected"
	default:
		return timescale.Class(err)
	}
}

// describe renders an error for the caller.
func describe(dbName string, err error) string {
	var (
		ae *argError
		fe *forbiddenError
	)
	switch {
	case errors.As(err, &ae), errors.As(err, &fe), errors.Is(err, sqlguard.ErrRejected),
		errors.Is(err, timescale.ErrNotFound), errors.Is(err, timescale.ErrNoTimescaleDB), errors.Is(err, timescale.ErrNoDatabases):
		return err.Error()
	default:
		return timescale.Describe(dbName, err)
	}
}

// audit writes the structured per-call log line.
func (deps Deps) audit(tool string, caller server.Caller, stats callStats, start time.Time, err error) {
	attrs := []any{
		"tool", tool,
		"caller", callerName(caller),
		"database", stats.database,
		"duration_ms", time.Since(start).Milliseconds(),
		"rows", stats.rows,
		"truncated", stats.truncated,
	}
	if err != nil {
		attrs = append(attrs, "error_class", errorClass(err), "error", err.Error())
		deps.Log.Warn("tool call failed", attrs...)
		return
	}
	deps.Log.Info("tool call", attrs...)
}

// listResult is the {items} envelope of every list tool, following the
// mcp-toolkit convention.
type listResult struct {
	Database string `json:"database,omitempty"`
	Items    any    `json:"items"`
	Count    int    `json:"count"`
}

// pageResult is the envelope of the paged listings (tables, hypertables):
// listResult plus the page bounds and whether the rows carry sizes. Note
// tells a model what was left out and how to get it.
type pageResult struct {
	Database      string `json:"database,omitempty"`
	Items         any    `json:"items"`
	Count         int    `json:"count"`
	TotalCount    int    `json:"total_count"`
	Offset        int    `json:"offset"`
	Truncated     bool   `json:"truncated"`
	SizesIncluded bool   `json:"sizes_included"`
	Note          string `json:"note,omitempty"`
}
