package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

const (
	inserterRole     = "mcp_it_inserter"
	inserterPassword = "mcp-it-inserter" //nolint:gosec // throwaway role in the test database
)

// TestIntegrationInsertRow runs timescale_insert_row as a role that may
// only INSERT into it_cases.cases (no SELECT, nothing else), the least
// privilege the insert path is meant for.
func TestIntegrationInsertRow(t *testing.T) {
	dsn := integrationDSN(t)
	admin := adminConn(t, dsn)
	ctx := context.Background()
	for _, stmt := range []string{
		"DROP SCHEMA IF EXISTS it_cases CASCADE",
		"CREATE SCHEMA it_cases",
		`CREATE TABLE it_cases.cases (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, case_date date NOT NULL, title text NOT NULL,
			tags text[], details jsonb, created_at timestamptz NOT NULL DEFAULT now())`,
		"CREATE TABLE it_cases.other (v text)",
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '` + inserterRole + `') THEN CREATE ROLE ` + inserterRole + ` LOGIN; END IF; END $$`,
		"ALTER ROLE " + inserterRole + " PASSWORD '" + inserterPassword + "'",
		"GRANT USAGE ON SCHEMA it_cases TO " + inserterRole,
		"GRANT INSERT ON it_cases.cases, it_cases.other TO " + inserterRole,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := timescale.NewRegistry([]config.Database{{
		Name: itDatabase, Host: cfg.Host, Port: int(cfg.Port), DBName: cfg.Database, SSLMode: sslDisable,
		User: cfg.User, Password: cfg.Password, MaxRows: itMaxRows, StatementTimeout: 5 * time.Second, MaxConnections: 2,
		Insert: &config.Insert{Host: cfg.Host, Port: int(cfg.Port), User: inserterRole, Password: inserterPassword, Tables: []string{"it_cases.cases"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	h := newHarness(t, Deps{Registry: reg}, true)
	caller := alice()

	out := h.mustOK(caller, toolInsertRow, map[string]any{argTable: "it_cases.cases", argRow: map[string]any{
		"case_date": "2026-09-29", "title": "Void drift", "tags": []any{"voids", "water"}, "details": map[string]any{"lag_min": 12},
	}})
	if out["inserted"] != float64(1) {
		t.Fatalf("result = %v", out)
	}
	var (
		title string
		tags  []string
		lag   int
	)
	if err := admin.QueryRow(ctx, "SELECT title, tags, (details->>'lag_min')::int FROM it_cases.cases").Scan(&title, &tags, &lag); err != nil {
		t.Fatal(err)
	}
	if title != "Void drift" || strings.Join(tags, ",") != "voids,water" || lag != 12 {
		t.Errorf("row = %q %v %d", title, tags, lag)
	}

	// The role could insert into it_cases.other; the allowlist refuses it.
	h.mustErr(caller, toolInsertRow, map[string]any{argTable: "it_cases.other", argRow: map[string]any{"v": "x"}}, "not writable")
	// A generated identity column cannot be set; an unknown column fails in PostgreSQL.
	h.mustErr(caller, toolInsertRow, map[string]any{argTable: "it_cases.cases", argRow: map[string]any{"id": 7, "case_date": "2026-09-29", "title": "x"}}, "")
	h.mustErr(caller, toolInsertRow, map[string]any{argTable: "it_cases.cases", argRow: map[string]any{"nope": 1, "case_date": "2026-09-29", "title": "x"}}, "nope")
	// Reads still run as the read role in a READ ONLY transaction.
	h.mustErr(caller, toolQuery, map[string]any{"sql": "INSERT INTO it_cases.cases (case_date, title) VALUES ('2026-09-29', 'x')"}, "read-only")

	var n int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM it_cases.cases").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("cases = %d, want 1", n)
	}
}
