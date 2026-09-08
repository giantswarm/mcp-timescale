// Package cmd provides the command-line interface for mcp-timescale.
//
// This package implements a Cobra-based CLI with multiple subcommands:
//   - serve: Starts the MCP server (default behavior when no subcommand is provided)
//   - version: Displays the application version, commit and build time
//   - self-update: Updates the binary to the latest release from GitHub after
//     verifying its cosign Sigstore bundle
//
// Command Structure:
//
//	mcp-timescale [flags]                 # Starts the MCP server (default)
//	mcp-timescale serve [flags]           # Explicitly starts the MCP server
//	mcp-timescale version                 # Shows version information
//	mcp-timescale self-update             # Updates to the latest signed release
//	mcp-timescale help [command]          # Shows help information
//
// The serve command supports multiple transport options:
//   - streamable-http: Streamable HTTP transport (default) - for HTTP-based integration
//   - sse: Server-Sent Events over HTTP - for web-based clients
//   - stdio: Standard input/output - for command-line integration (runs as caller "local")
//
// Transport Configuration Examples:
//
//	mcp-timescale serve --transport stdio
//	mcp-timescale serve --transport sse --mcp-addr :8080
//	mcp-timescale serve --transport streamable-http --mcp-addr :8080 --metrics-addr :9091
//
// There is no write mode: every tool is read-only by construction (see
// internal/tools.ReadOnlyTools and docs/ARCHITECTURE.md), so the serve
// command has no safety switch to flip.
package cmd
