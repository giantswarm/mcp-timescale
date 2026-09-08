# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `mcp-timescale self-update` installs the latest GitHub release only after its cosign Sigstore bundle verifies for a CircleCI build of giantswarm/mcp-timescale (`github.com/giantswarm/selfupdate-cosign`); a release without a bundle or a download that does not match its signature is refused and the installed binary stays as it is.
- `pkg/project` carries the build identifiers the generated Makefile and the architect `go-build` job stamp at link time; `mcp-timescale version` and `--version` print the release version (plus commit and build time) instead of `dev`.
- Running the binary without a subcommand starts the server, same as `serve`.
- `tools.ReadOnlyTools` names the single tool class; `TestEveryToolIsReadOnlyAndStrict` compares it with the registered tools and their annotations, and the server logs its write policy (`readOnly=true writeMode=none`) at startup.
- Chart unit tests (`make helm-test`), `make govulncheck`, `make test-vet`, `.golangci.yml` goconst tuning, `.helmignore` and repo cursor rules — the same developer surface as mcp-kubernetes.

### Changed

- Release binaries for linux, darwin and windows on amd64 and arm64 are attached to every GitHub Release next to their signature bundles (generated CI `cli` flavour).

## [0.2.0] - 2026-09-08

### Added

- Read-only TimescaleDB / PostgreSQL MCP server acting on the caller identity: catalog tools (databases, schemas, tables, hypertables, chunks, continuous aggregates, jobs), guarded `timescale_query`, `timescale_explain` and `timescale_sample_rows`, per-database `allowedGroups` / `allowedUsers`, attribution through `application_name`, one audit line per call, and a Helm chart with hardened defaults, OAuth (mcp-oauth, forwarded tokens) and a Gateway API route.
