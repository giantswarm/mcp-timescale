package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-template/internal/example"
)

func registerGetThing(s *mcpsrv.MCPServer, deps Deps) {
	tool := mcp.NewTool("things_get",
		mcp.WithDescription("Fetch one thing by id. Read-only."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Thing id, e.g. thing-3.")),
	)
	s.AddTool(tool, getThingHandler(deps))
}

func getThingHandler(deps Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		t, err := deps.Client.Get(ctx, id)
		if errors.Is(err, example.ErrNotFound) {
			return mcp.NewToolResultError("thing " + id + " not found"), nil
		}
		if err != nil {
			return mcp.NewToolResultError("get thing: " + err.Error()), nil
		}
		body, err := json.Marshal(t)
		if err != nil {
			return mcp.NewToolResultError("marshal thing: " + err.Error()), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}
