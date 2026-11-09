package cmd

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/giantswarm/mcp-toolkit/brand"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestNewMCPServerAdvertisesTheTimescaleIcon(t *testing.T) {
	srv := newMCPServer("0.0.0", time.Minute)

	initReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	raw, err := json.Marshal(srv.HandleMessage(t.Context(), initReq))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result mcp.InitializeResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}

	want := []mcp.Icon{brand.Icon(timescaleLogo)}
	if got := resp.Result.ServerInfo.Icons; !reflect.DeepEqual(got, want) {
		t.Errorf("ServerInfo.Icons = %+v, want %+v", got, want)
	}
}
