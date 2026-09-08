package tools

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

const (
	dbOpen       = "open"
	dbRestricted = "restricted"
	schemaPublic = "public"
	argMaxRows   = "max_rows"
	nope         = "nope"
	sqlSetRW     = "SET transaction_read_only = off"
	groupA       = "team-a"
	aliceEmail   = "alice@example.com"
	toolQuery    = "timescale_query"
	toolList     = "timescale_list_databases"
	sqlOne       = "SELECT 1"
)

// unreachable databases: port 1 on loopback refuses immediately, so
// anything that reaches the pool fails fast with a connect error.
func testRegistry(t *testing.T, restricted bool) *timescale.Registry {
	t.Helper()
	dbs := []config.Database{
		{Name: dbOpen, Description: "open to all", Host: "127.0.0.1", Port: 1, DBName: "x", SSLMode: "disable", User: "u", Password: "topsecret", MaxRows: 500, StatementTimeout: 30 * time.Second, MaxConnections: 1},
	}
	if restricted {
		dbs = append(dbs, config.Database{Name: dbRestricted, Host: "127.0.0.1", Port: 1, DBName: "y", SSLMode: "disable", User: "u", Password: "topsecret", AllowedGroups: []string{groupA}, MaxRows: 20, StatementTimeout: 10 * time.Second, MaxConnections: 1})
	}
	reg, err := timescale.NewRegistry(dbs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	return reg
}

type harness struct {
	t   *testing.T
	cli *client.Client
}

// newHarness builds an MCP server with the tools and an in-process client.
// validate switches the server's own input schema validation on.
func newHarness(t *testing.T, deps Deps, validate bool) *harness {
	t.Helper()
	deps.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := []mcpsrv.ServerOption{mcpsrv.WithToolCapabilities(false), mcpsrv.WithStrictInputSchemaDefault()}
	if validate {
		opts = append(opts, mcpsrv.WithInputSchemaValidation())
	}
	srv := mcpsrv.NewMCPServer("test", "0", opts...)
	Register(srv, deps)
	cli, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &harness{t: t, cli: cli}
}

func (h *harness) call(ctx context.Context, name string, args map[string]any) (string, bool) {
	h.t.Helper()
	res, err := h.cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		h.t.Fatalf("CallTool(%s): %v", name, err)
	}
	if len(res.Content) == 0 {
		h.t.Fatalf("CallTool(%s): no content", name)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		h.t.Fatalf("CallTool(%s): content %T, want text", name, res.Content[0])
	}
	return tc.Text, res.IsError
}

func (h *harness) mustErr(ctx context.Context, name string, args map[string]any, mention string) {
	h.t.Helper()
	text, isErr := h.call(ctx, name, args)
	if !isErr {
		h.t.Fatalf("%s(%v) succeeded, want error mentioning %q: %s", name, args, mention, text)
	}
	if !strings.Contains(text, mention) {
		h.t.Errorf("%s(%v) error %q does not mention %q", name, args, text, mention)
	}
}

func (h *harness) mustOK(ctx context.Context, name string, args map[string]any) map[string]any {
	h.t.Helper()
	text, isErr := h.call(ctx, name, args)
	if isErr {
		h.t.Fatalf("%s(%v) failed: %s", name, args, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		h.t.Fatalf("%s result is not a JSON object: %v\n%s", name, err, text)
	}
	return out
}

func alice(groups ...string) context.Context {
	return server.ContextWithCaller(context.Background(), server.Caller{Subject: "sub-alice", Email: aliceEmail, Groups: groups})
}

func TestEveryToolIsReadOnlyAndStrict(t *testing.T) {
	h := newHarness(t, Deps{Registry: testRegistry(t, true)}, true)
	res, err := h.cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The registered set is exactly the classification: a new tool joins
	// ReadOnlyTools (and the README table) before it ships, and nothing in
	// the list may silently stop being registered.
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	want := append([]string(nil), ReadOnlyTools...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registered tools %v\n  want ReadOnlyTools %v", got, want)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a.ReadOnlyHint == nil || !*a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint ||
			a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: annotations must be readOnly/non-destructive/idempotent/closed-world, got %+v", tool.Name, a)
		}
		if !strings.HasPrefix(tool.Name, "timescale_") {
			t.Errorf("%s: tools are prefixed timescale_", tool.Name)
		}
		if ap, ok := tool.InputSchema.AdditionalProperties.(bool); !ok || ap {
			t.Errorf("%s: input schema must set additionalProperties=false, got %v", tool.Name, tool.InputSchema.AdditionalProperties)
		}
		if tool.Name != toolList {
			p, ok := tool.InputSchema.Properties[argDatabase]
			if !ok {
				t.Errorf("%s: missing database argument", tool.Name)
			}
			if !containsString(tool.InputSchema.Required, argDatabase) {
				t.Errorf("%s: database must be required with two databases configured (schema %v)", tool.Name, p)
			}
		}
	}
}

func TestDatabaseArgOptionalWithOneDatabase(t *testing.T) {
	h := newHarness(t, Deps{Registry: testRegistry(t, false)}, true)
	res, err := h.cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if containsString(tool.InputSchema.Required, argDatabase) {
			t.Errorf("%s: database must be optional with a single database", tool.Name)
		}
	}
}

func TestFailClosedWithoutCaller(t *testing.T) {
	h := newHarness(t, Deps{Registry: testRegistry(t, false)}, false)
	res, err := h.cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		args := map[string]any{argSQL: sqlOne, argSchema: schemaPublic, argTable: "t", argHypertable: "t"}
		h.mustErr(context.Background(), tool.Name, args, "no caller identity")
	}
}

func TestLocalCallerWhenOAuthIsOff(t *testing.T) {
	h := newHarness(t, Deps{Registry: testRegistry(t, false), LocalCaller: true}, true)
	out := h.mustOK(context.Background(), toolList, nil)
	if out["caller"] != LocalCallerEmail {
		t.Errorf("caller = %v, want %s", out["caller"], LocalCallerEmail)
	}
	// An authenticated caller still wins over the local fallback.
	out = h.mustOK(alice(), toolList, nil)
	if out["caller"] != aliceEmail {
		t.Errorf("caller = %v, want %s", out["caller"], aliceEmail)
	}
}

func TestListDatabasesHidesRestrictedAndNeverLeaksSecrets(t *testing.T) {
	h := newHarness(t, Deps{Registry: testRegistry(t, true)}, true)

	text, isErr := h.call(alice(), toolList, nil)
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "topsecret") {
		t.Fatal("list_databases leaked a password")
	}
	var out struct {
		Items  []map[string]any `json:"items"`
		Hidden int              `json:"hidden_count"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Hidden != 1 || out.Items[0]["name"] != dbOpen {
		t.Fatalf("alice without groups: items=%v hidden=%d", out.Items, out.Hidden)
	}
	if out.Items[0]["reachable"] != false || out.Items[0]["error_class"] != "connect" {
		t.Errorf("unreachable database must report reachable=false and error_class=connect: %v", out.Items[0])
	}
	if errMsg, _ := out.Items[0]["error"].(string); strings.Contains(errMsg, "topsecret") || strings.Contains(errMsg, "user=") {
		t.Errorf("connect error must not echo the connection string: %q", errMsg)
	}

	text, _ = h.call(alice(groupA), toolList, nil)
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 || out.Hidden != 0 {
		t.Fatalf("alice in %s: items=%d hidden=%d", groupA, len(out.Items), out.Hidden)
	}
}

func TestAllowlistEnforcedPerDatabase(t *testing.T) {
	h := newHarness(t, Deps{Registry: testRegistry(t, true)}, true)
	h.mustErr(alice(), toolQuery, map[string]any{argDatabase: dbRestricted, argSQL: sqlOne}, "caller alice@example.com is not allowed to use database restricted")
	h.mustErr(alice(), "timescale_get_database_info", map[string]any{argDatabase: dbRestricted}, "not allowed")
	// With the group the allowlist passes and the call reaches the (dead) pool.
	h.mustErr(alice(groupA), toolQuery, map[string]any{argDatabase: dbRestricted, argSQL: sqlOne}, "cannot connect to database restricted")
}

func TestDatabaseSelection(t *testing.T) {
	multi := newHarness(t, Deps{Registry: testRegistry(t, true)}, false)
	multi.mustErr(alice(), toolQuery, map[string]any{argSQL: sqlOne}, "database is required")
	multi.mustErr(alice(), toolQuery, map[string]any{argDatabase: nope, argSQL: sqlOne}, `unknown database "nope"`)

	single := newHarness(t, Deps{Registry: testRegistry(t, false)}, false)
	// Defaults to the only database and then fails on the dead pool.
	single.mustErr(alice(), toolQuery, map[string]any{argSQL: sqlOne}, "cannot connect to database open")

	none := newHarness(t, Deps{Registry: mustRegistry(t, nil)}, false)
	none.mustErr(alice(), toolQuery, map[string]any{argSQL: sqlOne}, "no databases are configured")
	out := none.mustOK(alice(), toolList, nil)
	if out["count"] != float64(0) {
		t.Errorf("empty registry: %v", out)
	}
}

func TestUnknownArgumentsRejected(t *testing.T) {
	ctx := alice()
	// Handler-level check (server validation off).
	h := newHarness(t, Deps{Registry: testRegistry(t, false)}, false)
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: sqlOne, "bogus": 1}, "unknown argument(s): bogus")
	h.mustErr(ctx, toolList, map[string]any{argDatabase: dbOpen}, "unknown argument(s): database")
	// Server-level schema validation (as wired in cmd/serve.go) refuses too.
	strict := newHarness(t, Deps{Registry: testRegistry(t, false)}, true)
	if _, isErr := strict.call(ctx, toolQuery, map[string]any{argSQL: sqlOne, "bogus": 1}); !isErr {
		t.Error("server-side schema validation must reject unknown properties")
	}
}

func TestArgumentTypesAndBounds(t *testing.T) {
	ctx := alice()
	h := newHarness(t, Deps{Registry: testRegistry(t, false)}, false)
	h.mustErr(ctx, toolQuery, map[string]any{}, "argument sql is required")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: "   "}, "argument sql is required")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: 5}, "argument sql must be a string")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: sqlOne, argMaxRows: "many"}, "argument max_rows must be an integer")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: sqlOne, argMaxRows: 1.5}, "argument max_rows must be an integer")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: sqlOne, argMaxRows: 0}, "argument max_rows must be at least 1")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: sqlOne, "timeout_seconds": 31}, "must not exceed the database statement timeout of 30 seconds")
	h.mustErr(ctx, toolQuery, map[string]any{argSQL: sqlOne, "timeout_seconds": -1}, "timeout_seconds must be at least 1")
	h.mustErr(ctx, "timescale_explain", map[string]any{argSQL: sqlOne, "format": "yaml"}, "argument format must be text or json")
	h.mustErr(ctx, "timescale_explain", map[string]any{argSQL: sqlOne, "analyze": "yes"}, "argument analyze must be a boolean")
	h.mustErr(ctx, "timescale_list_chunks", map[string]any{argSchema: schemaPublic, argHypertable: "m", argLimit: 0}, "limit must be between 1 and 1000")
	h.mustErr(ctx, "timescale_list_chunks", map[string]any{argSchema: schemaPublic}, "argument hypertable is required")
	h.mustErr(ctx, "timescale_describe_table", map[string]any{argTable: "m"}, "argument schema is required")
	h.mustErr(ctx, "timescale_sample_rows", map[string]any{argSchema: schemaPublic, argTable: "m", argLimit: 0}, "limit must be at least 1")
	h.mustErr(ctx, "timescale_list_tables", map[string]any{"include_views": "no"}, "argument include_views must be a boolean")
}

func TestSQLRejectedBeforeAnyConnection(t *testing.T) {
	ctx := alice()
	h := newHarness(t, Deps{Registry: testRegistry(t, false)}, false)
	for sql, mention := range map[string]string{
		"DELETE FROM t": "got DELETE",
		"WITH d AS (DELETE FROM t) SELECT * FROM d": "DELETE is not allowed",
		sqlSetRW:                             "SET",
		"SELECT 1; SELECT 2":                 "multiple statements",
		"SELECT set_config('x', 'y', false)": "set_config",
		"SELECT * FROM t FOR UPDATE":         "row locking",
		"":                                   "argument sql is required",
	} {
		h.mustErr(ctx, toolQuery, map[string]any{argSQL: sql}, mention)
	}
	h.mustErr(ctx, "timescale_explain", map[string]any{argSQL: "SHOW all"}, "can be explained")
	h.mustErr(ctx, "timescale_explain", map[string]any{argSQL: "DELETE FROM t"}, "got DELETE")
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if got := callerName(server.Caller{Subject: "s"}); got != "s" {
		t.Errorf("callerName without email = %q", got)
	}
	if got := errorClass(ErrNoCallerIdentity); got != "no_caller" {
		t.Errorf("errorClass = %q", got)
	}
	if got := errorClass(&forbiddenError{}); got != "forbidden" {
		t.Errorf("errorClass = %q", got)
	}
	if got := errorClass(&argError{}); got != "bad_argument" {
		t.Errorf("errorClass = %q", got)
	}
}

func mustRegistry(t *testing.T, dbs []config.Database) *timescale.Registry {
	t.Helper()
	reg, err := timescale.NewRegistry(dbs)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
