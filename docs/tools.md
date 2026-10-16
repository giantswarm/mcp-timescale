# The tools

Generated from the registered tools (`make tools-doc`), for a server with two databases, one of them with an insert path. With one database, `database` is optional; `timescale_insert_row` is registered only when a database has an insert path, and its description names the writable tables.

| Tool | Kind |
|---|---|
| `timescale_describe_table` | read-only |
| `timescale_explain` | read-only |
| `timescale_get_database_info` | read-only |
| `timescale_insert_row` | write |
| `timescale_list_chunks` | read-only |
| `timescale_list_continuous_aggregates` | read-only |
| `timescale_list_databases` | read-only |
| `timescale_list_hypertables` | read-only |
| `timescale_list_jobs` | read-only |
| `timescale_list_schemas` | read-only |
| `timescale_list_tables` | read-only |
| `timescale_query` | read-only |
| `timescale_sample_rows` | read-only |

## `timescale_describe_table`

```text
Describe a table, view or hypertable: columns, keys, indexes and, for TimescaleDB objects, their settings and policies.

Also foreign keys and a view's definition. Hypertables add the time column, chunk interval, chunk count, compression settings, detailed size and retention/compression/reorder policies; continuous aggregates add the definition, source hypertable and refresh policy.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "schema": {
      "description": "Schema name, e.g. public.",
      "type": "string"
    },
    "table": {
      "description": "Table, view or hypertable name.",
      "type": "string"
    }
  },
  "required": [
    "database",
    "schema",
    "table"
  ],
  "type": "object"
}
```

## `timescale_explain`

```text
Show a SELECT-like statement's execution plan, to check chunk exclusion and index use.

Runs EXPLAIN (ANALYZE <analyze>, BUFFERS <analyze>, FORMAT <format>). With analyze the statement runs (read-only) and real timings are reported. A bounded time range should scan few chunks.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "analyze": {
      "default": false,
      "description": "Execute the statement and report actual timings and buffers (default false).",
      "type": "boolean"
    },
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "format": {
      "default": "text",
      "description": "Plan format: text (default) or json.",
      "enum": [
        "text",
        "json"
      ],
      "type": "string"
    },
    "sql": {
      "description": "The SELECT, WITH ... SELECT, VALUES or TABLE statement to explain.",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Statement timeout for this call in seconds; at most the database statement timeout.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "database",
    "sql"
  ],
  "type": "object"
}
```

## `timescale_get_database_info`

```text
Show one database's server facts: versions, TimescaleDB license, role, size, timezone, extensions and object counts.

License apache means no compression or continuous-aggregate policies.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    }
  },
  "required": [
    "database"
  ],
  "type": "object"
}
```

## `timescale_insert_row`

```text
Insert one row into a writable table and commit it: the only write this server offers.

No UPDATE, DELETE or DDL, and only these tables (by database: notes: public.annotations). row maps column names to values; PostgreSQL converts them to the column types (timestamps as RFC 3339 strings, arrays as JSON arrays, json/jsonb columns as JSON values). Columns left out keep their defaults. Runs as a role that may only INSERT into these tables, attributed to you via application_name. Read the table's columns first with timescale_describe_table.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "row": {
      "description": "Column name to value. Column names are lower-case identifiers.",
      "properties": {},
      "type": "object"
    },
    "table": {
      "description": "Schema-qualified writable table, for example werk.cases.",
      "type": "string"
    }
  },
  "required": [
    "database",
    "table",
    "row"
  ],
  "type": "object"
}
```

## `timescale_list_chunks`

```text
List one hypertable's chunks, newest first, with time or integer range, compression state, tablespace and size.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "hypertable": {
      "description": "Hypertable name.",
      "type": "string"
    },
    "limit": {
      "default": 50,
      "description": "Maximum chunks to return (default 50, max 1000).",
      "maximum": 1000,
      "minimum": 1,
      "type": "integer"
    },
    "newest_first": {
      "default": true,
      "description": "Order by range start descending (default true).",
      "type": "boolean"
    },
    "schema": {
      "description": "Schema of the hypertable.",
      "type": "string"
    }
  },
  "required": [
    "database",
    "schema",
    "hypertable"
  ],
  "type": "object"
}
```

## `timescale_list_continuous_aggregates`

```text
List the continuous aggregates (pre-computed time_bucket rollups) with definition, source and refresh policy.

Query them like tables for fast long-range analysis. Empty on the Apache-licensed TimescaleDB build.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    }
  },
  "required": [
    "database"
  ],
  "type": "object"
}
```

## `timescale_list_databases`

```text
List the databases you may use, with reachability and PostgreSQL and TimescaleDB versions. Start here.

The other tools take one of these names as database. Databases whose allowlist excludes you are hidden (hidden_count).
```

```json
{
  "additionalProperties": false,
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `timescale_list_hypertables`

```text
List the hypertables with time column, chunk interval, chunk count and compression, one page at a time.

Ordered by schema and name; hypertables that materialize a continuous aggregate are marked. total_bytes/total_size (hypertable_size: the table plus every chunk, compressed data included) come when the page holds fewer than 50 hypertables or include_sizes is true; the result says when sizes were skipped, and timescale_describe_table has one hypertable's detailed size. Errors when the timescaledb extension is not installed.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "include_sizes": {
      "description": "Compute total_bytes and total_size. Default: true below 50 hypertables on the page. true costs time proportional to the page's chunks.",
      "type": "boolean"
    },
    "limit": {
      "default": 200,
      "description": "Maximum hypertables to return (default 200, max 1000). The listing is ordered by schema, name.",
      "maximum": 1000,
      "minimum": 1,
      "type": "integer"
    },
    "offset": {
      "default": 0,
      "description": "Number of hypertables to skip, for paging (default 0).",
      "minimum": 0,
      "type": "integer"
    },
    "schema": {
      "description": "Restrict to the hypertables of one schema.",
      "type": "string"
    }
  },
  "required": [
    "database"
  ],
  "type": "object"
}
```

## `timescale_list_jobs`

```text
List the TimescaleDB background jobs (policies and user-defined actions) with schedule, target and run statistics.

Retention, compression, reorder and refresh policies, with config, last run status, next start, total runs and failures.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    }
  },
  "required": [
    "database"
  ],
  "type": "object"
}
```

## `timescale_list_schemas`

```text
List the non-system schemas with owner, relation count and comment.

TimescaleDB-internal schemas (_timescaledb_*), which hold the chunks, are hidden; pass one as schema to timescale_list_tables to list it.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    }
  },
  "required": [
    "database"
  ],
  "type": "object"
}
```

## `timescale_list_tables`

```text
List tables and views with kind, owner, row estimate and comment, one page at a time, ordered by schema and name.

is_hypertable and is_continuous_aggregate mark TimescaleDB objects. Without schema, system and _timescaledb_* schemas are skipped. total_bytes/total_size (hypertable_size for hypertables, chunks included) come when the page holds fewer than 50 relations or include_sizes is true; the result says when sizes were skipped, and timescale_describe_table has one hypertable's detailed size.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "include_sizes": {
      "description": "Compute total_bytes and total_size. Default: true below 50 relations on the page. true costs time proportional to the page's chunks.",
      "type": "boolean"
    },
    "include_views": {
      "default": true,
      "description": "Include views and materialized views (default true).",
      "type": "boolean"
    },
    "limit": {
      "default": 200,
      "description": "Maximum relations to return (default 200, max 1000). The listing is ordered by schema, name.",
      "maximum": 1000,
      "minimum": 1,
      "type": "integer"
    },
    "offset": {
      "default": 0,
      "description": "Number of relations to skip, for paging (default 0).",
      "minimum": 0,
      "type": "integer"
    },
    "schema": {
      "description": "Restrict to one schema. Naming a schema also lists internal ones such as _timescaledb_internal.",
      "type": "string"
    }
  },
  "required": [
    "database"
  ],
  "type": "object"
}
```

## `timescale_query`

```text
Run one read-only SQL statement and return columns, rows and whether the result was truncated.

SELECT, WITH ... SELECT, VALUES, TABLE, EXPLAIN or SHOW. Data-modifying statements, SET/RESET, row locks, multiple statements and side-effect functions are refused; the transaction is READ ONLY regardless. Always bound the time range (WHERE ts > now() - interval '1 day') so only the relevant chunks are scanned; time_bucket('5 minutes', ts) AS bucket ... GROUP BY bucket and first()/last() are the TimescaleDB idioms. Values: timestamps as RFC 3339 strings, numerics as strings, bytea as base64.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "max_rows": {
      "default": 100,
      "description": "Rows to return (default 100). Capped by the database max_rows; truncated is true when more rows existed.",
      "minimum": 1,
      "type": "integer"
    },
    "sql": {
      "description": "A single SQL statement. A trailing semicolon is allowed.",
      "type": "string"
    },
    "timeout_seconds": {
      "description": "Statement timeout for this call in seconds; at most the database statement timeout.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "database",
    "sql"
  ],
  "type": "object"
}
```

## `timescale_sample_rows`

```text
Return a table's rows: a hypertable's newest first, any other relation's in storage order.

Identifiers are validated against the catalog. The result has timescale_query's shape.
```

```json
{
  "additionalProperties": false,
  "properties": {
    "database": {
      "description": "Configured database name, required on every call (timescale_list_databases lists them).",
      "type": "string"
    },
    "limit": {
      "default": 20,
      "description": "Rows to return (default 20, capped by the database max_rows).",
      "minimum": 1,
      "type": "integer"
    },
    "schema": {
      "description": "Schema name.",
      "type": "string"
    },
    "table": {
      "description": "Table, view or hypertable name.",
      "type": "string"
    }
  },
  "required": [
    "database",
    "schema",
    "table"
  ],
  "type": "object"
}
```
