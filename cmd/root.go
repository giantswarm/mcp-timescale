// Package cmd defines the cobra root and subcommands. main.go calls
// Execute(); tests can drive rootCmd.ExecuteContext directly.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// serviceName is the OTEL service.name and the MCP server identifier. It can
// be overridden at build time via
// -ldflags "-X github.com/giantswarm/mcp-timescale/cmd.serviceName=..."
// (a const cannot be -X-overridden).
var serviceName = "mcp-timescale"

// rootCmd is the entry point when the binary is called without a subcommand.
var rootCmd = &cobra.Command{
	Use:   serviceName,
	Short: "Read-only MCP server for TimescaleDB and PostgreSQL databases, acting on the caller identity",
	Long: `mcp-timescale is a Model Context Protocol (MCP) server that gives agents
read-only access to TimescaleDB and PostgreSQL databases: the catalog (schemas,
tables, hypertables, chunks, continuous aggregates, background jobs) and a
guarded SELECT surface. Every call runs as the person behind it, inside a
READ ONLY transaction on a read-only role; there is no write mode.

When run without a subcommand it starts the server (same as 'mcp-timescale serve').`,
	// Cobra would otherwise print the usage text after every handled error.
	SilenceUsage: true,
}

// SetVersion sets the version the root command reports (--version, version,
// self-update). main injects pkg/project.Version().
func SetVersion(v string) { rootCmd.Version = v }

// Execute runs the root command and exits non-zero on error.
func Execute() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("%s version {{.Version}}\n", serviceName))

	// No subcommand: serve, so a plain `mcp-timescale` behaves like the
	// chart's and the Dockerfile's explicit `serve`.
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "serve")
	}

	if err := rootCmd.Execute(); err != nil {
		// Cobra has printed the error; the exit status is what is left to do.
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newSelfUpdateCmd())
}
