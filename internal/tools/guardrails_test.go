package tools

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// Each fact below is in a description for the reason its message names.
// Reword a description freely, but keep the fact. Before cutting any other
// sentence, find its pull request with git log -S'<sentence>' and read why
// it is there.

func registeredTool(t *testing.T, s *mcpsrv.MCPServer, name string) mcp.Tool {
	t.Helper()
	st := s.GetTool(name)
	if st == nil {
		t.Fatalf("no tool %s", name)
	}
	return st.Tool
}

func paramDescription(t *testing.T, tool mcp.Tool, param string) string {
	t.Helper()
	prop, ok := tool.InputSchema.Properties[param].(map[string]any)
	if !ok {
		t.Fatalf("%s has no parameter %s", tool.Name, param)
	}
	d, _ := prop["description"].(string)
	return d
}

// TestDatabaseArgSaysWhenItIsRequired: with several databases configured,
// models left database out while its description called it optional (#42).
func TestDatabaseArgSaysWhenItIsRequired(t *testing.T) {
	d := paramDescription(t, registeredTool(t, docsServer(t), toolQuery), argDatabase)
	if !strings.Contains(d, "required on every call") {
		t.Errorf("database lost a fact from #42: with several databases it is required on every call, or models leave it out: %q", d)
	}
}

// TestPagedListingsSayHowToGetSizes: sizing every hypertable of a large
// catalog spent the whole statement timeout, so the listings size small
// pages only (#12).
func TestPagedListingsSayHowToGetSizes(t *testing.T) {
	s := docsServer(t)
	for _, name := range []string{toolTables, toolHypers} {
		tool := registeredTool(t, s, name)
		for fact, why := range map[string]string{
			"by schema and name":           "limit and offset page through that order",
			"the result says when sizes":   "large pages come without sizes, and the result says how to get them",
			"timescale_describe_table has": "one hypertable's detailed size comes from timescale_describe_table",
		} {
			if !strings.Contains(tool.Description, fact) {
				t.Errorf("%s lost a fact from #12: %s", name, why)
			}
		}
		if d := paramDescription(t, tool, argSizes); !strings.Contains(d, "costs time") {
			t.Errorf("%s.include_sizes lost a fact from #12: true costs time proportional to the page's chunks: %q", name, d)
		}
	}
}

// TestInsertRowNamesItsWriteSurface: the description lists the writable
// tables, so a model knows the whole write surface up front (#43).
func TestInsertRowNamesItsWriteSurface(t *testing.T) {
	d := registeredTool(t, docsServer(t), toolInsertRow).Description
	for fact, why := range map[string]string{
		"the only write":            "no other tool writes",
		"notes: public.annotations": "the writable tables, by database",
		"timescale_describe_table":  "read the columns before inserting",
	} {
		if !strings.Contains(d, fact) {
			t.Errorf("%s lost a fact from #43: %s", toolInsertRow, why)
		}
	}
}
