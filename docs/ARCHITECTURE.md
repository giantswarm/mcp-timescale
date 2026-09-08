# Architecture

Top-to-bottom on the request path.

```
client (Claude Desktop / mcp-inspector / Cursor / …)
   │  bearer token
   ▼
HTTP listener  cmd/{MCP-NAME}/serve.go
   │
   ▼
otelhttp wrapper  internal/server/transport.go
   │
   ▼
mux  internal/server/transport.go
 ├── Handler.RegisterOAuthRoutes     /oauth/* + /.well-known/oauth-*
 │                                   (mcp-oauth bundle: flow, discovery, RFC 8414/9728)
 ├── /mcp  (streamable-HTTP)         mcp-oauth ValidateToken → mcp-go transport
 └── /sse, /message  (SSE)           mcp-oauth ValidateToken → mcp-go transport
   │
   ▼
mcp-go server  cmd/{MCP-NAME}/serve.go
   │  WithHTTPContextFunc(PromoteOAuthCaller)
   ▼
tool handler  internal/tools/*.go
   │  CallerFromContext(ctx) → server.Caller
   ▼
domain client  internal/<domain>/client.go
```

A second HTTP server (port 9091 by default) serves `/healthz`, `/readyz`,
and `/metrics`. Health probes share a 2s deadline. Prometheus metrics live
on a package-local registry so tests can construct the server twice
without duplicate-registration panics.

## Why this shape

- **Two HTTP servers, not one.** MCP streaming-HTTP responses are
  intentionally long-lived; setting a `WriteTimeout` to satisfy the
  obs path would kill MCP streams. They get separate `http.Server`s.
- **No shared mcpkit dependency.** Wiring lives in `internal/server/`
  inside the repo. An engineer can read `serve.go` end-to-end and trace
  every helper without leaving the codebase.
- **No tool middleware shipped.** A future server might want
  `Instrument` / `Timeout` / `ResponseCap` / `RequireCaller` from
  `mcp-observability-platform`'s `internal/server/middleware/`. They are
  good patterns but only two of three GS MCP servers use them — copy
  what you need rather than absorb the framework.
