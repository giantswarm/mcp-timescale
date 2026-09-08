// Package tools wires the MCP tools this server exposes. Add a new tool by
// dropping a `<verb>_<noun>.go` file with one register* function and
// adding it to Register.
package tools

import (
	"log/slog"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-template/internal/example"
)

// Deps is the bag of dependencies tool handlers need. Keep it small:
// adding a field here means every tool gets to see it, so the bar is high.
type Deps struct {
	Client example.Client
	Log    *slog.Logger
}

// Register installs every tool on s. Stays tiny; per-domain register
// functions live next to the tools they register.
func Register(s *mcpsrv.MCPServer, deps Deps) {
	registerListThings(s, deps)
	registerGetThing(s, deps)
	registerCreateThing(s, deps)
}
