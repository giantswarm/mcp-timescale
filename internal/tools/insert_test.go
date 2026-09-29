package tools

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

const dbPlant = "plant"

func insertRegistry(t *testing.T) *timescale.Registry {
	t.Helper()
	reg, err := timescale.NewRegistry([]config.Database{
		{Name: dbOpen, Host: "127.0.0.1", Port: 1, DBName: "x", SSLMode: sslDisable, User: "u", Password: "p", MaxRows: 500, StatementTimeout: 30 * time.Second, MaxConnections: 1},
		{Name: dbPlant, Host: "127.0.0.1", Port: 1, DBName: "y", SSLMode: sslDisable, User: "u", Password: "p", MaxRows: 500, StatementTimeout: 30 * time.Second, MaxConnections: 1,
			AllowedGroups: []string{groupA},
			Insert:        &config.Insert{Host: "127.0.0.1", Port: 1, User: "w", Password: "wp", Tables: []string{"werk.cases"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	return reg
}

func TestInsertRowOnlyWithInsertPath(t *testing.T) {
	h := newHarness(t, Deps{Registry: insertRegistry(t)}, true)
	res, err := h.cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var tool *mcp.Tool
	for i := range res.Tools {
		if res.Tools[i].Name == toolInsertRow {
			tool = &res.Tools[i]
		}
	}
	if tool == nil {
		t.Fatalf("%s not registered with an insert path", toolInsertRow)
	}
	a := tool.Annotations
	if a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.IdempotentHint == nil || *a.IdempotentHint {
		t.Errorf("annotations must be write/non-destructive/non-idempotent, got %+v", a)
	}
	if !slices.Contains(tool.InputSchema.Required, argRow) || !slices.Contains(tool.InputSchema.Required, argTable) || !slices.Contains(tool.InputSchema.Required, argDatabase) {
		t.Errorf("required = %v", tool.InputSchema.Required)
	}
	if len(res.Tools) != len(ReadOnlyTools)+len(WriteTools) {
		t.Errorf("registered %d tools, want %d", len(res.Tools), len(ReadOnlyTools)+len(WriteTools))
	}
}

func TestInsertRowRefusesBeforeAnyConnection(t *testing.T) {
	h := newHarness(t, Deps{Registry: insertRegistry(t)}, true)
	ctx := alice(groupA)
	row := map[string]any{"title": "x"}
	h.mustErr(ctx, toolInsertRow, map[string]any{argDatabase: dbOpen, argTable: "werk.cases", argRow: row}, "has no insert path")
	h.mustErr(ctx, toolInsertRow, map[string]any{argDatabase: dbPlant, argTable: "werk.plants", argRow: row}, "not writable")
	h.mustErr(ctx, toolInsertRow, map[string]any{argDatabase: dbPlant, argTable: "werk.cases", argRow: map[string]any{}}, "no columns")
	h.mustErr(ctx, toolInsertRow, map[string]any{argDatabase: dbPlant, argTable: "werk.cases", argRow: map[string]any{`title"); DROP TABLE x; --`: "y"}}, "column name")
	h.mustErr(alice(), toolInsertRow, map[string]any{argDatabase: dbPlant, argTable: "werk.cases", argRow: row}, "not allowed")
}
