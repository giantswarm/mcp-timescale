package cmd

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/giantswarm/mcp-toolkit/brand"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/server"
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

func TestRunServeReturnsListenerFailures(t *testing.T) {
	for _, blockedServer := range []string{"MCP", "observability"} {
		t.Run(blockedServer, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, listener.Close()) })

			previousTransport, previousMCPAddr, previousMetricsAddr, previousDatabasesFile := flagTransport, flagMCPAddr, flagMetricsAddr, flagDatabasesFile
			t.Cleanup(func() {
				flagTransport, flagMCPAddr, flagMetricsAddr, flagDatabasesFile = previousTransport, previousMCPAddr, previousMetricsAddr, previousDatabasesFile
			})
			flagTransport = server.TransportStreamableHTTP
			flagMCPAddr, flagMetricsAddr = "127.0.0.1:0", "127.0.0.1:0"
			if blockedServer == "MCP" {
				flagMCPAddr = listener.Addr().String()
			} else {
				flagMetricsAddr = listener.Addr().String()
			}
			flagDatabasesFile = filepath.Join(t.TempDir(), "absent.yaml")
			for _, name := range []string{config.EnvDatabasesFile, config.EnvDSN, "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
				t.Setenv(name, "")
			}
			t.Setenv("OAUTH_ENABLED", "false")
			t.Setenv("DEBUG", "false")
			t.Setenv("OTEL_LOGS_EXPORTER", "none")
			t.Setenv("OTEL_TRACES_EXPORTER", "none")
			commandContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := &cobra.Command{}
			command.SetContext(commandContext)

			err = runServe(command, nil)
			require.ErrorIs(t, err, syscall.EADDRINUSE)
			require.ErrorContains(t, err, listener.Addr().String())
		})
	}
}
