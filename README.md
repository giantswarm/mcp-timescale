[![CircleCI](https://dl.circleci.com/status-badge/img/gh/giantswarm/mcp-timescale/tree/main.svg?style=svg)](https://dl.circleci.com/status-badge/redirect/gh/giantswarm/mcp-timescale/tree/main)

# mcp-timescale

Read-only [MCP](https://modelcontextprotocol.io) server for TimescaleDB and
PostgreSQL databases that **acts on the caller's identity**. An agent gets the
catalog (schemas, tables, hypertables, chunks, continuous aggregates,
background jobs) and a guarded `SELECT` surface; the server gets the person
behind every call and refuses to run as nobody.

Built on [`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go),
[`giantswarm/mcp-oauth`](https://github.com/giantswarm/mcp-oauth) (OAuth 2.1
resource server, token forwarding) and
[`giantswarm/mcp-toolkit`](https://github.com/giantswarm/mcp-toolkit) (logging,
tracing, health, response cap and per-tool timeout middleware);
[`jackc/pgx`](https://github.com/jackc/pgx) talks to PostgreSQL.

## Tools

Every tool is read-only (`readOnlyHint: true`, `destructiveHint: false`,
`idempotentHint: true`, `openWorldHint: false`), has a strict input schema
(unknown arguments are rejected) and returns one JSON document as text
content. Every tool except `timescale_list_databases` takes `database` (the
configured name); it is optional when exactly one database is configured.

| Tool | Arguments | Returns |
| --- | --- | --- |
| `timescale_list_databases` | — | The databases the caller may use: name, description, host, port, dbname, sslmode, `reachable` (3 s probe), PostgreSQL and TimescaleDB versions, `hidden_count` for databases the allowlist excludes. |
| `timescale_get_database_info` | `database` | Server version, TimescaleDB version and license, current role, `transaction_read_only`, database size, timezone, server time, installed extensions, number of hypertables / continuous aggregates / jobs. |
| `timescale_list_schemas` | `database` | Non-system schemas with owner, relation count and comment (TimescaleDB-internal schemas hidden). |
| `timescale_list_tables` | `database`, `schema?`, `include_views?` (true) | Tables and views with kind, owner, row estimate, total size (`hypertable_size` for hypertables), `is_hypertable`, `is_continuous_aggregate`, comment. |
| `timescale_describe_table` | `database`, `schema`, `table` | Columns (type, nullable, default, comment), primary key, indexes, foreign keys, view definition; for hypertables the dimensions (time column, chunk interval), chunk count, compression settings, detailed size and policies; for continuous aggregates the definition, source hypertable and refresh policy. |
| `timescale_list_hypertables` | `database` | Hypertables with owner, dimensions, chunk count, compression flag, time column and chunk interval, total size; materialization hypertables of continuous aggregates are marked. |
| `timescale_list_chunks` | `database`, `schema`, `hypertable`, `limit?` (50), `newest_first?` (true) | Chunks with time / integer range, compression state, tablespace, creation time and size. |
| `timescale_list_continuous_aggregates` | `database` | Continuous aggregates with definition, source and materialization hypertable, `materialized_only`, compression and refresh policy. Empty on the Apache-licensed build. |
| `timescale_list_jobs` | `database` | Background jobs (retention, compression, reorder, refresh policies, user-defined actions) with schedule, config, target and statistics. |
| `timescale_query` | `database`, `sql`, `max_rows?` (100), `timeout_seconds?` | One `SELECT` / `WITH … SELECT` / `VALUES` / `TABLE` / `EXPLAIN` / `SHOW` statement: `{database, columns:[{name,type}], rows:[[…]], row_count, truncated, duration_ms}`. |
| `timescale_explain` | `database`, `sql`, `analyze?` (false), `format?` (text) | `EXPLAIN (ANALYZE, BUFFERS, FORMAT …)` of a SELECT-like statement; the plan as text or parsed JSON. |
| `timescale_sample_rows` | `database`, `schema`, `table`, `limit?` (20) | The newest rows of a table (hypertables ordered by their time dimension); identifiers are validated against the catalog first. |

Values are encoded for a model: timestamps as RFC 3339 strings (`timestamp`
without zone and `date` keep their zone-less form), `numeric` as strings (no
float rounding), `bytea` as base64, `uuid` / `inet` / `interval` as text,
`NaN` / `±Infinity` as strings, `json` / `jsonb` and arrays as JSON values.

The server also publishes instructions that tell a model how to approach
time-series data here: `time_bucket()` over a bounded time range, continuous
aggregates for long ranges, `timescale_describe_table` for the time column and
chunk interval.

## Authentication and the caller's identity

With `OAUTH_ENABLED=true` (the chart default) mcp-timescale is an OAuth 2.1
resource server built on [mcp-oauth](https://github.com/giantswarm/mcp-oauth).
Two kinds of bearer are accepted on `/mcp`:

- an OIDC ID token forwarded by an aggregator such as
  [muster](https://github.com/giantswarm/muster) (`auth.forwardToken: true` on
  its `MCPServer` CR), when its audience is listed in
  `OAUTH_TRUSTED_AUDIENCES` — single sign-on, no state on this server;
- an access token issued by mcp-timescale's own authorization server after an
  interactive login at the configured identity provider (Dex).

Either way the server **acts on the caller's identity**:

- A tool call without a validated identity is refused
  (`no caller identity: this server acts on the caller's identity and refuses
  anonymous calls`). There is no fallback identity.
- Each database carries an optional allowlist (`allowedGroups`,
  `allowedUsers`); the caller must match one entry when either list is set.
  `timescale_list_databases` shows only databases the caller may use.
- Every statement runs in a `READ ONLY` transaction on a connection whose
  session defaults are read-only, and the transaction is always rolled back.
  The database role itself should be a reader.
- The database sees the person: `application_name` is
  `mcp-timescale/<email>` for the duration of the transaction (visible in
  `pg_stat_activity` and with `%a` in `log_line_prefix`), and the server logs
  one structured audit line per call with tool, caller, database, duration,
  rows, truncation and error class.

Read-only is enforced in layers — role, session defaults, `READ ONLY`
transaction, single-statement extended protocol, and a SQL classifier that
refuses data-modifying keywords, `SET`/`RESET`, `SELECT … INTO`, row locks,
multiple statements and side-effect functions (`pg_terminate_backend`,
`pg_read_file`, `set_config`, advisory locks, `dblink`, …) with a clear
message before any round trip. Rows are bounded by a server-side cursor
(`max_rows + 1` rows leave the database, never more), `max_rows` by the
database's `maxRows`, `timeout_seconds` by its `statementTimeout`. See
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

Without OAuth (`--transport stdio` or `OAUTH_ENABLED=false`) every call runs as
the caller `local`; the server logs a warning at startup. Use that for local
development only.

## Configuration

### Databases

Databases come from a YAML file, `MCP_TIMESCALE_DATABASES_FILE`
(`--databases-file`, default `/etc/mcp-timescale/databases.yaml`):

```yaml
databases:
  - name: demo                        # tool-facing id, [a-z0-9-]{1,63}
    description: Factory demo data     # optional, shown by list_databases
    host: timescale-demo-repl.timescale-demo.svc.cluster.local
    port: 5432
    dbname: timescaledb
    sslmode: require                   # disable | require | verify-ca | verify-full
    sslRootCertFile: ""                # optional CA bundle for verify-*
    usernameFile: /etc/mcp-timescale/secrets/demo/username   # or username: reader
    passwordFile: /etc/mcp-timescale/secrets/demo/password   # or passwordEnv: DEMO_PASSWORD
    allowedGroups: []                  # optional; empty lists = every authenticated caller
    allowedUsers: []                   # optional emails (case-insensitive)
    maxRows: 500                       # hard cap for rows returned (default 500, max 5000)
    statementTimeout: 30s              # default 30s, max 10m
    maxConnections: 4                  # pool size (default 4)
```

Unknown keys, duplicate names, missing credentials and out-of-range values
fail startup. Passwords are read from the file or environment variable at
startup and never logged. An empty inventory is allowed (the server starts and
reports that nothing is configured) so a chart can be installed before its
databases exist.

For local use `MCP_TIMESCALE_DSN=postgres://…` adds a database named
`default` without a file.

### Environment

Every knob is an env var; flags override. The OAuth knobs come straight from
[`mcp-oauth/oauthconfig`](https://pkg.go.dev/github.com/giantswarm/mcp-oauth/oauthconfig).

| Variable | Default | Purpose |
| --- | --- | --- |
| `MCP_TIMESCALE_DATABASES_FILE` | `/etc/mcp-timescale/databases.yaml` | Database inventory (see above) |
| `MCP_TIMESCALE_DSN` | — | Single database `default` for local runs |
| `MCP_TRANSPORT` | streamable-http | stdio \| sse \| streamable-http |
| `MCP_ADDR` | :8080 | MCP HTTP listener |
| `METRICS_ADDR` | :9091 | /metrics, /healthz, /readyz |
| `DEBUG` | false | Debug logging |
| `OAUTH_ENABLED` | false | Set true in production |
| `OAUTH_PROVIDER` | — | dex \| google \| github |
| `OAUTH_ISSUER` | — | This server's own /oauth/* base URL |
| `OAUTH_DEX_ISSUER_URL` | — | Upstream Dex issuer (provider=dex) |
| `OAUTH_DEX_CLIENT_ID` / `OAUTH_DEX_CLIENT_SECRET[_FILE]` | — | Dex client |
| `OAUTH_TRUSTED_AUDIENCES` | — | Audiences of forwarded ID tokens (muster's Dex client ID) |
| `OAUTH_STORAGE_BACKEND` | memory | memory \| valkey (valkey for >1 replica with interactive logins) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | Tracing; empty disables |

The per-tool timeout middleware is sized from the largest configured
`statementTimeout` (+15 s, at least 45 s) so it never fires before PostgreSQL
does.

## Local quickstart

```bash
docker run -d --name tsdb -e POSTGRES_PASSWORD=pw -p 127.0.0.1:5432:5432 timescale/timescaledb:2.29.2-pg17

export MCP_TIMESCALE_DSN='postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable'
go run . serve --transport stdio            # Claude Desktop / Cursor: stdio, caller "local"
OAUTH_ENABLED=false go run . serve          # streamable-HTTP on :8080 for mcp-inspector
```

Or against a real cluster, with the same read-only role the chart would use:

```bash
kubectl -n timescale-demo port-forward svc/timescale-demo-repl 5432:5432 &
cat > /tmp/databases.yaml <<EOF
databases:
  - name: demo
    host: 127.0.0.1
    dbname: timescaledb
    sslmode: require
    username: timescaledb_data_reader_user
    passwordEnv: DEMO_PASSWORD
EOF
DEMO_PASSWORD=$(kubectl -n timescale-demo get secret timescaledb-data-reader-user.timescale-demo.credentials.postgresql.acid.zalan.do -o jsonpath='{.data.password}' | base64 -d) \
  go run . serve --transport stdio --databases-file /tmp/databases.yaml
```

`make test` runs the unit tests; `make test-integration` starts a throwaway
TimescaleDB container and runs the integration suite (hypertable, continuous
aggregate, policies, truncation, refused writes, attribution in
`pg_stat_activity`, statement timeout). `MCP_TIMESCALE_TEST_DSN` points it at
an existing database instead.

## Kubernetes

The chart in `helm/mcp-timescale` ships hardened defaults: non-root,
read-only root filesystem, no capabilities, no ServiceAccount token, no RBAC
(the server never talks to the Kubernetes API), ServiceMonitor on, OAuth on.
Database credentials are mounted from Secrets (0440) and referenced by path
from the generated `databases.yaml`; nothing secret lands in a ConfigMap.

```yaml
oauth:
  issuerURL: https://mcp-timescale.example.com
  dex:
    issuerURL: https://dex.example.com
    clientIDSecretRef: {name: mcp-timescale-dex, key: client-id}
    clientSecretSecretRef: {name: mcp-timescale-dex, key: client-secret}
  trustedAudiences:
    - <muster's Dex client ID>          # forwarded ID tokens

databases:
  - name: demo
    description: Factory demo data
    host: timescale-demo-repl.timescale-demo.svc.cluster.local   # the replica service
    dbname: timescaledb
    sslmode: require
    secretRef:
      # Zalando postgres-operator credentials Secret of a reader role.
      name: timescaledb-data-reader-user.timescale-demo.credentials.postgresql.acid.zalan.do
      usernameKey: username
      passwordKey: password
    allowedGroups: [data-analysts]
    maxRows: 500
    statementTimeout: 30s

gatewayAPI:                              # expose through an existing Gateway
  enabled: true
  httpRoute:
    parentRefs:
      - name: giantswarm-default
        namespace: envoy-gateway-system
    hostnames: [mcp-timescale.example.com]
  backendTrafficPolicy:
    enabled: true                        # Envoy Gateway: no request timeout on MCP streams
```

The `HTTPRoute` forwards `/`, so `/mcp`, `/oauth/*` and `/.well-known/*` share
the Service. `storage.kind` defaults to `memory` and `replicas` to `1`: with
forwarded tokens no OAuth state needs sharing. Interactive logins across more
than one replica need `storage.kind: valkey`. `networkPolicy.enabled` is off by
default because the databases and Dex are reached by DNS name; the values file
documents the egress rules to add when enabling it (TCP 5432 to the database
namespaces, TCP 443 for Dex JWKS).

### Registering with muster

muster forwards the session's IdP ID token byte-identical when the
`MCPServer` CR carries a `forwardToken` auth block; the server validates it
against Dex's JWKS and requires the audience to be in
`oauth.trustedAudiences`:

```yaml
apiVersion: muster.giantswarm.io/v1alpha1
kind: MCPServer
metadata:
  name: timescale
spec:
  type: streamable-http
  url: https://mcp-timescale.example.com/mcp
  description: Read-only TimescaleDB access, acting as the caller
  auth:
    type: oauth
    forwardToken: true
```

Tools then appear in muster with the `x_timescale_` prefix
(`x_timescale_timescale_query`, …) and every call reaches the database as the
person who is logged in to muster.

## Development

| Path | Purpose |
| --- | --- |
| `cmd/` | cobra entry — `serve.go` wires config, pools, tools and transports |
| `internal/config/` | `databases.yaml` loading, validation, allowlists |
| `internal/sqlguard/` | read-only SQL classifier (tokenizer + keyword rules) |
| `internal/timescale/` | registry of pools, attributed read-only sessions, cursor-bounded queries, catalog introspection, value encoding |
| `internal/tools/` | the MCP tools, argument validation, identity and allowlist checks, audit log |
| `internal/server/` | OAuth wiring, transport mux, caller context, /metrics |
| `helm/mcp-timescale/` | Helm chart |
| `scripts/integration-test.sh` | Docker-backed integration run (`make test-integration`) |

`go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run` and
`pre-commit run --all-files` are expected to pass; `make helm-lint` and
`make helm-template` render the chart with `helm/mcp-timescale/ci/ci-values.yaml`.

## Releasing

Releases are cut automatically from Conventional Commit PR titles (`feat:`,
`fix:`, `feat!:`); the CircleCI pipeline pushes the multi-arch image to gsoci
and the chart to the Giant Swarm catalog.

## Security

Report vulnerabilities per `SECURITY.md`. The image is distroless, runs as
non-root with `readOnlyRootFilesystem: true`, drops all Linux capabilities and
mounts no ServiceAccount token. The server holds read-only database
credentials only and cannot escalate them: every statement is confined to a
read-only transaction on a read-only session.
