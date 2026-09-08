package sqlguard

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyAccepts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sql  string
		kind Kind
		want string // expected normalized SQL; empty = same as input trimmed
	}{
		{"plain select", sqlSelectOne, KindSelect, ""},
		{"lowercase with column named cluster", "select * from metrics where cluster = 'x'", KindSelect, ""},
		{"trailing semicolon and whitespace", "  \n SELECT 1;  \n", KindSelect, sqlSelectOne},
		{"trailing semicolon then comment", "SELECT 1 ; -- done", KindSelect, sqlSelectOne},
		{"leading line comment", "-- comment\nSELECT 1", KindSelect, ""},
		{"nested block comment", "/* block /* nested */ still */ SELECT 1", KindSelect, ""},
		{"comment containing a drop", "select 1 -- ; drop table t", KindSelect, ""},
		{"cte", "WITH x AS (SELECT 1) SELECT * FROM x", KindWith, ""},
		{"recursive cte", "WITH RECURSIVE t(n) AS (VALUES (1) UNION ALL SELECT n+1 FROM t WHERE n < 5) SELECT sum(n) FROM t", KindWith, ""},
		{"recursive cte with SEARCH ... SET", "WITH RECURSIVE g AS (SELECT 1 AS id UNION ALL SELECT id+1 FROM g WHERE id < 3) SEARCH DEPTH FIRST BY id SET ord SELECT * FROM g", KindWith, ""},
		{"explain", "EXPLAIN " + sqlSelectOne, KindExplain, ""},
		{"explain with options", "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT 1", KindExplain, ""},
		{"explain with boolean options", "EXPLAIN (ANALYZE true, BUFFERS false) SELECT 1", KindExplain, ""},
		{"explain analyze legacy", "EXPLAIN ANALYZE VERBOSE SELECT 1", KindExplain, ""},
		{"explain cte", "EXPLAIN WITH x AS (SELECT 1) SELECT * FROM x", KindExplain, ""},
		{"show setting", "SHOW server_version", KindShow, ""},
		{"show all", "SHOW ALL", KindShow, ""},
		{"values", "VALUES (1), (2)", KindValues, ""},
		{"table", "TABLE metrics", KindTable, ""},
		{"table in internal schema", "TABLE _timescaledb_internal._hyper_1_1_chunk", KindTable, ""},
		{"keywords inside string", "SELECT 'DELETE FROM t; DROP TABLE x' AS s", KindSelect, ""},
		{"keyword as quoted identifier", `SELECT "delete", "set" FROM t`, KindSelect, ""},
		{"dollar quoted string", "SELECT $$; DELETE FROM t$$", KindSelect, ""},
		{"tagged dollar quoted string", "SELECT 'x' || $tag$ ; drop table t $tag$", KindSelect, ""},
		{"escape string", `SELECT E'it\'s; DROP TABLE t'`, KindSelect, ""},
		{"doubled quote in string", "SELECT 'it''s; DROP TABLE t'", KindSelect, ""},
		{"column named start inside function", "SELECT coalesce(start, now()) FROM t", KindSelect, ""},
		{"column named cluster inside function", "SELECT lower(cluster) FROM t", KindSelect, ""},
		{"column named end after comma", "SELECT a, end FROM t", KindSelect, ""},
		{"time_bucket query", "SELECT time_bucket('1 hour', ts) AS bucket, avg(v) FROM m GROUP BY bucket ORDER BY bucket DESC LIMIT 10", KindSelect, ""},
		{"positional parameter", "SELECT * FROM t WHERE id = $1", KindSelect, ""},
		{"substring for", "SELECT substring(name from 1 for 3) FROM t", KindSelect, ""},
		{"filter with deleted column", "SELECT count(*) FILTER (WHERE deleted) FROM t", KindSelect, ""},
		{"casts and json operators", "SELECT x::text, y->>'k', z #>> '{a}' FROM t", KindSelect, ""},
		{"union", "select 1 union all select 2", KindSelect, ""},
		{"subquery", "SELECT * FROM (SELECT 1 AS a) AS s WHERE a IN (SELECT 1)", KindSelect, ""},
		{"pg_sleep allowed", "SELECT pg_sleep(1)", KindSelect, ""},
		{"pg_stat_activity", "SELECT application_name FROM pg_stat_activity", KindSelect, ""},
		{"unicode identifier", "SELECT größe FROM tabelle", KindSelect, ""},
		{"number literals", "SELECT 1.5e-3, .5, 10", KindSelect, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := Classify(tc.sql)
			if err != nil {
				t.Fatalf("Classify(%q) rejected: %v", tc.sql, err)
			}
			if st.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", st.Kind, tc.kind)
			}
			want := tc.want
			if want == "" {
				want = strings.TrimSpace(tc.sql)
			}
			if st.SQL != want {
				t.Errorf("SQL = %q, want %q", st.SQL, want)
			}
		})
	}
}

// Expected error fragments shared by several cases.
const (
	sqlSelectOne   = "SELECT 1"
	msgEmpty       = "empty"
	kwDelete       = "DELETE"
	kwInsert       = "INSERT"
	msgMultiple    = "multiple statements"
	msgRowLock     = "row locking"
	msgExplainOnly = "EXPLAIN may only explain"
	msgKeyword     = "must start with a keyword"
	fnTerminate    = "pg_terminate_backend"
	kwUpdate       = "UPDATE"
)

func TestClassifyRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sql  string
		msg  string // substring the error must contain
	}{
		{"empty", "", msgEmpty},
		{"whitespace", "   \n\t", msgEmpty},
		{"only a comment", "-- nothing here", msgEmpty},
		{"only a semicolon", ";", msgEmpty},
		{"delete", "DELETE FROM t", kwDelete},
		{"insert", "INSERT INTO t VALUES (1)", kwInsert},
		{"update", "UPDATE t SET a = 1", kwUpdate},
		{"merge", "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE", "MERGE"},
		{"create", "CREATE TABLE t (a int)", "CREATE"},
		{"drop", "DROP TABLE t", "DROP"},
		{"alter", "ALTER TABLE t ADD COLUMN c int", "ALTER"},
		{"truncate", "TRUNCATE t", "TRUNCATE"},
		{"grant", "GRANT SELECT ON t TO u", "GRANT"},
		{"copy", "COPY t TO '/tmp/x'", "COPY"},
		{"begin", "BEGIN", "BEGIN"},
		{"commit", "COMMIT", "COMMIT"},
		{"do block", "DO $$ BEGIN PERFORM 1; END $$", "DO"},
		{"call", "CALL proc()", "CALL"},
		{"lock", "LOCK TABLE t", "LOCK"},
		{"vacuum", "VACUUM t", "VACUUM"},
		{"analyze", "ANALYZE t", "ANALYZE"},
		{"cluster", "CLUSTER t", "CLUSTER"},
		{"refresh", "REFRESH MATERIALIZED VIEW mv", "REFRESH"},
		{"listen", "LISTEN chan", "LISTEN"},
		{"notify", "NOTIFY chan", "NOTIFY"},
		{"prepare", "PREPARE p AS SELECT 1", "PREPARE"},
		{"execute", "EXECUTE p", "EXECUTE"},
		{"discard", "DISCARD ALL", "DISCARD"},
		{"security label", "SECURITY LABEL ON TABLE t IS 'x'", "SECURITY"},
		{"set", "SET transaction_read_only = off", "SET"},
		{"set local", "SET LOCAL statement_timeout = 0", "SET"},
		{"reset", "RESET ALL", "RESET"},
		{"parenthesized select", "(SELECT 1)", msgKeyword},
		{"cte delete", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", kwDelete},
		{"cte insert", "WITH x AS (INSERT INTO t VALUES (1) RETURNING *) SELECT 1", kwInsert},
		{"cte update", "WITH x AS (UPDATE t SET a = 1 RETURNING *) SELECT 1", kwUpdate},
		{"cte merge", "WITH x AS (MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE) SELECT 1", "MERGE"},
		{"cte then insert", "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", kwInsert},
		{"cte then delete", "WITH x AS (SELECT 1) DELETE FROM t", kwDelete},
		{"cte then update", "WITH x AS (SELECT 1) UPDATE t SET a = 1", kwUpdate},
		{"subquery delete", "SELECT (DELETE FROM t)", kwDelete},
		{"two statements", "SELECT 1; SELECT 2", msgMultiple},
		{"select then drop", "SELECT 1; DROP TABLE t; --", msgMultiple},
		{"double semicolon", "SELECT 1;;", msgMultiple},
		{"select into", "SELECT * INTO newt FROM t", "INTO"},
		{"for update", "SELECT * FROM t FOR UPDATE", msgRowLock},
		{"for no key update", "SELECT * FROM t FOR NO KEY UPDATE", msgRowLock},
		{"for share", "SELECT * FROM t FOR SHARE", msgRowLock},
		{"for key share", "SELECT * FROM t FOR KEY SHARE", msgRowLock},
		{"explain delete", "EXPLAIN DELETE FROM t", msgExplainOnly},
		{"explain analyze update", "EXPLAIN (ANALYZE) UPDATE t SET a = 1", msgExplainOnly},
		{"explain legacy insert", "EXPLAIN ANALYZE INSERT INTO t VALUES (1)", msgExplainOnly},
		{"explain nothing", "EXPLAIN", "needs a statement"},
		{"explain unclosed options", "EXPLAIN (ANALYZE SELECT 1", "not closed"},
		{"explain show", "EXPLAIN SHOW all", msgExplainOnly},
		{"terminate backend", "SELECT pg_terminate_backend(123)", fnTerminate},
		{"terminate backend qualified", "SELECT pg_catalog.pg_terminate_backend(123)", fnTerminate},
		{"terminate backend quoted", `SELECT "pg_terminate_backend"(123)`, fnTerminate},
		{"terminate backend uppercase", "SELECT PG_TERMINATE_BACKEND(1)", "not allowed"},
		{"set_config", "SELECT set_config('statement_timeout', '0', false)", "set_config"},
		{"pg_read_file", "SELECT pg_read_file('/etc/passwd')", "pg_read_file"},
		{"advisory lock", "SELECT pg_advisory_lock(1)", "pg_advisory_lock"},
		{"txid_current", "SELECT txid_current()", "txid_current"},
		{"dblink", "SELECT * FROM dblink('host=x', 'select 1') AS t(a int)", "dblink"},
		{"unterminated string", "SELECT 'unterminated", "unterminated string"},
		{"unterminated block comment", "SELECT /* unterminated", "unterminated block comment"},
		{"unterminated quoted identifier", `SELECT "unterminated`, "unterminated quoted identifier"},
		{"unterminated dollar quote", "SELECT $q$ never closed", "unterminated dollar-quoted"},
		{"string escape trick", `SELECT 'abc\'; DELETE FROM t; --'`, msgMultiple},
		{"starts with number", "1 + 1", msgKeyword},
		{"starts with string", "'x'", msgKeyword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := Classify(tc.sql)
			if err == nil {
				t.Fatalf("Classify(%q) accepted as %s, want rejection", tc.sql, st.Kind)
			}
			if !errors.Is(err, ErrRejected) {
				t.Errorf("error %v does not wrap ErrRejected", err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.msg)
			}
		})
	}
}

func TestExplainable(t *testing.T) {
	t.Parallel()
	for kind, want := range map[Kind]bool{
		KindSelect: true, KindWith: true, KindValues: true, KindTable: true,
		KindExplain: false, KindShow: false,
	} {
		if got := (Statement{Kind: kind}).Explainable(); got != want {
			t.Errorf("Explainable(%s) = %v, want %v", kind, got, want)
		}
	}
}
