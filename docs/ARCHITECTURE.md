# Architecture

Top-to-bottom on the request path.

```
client (muster / Claude Desktop / Cursor / mcp-inspector)
   │  bearer token: an OIDC id_token forwarded by muster, or an access token
   │  from this server's own OAuth flow
   ▼
HTTP listener  cmd/serve.go
   │
   ▼
otelhttp wrapper + mux  internal/server/transport.go
 ├── /oauth/*, /.well-known/*      mcp-oauth: flow, discovery (RFC 8414 / 9728)
 └── /mcp  (streamable-HTTP)       mcp-oauth ValidateToken → mcp-go transport
   │  WithHTTPContextFunc(PromoteOAuthCaller): UserInfo → context
   ▼
mcp-go server  cmd/serve.go
   │  middleware: timeout (max statement timeout + 15s), responsecap (128 KiB),
   │  strict input schemas + server-side validation, recovery
   ▼
tool handler  internal/tools/*.go
   │  1. CallerFromContext → refuse when absent (stdio / OAuth off: "local")
   │  2. reject unknown arguments, check types and bounds
   │  3. resolve the database, enforce allowedGroups / allowedUsers
   │  4. timescale_query / timescale_explain: sqlguard.Classify(sql)
   │  5. audit log line: tool, caller, database, duration, rows, error class
   ▼
database layer  internal/timescale/
   │  Registry → Database → lazily opened pgxpool (session defaults set on
   │  connect: default_transaction_read_only=on, statement_timeout,
   │  idle_in_transaction_session_timeout)
   │  withTx: BEGIN READ ONLY, READ COMMITTED
   │          set_config('application_name', 'mcp-timescale/<caller>', true)
   │          set_config('statement_timeout', <per-call>, true)
   │          … statements … ROLLBACK (always)
   │  Query: DECLARE … NO SCROLL CURSOR FOR <sql>; FETCH FORWARD max_rows+1
   ▼
TimescaleDB / PostgreSQL (a read-only role)
```

A second HTTP server (port 9091 by default) serves `/healthz`, `/readyz`
and `/metrics`.

## Read-only, in layers

1. **Role.** The configured database role should be a plain reader (for a
   Zalando postgres-operator cluster: a role with `SELECT` on the schemas
   of interest, or a replica service). The server cannot grant itself more.
2. **Session.** Every pooled connection runs `SET default_transaction_read_only
   = on`, a `statement_timeout` and an `idle_in_transaction_session_timeout`
   right after connecting (`AfterConnect`; plain `SET` rather than startup
   parameters so PgBouncer-fronted databases work).
3. **Transaction.** Every tool call is one `BEGIN READ ONLY` transaction that
   is always rolled back. PostgreSQL refuses writes with SQLSTATE 25006 and
   `SET TRANSACTION READ WRITE` with 25001 once the first statement (the
   attribution `set_config`) has run. Because the transaction is rolled back,
   even a session-level `SET` smuggled through `set_config(..., false)` is
   reverted before the connection returns to the pool.
4. **Protocol.** Caller SQL is sent through the extended protocol
   (`QueryExecModeDescribeExec`). PostgreSQL parses exactly one statement per
   message: `SELECT 1; DELETE …` fails with "cannot insert multiple commands
   into a prepared statement". pgx would switch an argument-less `Exec` to the
   simple protocol, so the code path uses `Query` everywhere.
5. **Classifier.** `internal/sqlguard` tokenizes the text outside of strings,
   quoted identifiers and comments and accepts only SELECT, WITH … SELECT,
   VALUES, TABLE, EXPLAIN and SHOW. It rejects data-modifying keywords in
   statement position (CTE bodies, the main statement of a WITH), `SELECT …
   INTO`, row locks (`FOR UPDATE` and friends), a second statement, and calls
   of side-effect functions (`pg_terminate_backend`, `pg_read_file`,
   `set_config`, advisory locks, `dblink`, large objects, WAL and replication
   control, …). It is a heuristic that produces clear errors before any round
   trip; layers 1-4 are the guarantee.
6. **Bounds.** SELECT-like statements run through a `NO SCROLL` cursor and the
   server fetches `max_rows + 1` rows, so at most that many rows leave the
   database no matter what the statement would return. `max_rows` is capped by
   the database's `maxRows`; `timeout_seconds` by its `statementTimeout`. The
   response-cap middleware truncates anything above 128 KiB.

## Identity

The server holds no identity of its own towards the caller: with OAuth
enabled, a request without a validated token never reaches a tool handler
(mcp-oauth returns 401), and a handler that finds no caller in its context
refuses the call instead of running as nobody. The per-database
`allowedGroups` / `allowedUsers` lists are checked on every call;
`timescale_list_databases` shows only what the caller may use.

The database sees the person too: `application_name` is
`mcp-timescale/<email>` for the duration of the transaction, visible in
`pg_stat_activity` and in `log_line_prefix` (`%a`). The structured audit log
of the server records tool, caller, database, duration, row count,
truncation and an error class for every call.

## Why this shape

- **Two HTTP servers, not one.** MCP streaming-HTTP responses are
  intentionally long-lived; a `WriteTimeout` on the observability path
  would kill MCP streams.
- **One pool per database, opened lazily.** A misconfigured or unreachable
  database costs nothing until someone asks for it; `timescale_list_databases`
  probes each database with a 3 s deadline and reports the error class.
- **Objects, not positions, for catalog results.** Introspection tools return
  rows keyed by column name so a model does not have to line up positions.
  `timescale_query` returns `columns` + positional `rows` because that is the
  compact shape for data.
- **JSON in text content, no structuredContent.** muster mirrors
  structuredContent twice; the text content is the carrier every client reads.
