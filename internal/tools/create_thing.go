package tools

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-template/internal/server"
)

// writeGroup is the Dex / OIDC group claim tools demand for write
// operations. Replace with whatever your IdP populates — `mcp-things-write`
// or similar. Read tools omit this check.
const writeGroup = "mcp-things-write"

func registerCreateThing(s *mcpsrv.MCPServer, deps Deps) {
	tool := mcp.NewTool("things_create",
		mcp.WithDescription("Create a new thing. Write — requires "+writeGroup+" group."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable name for the new thing.")),
	)
	s.AddTool(tool, createThingHandler(deps))
}

func createThingHandler(deps Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Inline authz — no shared middleware in this template. If your
		// server grows more than ~3 write tools repeating this block,
		// promote it into a helper rather than a framework.
		caller, ok := server.CallerFromContext(ctx)
		if !ok {
			return mcp.NewToolResultError("authentication required"), nil
		}
		if !slices.Contains(caller.Groups, writeGroup) {
			return mcp.NewToolResultError("group " + writeGroup + " required to create things"), nil
		}

		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		t, err := deps.Client.Create(ctx, name)
		if err != nil {
			return mcp.NewToolResultError("create thing: " + err.Error()), nil
		}
		body, err := json.Marshal(t)
		if err != nil {
			return mcp.NewToolResultError("marshal thing: " + err.Error()), nil
		}
		deps.Log.Info("thing created", "id", t.ID, "caller", caller.Subject)
		return mcp.NewToolResultText(string(body)), nil
	}
}
