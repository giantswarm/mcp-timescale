package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/sqlguard"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

// The integration suite needs a TimescaleDB reachable through
// MCP_TIMESCALE_TEST_DSN (superuser, the DSN owns the test schema).
// `make test-integration` starts one in Docker and runs it.
const (
	integrationEnv = "MCP_TIMESCALE_TEST_DSN"
	itDatabase     = "it"
	itMaxRows      = 50
	itMetrics      = "metrics"
	itHourly       = "metrics_hourly"
	sqlDelete      = "DELETE FROM metrics"
	sqlTwoStmts    = "SELECT 1; DELETE FROM metrics"
)

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(integrationEnv)
	if dsn == "" {
		t.Skipf("%s is not set; run `make test-integration` to start a TimescaleDB container and execute this suite", integrationEnv)
	}
	return dsn
}

// adminConn is a plain connection with the DSN's own privileges, used to
// create fixtures and to observe pg_stat_activity.
func adminConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect %s: %v", integrationEnv, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func setupFixtures(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		"CREATE EXTENSION IF NOT EXISTS timescaledb",
		"DROP MATERIALIZED VIEW IF EXISTS public.metrics_hourly",
		"DROP TABLE IF EXISTS public.metrics",
		"DROP TABLE IF EXISTS public.moods",
		"DROP TYPE IF EXISTS public.mood",
		`CREATE TABLE public.metrics (
			ts timestamptz NOT NULL, device text NOT NULL, value double precision, payload jsonb,
			amount numeric(12,4), id uuid DEFAULT gen_random_uuid(), raw bytea, tags text[])`,
		"COMMENT ON TABLE public.metrics IS 'integration fixture'",
		"SELECT create_hypertable('public.metrics', 'ts', chunk_time_interval => interval '1 day')",
		`INSERT INTO public.metrics (ts, device, value, payload, amount, raw, tags)
			SELECT now() - (n || ' minutes')::interval - CASE WHEN n % 3 = 2 THEN interval '2 days' ELSE interval '0' END,
				'dev-' || (n % 3), n::double precision / 7, jsonb_build_object('n', n),
				n * 1.5, decode('cafe', 'hex'), ARRAY['a', 'b']
			FROM generate_series(1, 300) AS n`,
		"ALTER TABLE public.metrics SET (timescaledb.compress, timescaledb.compress_segmentby = 'device')",
		"SELECT add_compression_policy('public.metrics', interval '7 days')",
		"SELECT add_retention_policy('public.metrics', interval '90 days')",
		`CREATE MATERIALIZED VIEW public.metrics_hourly WITH (timescaledb.continuous) AS
			SELECT time_bucket('1 hour', ts) AS bucket, device, avg(value) AS avg_value FROM public.metrics GROUP BY 1, 2 WITH NO DATA`,
		"SELECT add_continuous_aggregate_policy('public.metrics_hourly', start_offset => interval '1 month', end_offset => interval '1 hour', schedule_interval => interval '1 hour')",
		"CREATE TYPE public.mood AS ENUM ('ok', 'bad')",
		"CREATE TABLE public.moods (m public.mood)",
		"INSERT INTO public.moods VALUES ('ok'), ('bad')",
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
}

func integrationHarness(t *testing.T, dsn string) (*harness, *timescale.Database) {
	t.Helper()
	reg, err := timescale.NewRegistry([]config.Database{{
		Name: itDatabase, Description: "integration", DSN: dsn,
		MaxRows: itMaxRows, StatementTimeout: 5 * time.Second, MaxConnections: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	db, _ := reg.Get(itDatabase)
	return newHarness(t, Deps{Registry: reg}, true), db
}

func TestIntegration(t *testing.T) {
	dsn := integrationDSN(t)
	admin := adminConn(t, dsn)
	setupFixtures(t, admin)
	h, db := integrationHarness(t, dsn)
	ctx := alice()

	t.Run("list_databases", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, toolList, nil)
		items := out["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items = %v", items)
		}
		item := items[0].(map[string]any)
		if item["reachable"] != true || item["timescaledb_version"] == "" || !strings.HasPrefix(item["postgres_version"].(string), "17") {
			t.Errorf("probe = %v", item)
		}
	})

	t.Run("get_database_info", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_get_database_info", nil)
		if out["transaction_read_only"] != "on" || out["timescaledb_installed"] != true || out["hypertables"].(float64) < 1 || out["jobs"].(float64) < 3 {
			t.Errorf("info = %v", out)
		}
	})

	t.Run("list_schemas", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_list_schemas", nil)
		names := namesOf(out["items"], "name")
		if !containsString(names, schemaPublic) || containsAny(names, "_timescaledb_internal", "pg_catalog", "information_schema") {
			t.Errorf("schemas = %v", names)
		}
	})

	t.Run("list_tables", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_list_tables", map[string]any{argSchema: schemaPublic})
		byName := indexBy(out["items"], "name")
		m, ok := byName[itMetrics]
		if !ok || m["is_hypertable"] != true || m["kind"] != "table" || m["comment"] != "integration fixture" || m["total_bytes"].(float64) <= 0 {
			t.Errorf("metrics = %v", m)
		}
		if c, ok := byName[itHourly]; !ok || c["is_continuous_aggregate"] != true || c["kind"] != "view" {
			t.Errorf("metrics_hourly = %v", c)
		}
		noViews := h.mustOK(ctx, "timescale_list_tables", map[string]any{argSchema: schemaPublic, "include_views": false})
		if _, ok := indexBy(noViews["items"], "name")[itHourly]; ok {
			t.Error("include_views=false must drop the continuous aggregate view")
		}
		internal := h.mustOK(ctx, "timescale_list_tables", map[string]any{argSchema: "_timescaledb_internal"})
		if internal["count"].(float64) < 1 {
			t.Error("naming the internal schema must list chunks")
		}
	})

	t.Run("describe_hypertable", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_describe_table", map[string]any{argSchema: schemaPublic, argTable: itMetrics})
		cols := namesOf(out["columns"], "name")
		if !containsString(cols, "ts") || !containsString(cols, "payload") {
			t.Errorf("columns = %v", cols)
		}
		ht, ok := out["hypertable"].(map[string]any)
		if !ok {
			t.Fatalf("hypertable details missing: %v", out)
		}
		dims := ht["dimensions"].([]any)
		if len(dims) != 1 || dims[0].(map[string]any)["column_name"] != "ts" || dims[0].(map[string]any)["time_interval"] != "1 day" {
			t.Errorf("dimensions = %v", dims)
		}
		if ht["num_chunks"].(float64) < 2 || ht["compression_enabled"] != true {
			t.Errorf("hypertable = %v", ht)
		}
		if cs, ok := ht["compression_settings"].(map[string]any); !ok || cs["segmentby"] != "device" {
			t.Errorf("compression_settings = %v", ht["compression_settings"])
		}
		procs := namesOf(ht["policies"], "proc_name")
		if !containsString(procs, "policy_retention") || !containsString(procs, "policy_compression") {
			t.Errorf("policies = %v", procs)
		}
		if size, ok := ht["size"].(map[string]any); !ok || size["total_bytes"].(float64) <= 0 {
			t.Errorf("size = %v", ht["size"])
		}
		idx := namesOf(out["indexes"], "name")
		if len(idx) == 0 {
			t.Error("hypertable must report its time index")
		}
	})

	t.Run("describe_continuous_aggregate", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_describe_table", map[string]any{argSchema: schemaPublic, argTable: itHourly})
		ca, ok := out["continuous_aggregate"].(map[string]any)
		if !ok || ca["source_hypertable_name"] != itMetrics || ca["refresh_job_id"] == nil || !strings.Contains(ca["view_definition"].(string), "time_bucket") {
			t.Errorf("continuous_aggregate = %v", out["continuous_aggregate"])
		}
		if out["view_definition"] == nil {
			t.Error("a continuous aggregate is a view and carries a view definition")
		}
	})

	t.Run("describe_plain_table_with_enum", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_describe_table", map[string]any{argSchema: schemaPublic, argTable: "moods"})
		if out["hypertable"] != nil || out["continuous_aggregate"] != nil {
			t.Errorf("plain table must carry no TimescaleDB details: %v", out)
		}
		h.mustErr(ctx, "timescale_describe_table", map[string]any{argSchema: schemaPublic, argTable: nope}, "not found")
	})

	t.Run("list_hypertables", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_list_hypertables", nil)
		m, ok := indexBy(out["items"], "name")[itMetrics]
		if !ok || m["time_column"] != "ts" || m["chunk_time_interval"] != "1 day" || m["compression_enabled"] != true || m["num_chunks"].(float64) < 2 {
			t.Errorf("hypertable = %v", m)
		}
	})

	t.Run("list_chunks", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_list_chunks", map[string]any{argSchema: schemaPublic, argHypertable: itMetrics})
		items := out["items"].([]any)
		if len(items) < 2 {
			t.Fatalf("chunks = %v", items)
		}
		first, second := items[0].(map[string]any), items[1].(map[string]any)
		if first["range_start"].(string) < second["range_start"].(string) {
			t.Errorf("newest_first must order by range_start descending: %v, %v", first["range_start"], second["range_start"])
		}
		if first["is_compressed"] != false || first["total_bytes"].(float64) <= 0 {
			t.Errorf("chunk = %v", first)
		}
		limited := h.mustOK(ctx, "timescale_list_chunks", map[string]any{argSchema: schemaPublic, argHypertable: itMetrics, argLimit: 1, "newest_first": false})
		if limited["count"].(float64) != 1 || limited["items"].([]any)[0].(map[string]any)["range_start"] != items[len(items)-1].(map[string]any)["range_start"] {
			t.Errorf("limit/newest_first=false: %v", limited)
		}
		h.mustErr(ctx, "timescale_list_chunks", map[string]any{argSchema: schemaPublic, argHypertable: nope}, "not found")
	})

	t.Run("list_continuous_aggregates", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_list_continuous_aggregates", nil)
		c, ok := indexBy(out["items"], "view_name")[itHourly]
		if !ok || c["source_hypertable_name"] != itMetrics || c["refresh_job_id"] == nil || c["refresh_schedule_interval"] != "01:00:00" {
			t.Errorf("cagg = %v", c)
		}
	})

	t.Run("list_jobs", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_list_jobs", nil)
		procs := namesOf(out["items"], "proc_name")
		for _, want := range []string{"policy_retention", "policy_compression", "policy_refresh_continuous_aggregate"} {
			if !containsString(procs, want) {
				t.Errorf("jobs %v lack %s", procs, want)
			}
		}
	})

	t.Run("query_rows_and_truncation", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SELECT ts, device, value, payload, amount, id, raw, tags FROM metrics ORDER BY ts DESC;", argMaxRows: 10})
		if out["row_count"].(float64) != 10 || out["truncated"] != true {
			t.Fatalf("row_count=%v truncated=%v", out["row_count"], out["truncated"])
		}
		cols := namesOf(out["columns"], "type")
		if strings.Join(cols, ",") != "timestamptz,text,float8,jsonb,numeric,uuid,bytea,_text" {
			t.Errorf("column types = %v", cols)
		}
		row := out["rows"].([]any)[0].([]any)
		if _, err := time.Parse(time.RFC3339Nano, row[0].(string)); err != nil {
			t.Errorf("timestamptz must be RFC 3339: %v", row[0])
		}
		if _, isMap := row[3].(map[string]any); !isMap {
			t.Errorf("jsonb must be a JSON object: %v", row[3])
		}
		if _, isStr := row[4].(string); !isStr {
			t.Errorf("numeric must be a string: %v (%T)", row[4], row[4])
		}
		if s, _ := row[5].(string); len(s) != 36 {
			t.Errorf("uuid must be canonical text: %v", row[5])
		}
		if row[6] != "yv4=" {
			t.Errorf("bytea must be base64: %v", row[6])
		}
		if tags, ok := row[7].([]any); !ok || len(tags) != 2 {
			t.Errorf("text[] must be a JSON array: %v", row[7])
		}

		// Requests above the database cap are clamped and still flagged.
		out = h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SELECT * FROM metrics", argMaxRows: 5000})
		if out["row_count"].(float64) != itMaxRows || out["truncated"] != true {
			t.Errorf("clamp: row_count=%v truncated=%v", out["row_count"], out["truncated"])
		}
		out = h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SELECT count(*) AS n FROM metrics"})
		if out["rows"].([]any)[0].([]any)[0].(float64) != 300 || out["truncated"] != false {
			t.Errorf("count: %v", out)
		}
		out = h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SELECT time_bucket('1 hour', ts) AS bucket, device, avg(value) FROM metrics WHERE ts > now() - interval '7 days' GROUP BY 1, 2 ORDER BY 1 DESC"})
		if out["row_count"].(float64) < 1 {
			t.Errorf("time_bucket: %v", out)
		}
		out = h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SELECT m FROM moods ORDER BY 1"})
		if namesOf(out["columns"], "type")[0] != "mood" {
			t.Errorf("enum type must be resolved by name: %v", out["columns"])
		}
		out = h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SHOW default_transaction_read_only"})
		if out["rows"].([]any)[0].([]any)[0] != "on" {
			t.Errorf("session default must be read-only: %v", out)
		}
		out = h.mustOK(ctx, toolQuery, map[string]any{argSQL: "EXPLAIN SELECT 1"})
		if out["row_count"].(float64) < 1 {
			t.Errorf("EXPLAIN via query: %v", out)
		}
	})

	t.Run("classifier_refuses_writes", func(t *testing.T) {
		h := h.with(t)
		for sql, mention := range map[string]string{
			"INSERT INTO metrics (ts, device) VALUES (now(), 'x')": "got INSERT",
			"UPDATE metrics SET value = 0":                         "got UPDATE",
			sqlDelete:                                              "got DELETE",
			"WITH d AS (DELETE FROM metrics RETURNING *) SELECT count(*) FROM d": "DELETE is not allowed",
			sqlSetRW:    "got SET",
			sqlTwoStmts: "multiple statements",
			"SELECT set_config('transaction_read_only', 'off', false)": "set_config",
			"SELECT * FROM metrics FOR UPDATE":                         "row locking",
			"TRUNCATE metrics":                                         "got TRUNCATE",
		} {
			h.mustErr(ctx, toolQuery, map[string]any{argSQL: sql}, mention)
		}
	})

	t.Run("postgres_refuses_writes_even_without_classifier", func(t *testing.T) {
		// Bypass the classifier by handing pre-built statements to the
		// database layer: the READ ONLY transaction and the extended
		// protocol are the guarantee.
		bg := context.Background()
		cases := map[string]sqlguard.Statement{
			"delete in read-only tx":           {SQL: sqlDelete, Kind: sqlguard.KindShow},
			"delete through cursor":            {SQL: sqlDelete, Kind: sqlguard.KindSelect},
			"cte delete":                       {SQL: "WITH d AS (DELETE FROM metrics RETURNING *) SELECT count(*) FROM d", Kind: sqlguard.KindWith},
			"set read write":                   {SQL: sqlSetRW, Kind: sqlguard.KindShow},
			"set_config read write":            {SQL: "SELECT set_config('transaction_read_only', 'off', true)", Kind: sqlguard.KindShow},
			"multi statement direct":           {SQL: sqlTwoStmts, Kind: sqlguard.KindShow},
			"multi statement cursor":           {SQL: sqlTwoStmts, Kind: sqlguard.KindSelect},
			"select into":                      {SQL: "SELECT * INTO metrics_copy FROM metrics", Kind: sqlguard.KindShow},
			"create table":                     {SQL: "CREATE TABLE evil (a int)", Kind: sqlguard.KindShow},
			"session statement_timeout escape": {SQL: "SELECT set_config('statement_timeout', '0', false)", Kind: sqlguard.KindShow},
		}
		for name, stmt := range cases {
			res, err := db.Query(bg, aliceEmail, stmt, 10, 0)
			if err == nil {
				if name == "session statement_timeout escape" {
					// set_config(..., false) is a session-level change; the
					// classifier refuses it, and even when bypassed it dies
					// with the transaction's connection state below.
					continue
				}
				t.Errorf("%s: PostgreSQL accepted %q: %+v", name, stmt.SQL, res)
				continue
			}
			t.Logf("%s: %s", name, timescale.Describe(itDatabase, err))
		}
		var n int
		if err := admin.QueryRow(bg, "SELECT count(*) FROM metrics").Scan(&n); err != nil || n != 300 {
			t.Fatalf("fixture rows changed: n=%d err=%v", n, err)
		}
		var exists bool
		if err := admin.QueryRow(bg, "SELECT to_regclass('public.evil') IS NOT NULL OR to_regclass('public.metrics_copy') IS NOT NULL").Scan(&exists); err != nil || exists {
			t.Fatalf("a bypassed statement created a table: exists=%v err=%v", exists, err)
		}
	})

	t.Run("session_state_never_leaks_between_calls", func(t *testing.T) {
		h := h.with(t)
		// After the escape attempts above every pooled connection must
		// still run with the configured statement timeout.
		out := h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SHOW statement_timeout"})
		if got := out["rows"].([]any)[0].([]any)[0]; got != "5s" {
			t.Errorf("statement_timeout = %v, want 5s", got)
		}
	})

	t.Run("explain", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_explain", map[string]any{argSQL: "SELECT * FROM metrics WHERE ts > now() - interval '1 hour'"})
		plan, _ := out["plan"].(string)
		if !strings.Contains(plan, "Scan") || out["analyzed"] != false {
			t.Errorf("text plan = %v", out)
		}
		out = h.mustOK(ctx, "timescale_explain", map[string]any{argSQL: "SELECT count(*) FROM metrics", "analyze": true, "format": "json"})
		doc, ok := out["plan"].([]any)
		if !ok || len(doc) != 1 || doc[0].(map[string]any)["Execution Time"] == nil {
			t.Errorf("json analyze plan = %v", out["plan"])
		}
		h.mustErr(ctx, "timescale_explain", map[string]any{argSQL: "DELETE FROM metrics"}, "got DELETE")
	})

	t.Run("sample_rows", func(t *testing.T) {
		h := h.with(t)
		out := h.mustOK(ctx, "timescale_sample_rows", map[string]any{argSchema: schemaPublic, argTable: itMetrics, argLimit: 5})
		rows := out["rows"].([]any)
		if len(rows) != 5 || out["truncated"] != true {
			t.Fatalf("sample = %v", out)
		}
		if rows[0].([]any)[0].(string) < rows[1].([]any)[0].(string) {
			t.Errorf("hypertable samples must be newest first: %v", rows)
		}
		out = h.mustOK(ctx, "timescale_sample_rows", map[string]any{argSchema: schemaPublic, argTable: "moods"})
		if out["row_count"].(float64) != 2 || out["truncated"] != false {
			t.Errorf("moods = %v", out)
		}
		h.mustErr(ctx, "timescale_sample_rows", map[string]any{argSchema: schemaPublic, argTable: nope}, "not found")
		h.mustErr(ctx, "timescale_sample_rows", map[string]any{argSchema: schemaPublic, argTable: `x"; DROP TABLE metrics; --`}, "not found")
	})

	t.Run("statement_timeout", func(t *testing.T) {
		h := h.with(t)
		h.mustErr(ctx, toolQuery, map[string]any{argSQL: "SELECT pg_sleep(3)", argTimeoutSec: 1}, "statement timeout")
	})

	t.Run("application_name_attribution", func(t *testing.T) {
		h := h.with(t)
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.mustOK(ctx, toolQuery, map[string]any{argSQL: "SELECT pg_sleep(2)"})
		}()
		want := "mcp-timescale/" + aliceEmail
		deadline := time.Now().Add(4 * time.Second)
		seen := false
		for time.Now().Before(deadline) && !seen {
			var n int
			if err := admin.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE application_name = $1 AND state = 'active'", want).Scan(&n); err != nil {
				t.Fatal(err)
			}
			seen = n > 0
			if !seen {
				time.Sleep(50 * time.Millisecond)
			}
		}
		<-done
		if !seen {
			t.Errorf("no active backend with application_name %q while the query ran", want)
		}
		// Outside a tool call the pooled connection is back to the base name.
		var n int
		if err := admin.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", want).Scan(&n); err != nil || n != 0 {
			t.Errorf("attribution must be transaction-local: %d sessions still named %q (err %v)", n, want, err)
		}
	})
}

// with rebinds the harness to a subtest so failures stop that subtest only.
func (h *harness) with(t *testing.T) *harness { return &harness{t: t, cli: h.cli} }

func namesOf(items any, key string) []string {
	list, _ := items.([]any)
	out := make([]string, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]any)
		s, _ := m[key].(string)
		out = append(out, s)
	}
	return out
}

func indexBy(items any, key string) map[string]map[string]any {
	list, _ := items.([]any)
	out := make(map[string]map[string]any, len(list))
	for _, it := range list {
		m, _ := it.(map[string]any)
		if s, ok := m[key].(string); ok {
			out[s] = m
		}
	}
	return out
}

func containsAny(list []string, values ...string) bool {
	for _, v := range values {
		if containsString(list, v) {
			return true
		}
	}
	return false
}
