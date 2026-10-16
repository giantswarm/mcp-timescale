package tools

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

// docsServer registers every tool: two databases, so database is required,
// and one with an insert path, so timescale_insert_row is registered.
func docsServer(t *testing.T) *mcpsrv.MCPServer {
	t.Helper()
	db := func(name string) config.Database {
		return config.Database{Name: name, Host: "127.0.0.1", Port: 1, DBName: name, SSLMode: sslDisable, User: "u", Password: "p",
			MaxRows: 500, StatementTimeout: 30 * time.Second, MaxConnections: 1}
	}
	notes := db("notes")
	notes.Insert = &config.Insert{Host: "127.0.0.1", Port: 1, User: "w", Password: "p", Tables: []string{"public.annotations"}}
	reg, err := timescale.NewRegistry([]config.Database{db("metrics"), notes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	s := mcpsrv.NewMCPServer("docs", "0")
	Register(s, Deps{Registry: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return s
}

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, docsServer(t),
		"# The tools\n\nGenerated from the registered tools (`make tools-doc`), for a server with two databases, one of them with an insert path. "+
			"With one database, `database` is optional; `"+toolInsertRow+"` is registered only when a database has an insert path, and its description names the writable tables.",
		"../../docs/tools.md")
}

// TestToolDescriptionBudgets holds the registered descriptions to the
// budget of giantswarm/giantswarm#38592.
func TestToolDescriptionBudgets(t *testing.T) {
	if err := tooldesc.CheckServer(docsServer(t), tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
}
