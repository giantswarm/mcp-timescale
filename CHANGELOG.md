# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Adopt `github.com/giantswarm/mcp-toolkit` v0.1.0 for cross-cutting plumbing. `cmd/serve.go` now imports `logging.New`, `tracing.Init`, `health.New`, `httpx.Run`, `responsecap.New`, and `timeout.New` instead of carrying inline copies. Per-tool middleware (`responsecap` with default 128 KiB cap, `timeout` with 30s default) is wired from day one so every MCP scaffolded from this template inherits both protections.

### Changed

- Bump `github.com/giantswarm/mcp-oauth` to v1.0.0.
- Bump `github.com/mark3labs/mcp-go` v0.49.0 → v0.52.0 to align with the rest of the Giant Swarm Go MCP fleet.
- `Config` no longer carries a `LogFormat` field. Format is auto-selected by `mcp-toolkit/logging` (JSON when `KUBERNETES_SERVICE_HOST` is set, text otherwise). The `LOG_FORMAT` env-var override is dropped — override at the call site in `cmd/serve.go` if a specific MCP needs a fixed format.

### Removed

- `internal/server/{health,logging,tracing}.go` — superseded by `mcp-toolkit/{health,logging,tracing}`.
- `Auth.IssuerHealthURL()` and the matching `oauth-issuer` readiness probe. The toolkit's `health` package follows the principle that `/readyz` should not probe shared downstreams: a transient Dex hiccup would otherwise flip every replica's `/readyz` simultaneously and the Service yanks its last endpoint. Token validation failures continue to surface to individual callers as 401/503.
