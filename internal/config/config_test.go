package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFileWithDefaultsAndSecrets(t *testing.T) {
	dir := t.TempDir()
	userFile := writeFile(t, dir, "username", "reader\n")
	passFile := writeFile(t, dir, "password", "s3cret\n")
	t.Setenv("OTHER_PW", "from-env")
	cfg := writeFile(t, dir, "databases.yaml", `
databases:
  - name: demo
    description: Factory demo data
    host: db.example.svc
    dbname: timescaledb
    usernameFile: `+userFile+`
    passwordFile: `+passFile+`
    allowedGroups: [team-a]
  - name: other
    host: other.example.svc
    port: 5433
    dbname: other
    sslmode: verify-full
    username: bob
    passwordEnv: OTHER_PW
    maxRows: 50
    statementTimeout: 5s
    maxConnections: 2
`)

	dbs, src, err := Load(cfg, true, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src.File != cfg || src.DSN {
		t.Errorf("source = %+v", src)
	}
	if len(dbs) != 2 {
		t.Fatalf("got %d databases, want 2", len(dbs))
	}

	demo := dbs[0]
	if demo.Port != DefaultPort || demo.SSLMode != DefaultSSLMode || demo.MaxRows != DefaultMaxRows ||
		demo.StatementTimeout != DefaultStatementTimeout || demo.MaxConnections != DefaultMaxConnections {
		t.Errorf("defaults not applied: %+v", demo)
	}
	if demo.User != "reader" || demo.Password != "s3cret" {
		t.Errorf("credentials not resolved from files (trailing newline must be trimmed): user=%q", demo.User)
	}

	other := dbs[1]
	if other.Port != 5433 || other.SSLMode != "verify-full" || other.MaxRows != 50 ||
		other.StatementTimeout != 5*time.Second || other.MaxConnections != 2 {
		t.Errorf("explicit values lost: %+v", other)
	}
	if other.User != "bob" || other.Password != "from-env" {
		t.Errorf("credentials: user=%q password from env not resolved", other.User)
	}
}

func TestLoadDSNOnly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	dbs, src, err := Load(missing, false, "postgres://u:p@localhost:5432/db?sslmode=disable")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !src.DSN || src.File != "" {
		t.Errorf("source = %+v", src)
	}
	if len(dbs) != 1 || dbs[0].Name != DSNDatabaseName || dbs[0].DSN == "" {
		t.Fatalf("dbs = %+v", dbs)
	}
	if dbs[0].MaxRows != DefaultMaxRows || dbs[0].StatementTimeout != DefaultStatementTimeout {
		t.Errorf("DSN database must still get defaults: %+v", dbs[0])
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, _, err := Load(missing, true, ""); err == nil {
		t.Error("an explicitly configured but missing file must fail")
	}
	dbs, _, err := Load(missing, false, "")
	if err != nil || len(dbs) != 0 {
		t.Errorf("default path missing and no DSN: want empty list, got %v %v", dbs, err)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	pw := writeFile(t, dir, "pw", "x")
	base := "host: h\ndbname: d\nusername: u\npasswordFile: " + pw + "\n"
	cases := []struct {
		name string
		yaml string
		msg  string
	}{
		{"unknown key", "databases:\n  - name: a\n    hots: x\n    " + strings.ReplaceAll(base, "\n", "\n    "), "field hots not found"},
		{"bad name", "databases:\n  - name: Demo_DB\n    " + strings.ReplaceAll(base, "\n", "\n    "), "must match"},
		{"missing name", "databases:\n  - " + strings.ReplaceAll(base, "\n", "\n    "), "name is required"},
		{"duplicate", "databases:\n  - name: a\n    " + strings.ReplaceAll(base, "\n", "\n    ") + "\n  - name: a\n    " + strings.ReplaceAll(base, "\n", "\n    "), "configured twice"},
		{"missing host", "databases:\n  - name: a\n    dbname: d\n    username: u\n    passwordFile: " + pw, "host is required"},
		{"missing dbname", "databases:\n  - name: a\n    host: h\n    username: u\n    passwordFile: " + pw, "dbname is required"},
		{"bad sslmode", "databases:\n  - name: a\n    sslmode: prefer\n    " + strings.ReplaceAll(base, "\n", "\n    "), "sslmode"},
		{"bad port", "databases:\n  - name: a\n    port: 70000\n    " + strings.ReplaceAll(base, "\n", "\n    "), "port"},
		{"both usernames", "databases:\n  - name: a\n    usernameFile: /x\n    " + strings.ReplaceAll(base, "\n", "\n    "), "not both"},
		{"no username", "databases:\n  - name: a\n    host: h\n    dbname: d\n    passwordFile: " + pw, "username or usernameFile"},
		{"no password", "databases:\n  - name: a\n    host: h\n    dbname: d\n    username: u", "passwordFile or passwordEnv"},
		{"both passwords", "databases:\n  - name: a\n    passwordEnv: X\n    " + strings.ReplaceAll(base, "\n", "\n    "), "not both"},
		{"password env unset", "databases:\n  - name: a\n    host: h\n    dbname: d\n    username: u\n    passwordEnv: MCP_TIMESCALE_TEST_UNSET_VAR", "is not set"},
		{"password file missing", "databases:\n  - name: a\n    host: h\n    dbname: d\n    username: u\n    passwordFile: " + filepath.Join(dir, "missing"), "passwordFile"},
		{"bad timeout", "databases:\n  - name: a\n    statementTimeout: 100ms\n    " + strings.ReplaceAll(base, "\n", "\n    "), "statementTimeout"},
		{"unparseable timeout", "databases:\n  - name: a\n    statementTimeout: soon\n    " + strings.ReplaceAll(base, "\n", "\n    "), "parse"},
		{"too many rows", "databases:\n  - name: a\n    maxRows: 6000\n    " + strings.ReplaceAll(base, "\n", "\n    "), "maxRows"},
		{"too many connections", "databases:\n  - name: a\n    maxConnections: 100\n    " + strings.ReplaceAll(base, "\n", "\n    "), "maxConnections"},
		{"empty allowlist entry", "databases:\n  - name: a\n    allowedUsers: [\"\"]\n    " + strings.ReplaceAll(base, "\n", "\n    "), "allowedUsers"},
		{"missing ca file", "databases:\n  - name: a\n    sslRootCertFile: /nonexistent/ca.crt\n    " + strings.ReplaceAll(base, "\n", "\n    "), "sslRootCertFile"},
		{"not yaml", "databases: [", "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "databases.yaml", tc.yaml)
			_, _, err := Load(p, true, "")
			if err == nil {
				t.Fatalf("Load accepted %q", tc.yaml)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.msg)
			}
		})
	}
}

func TestLoadDSNNameCollision(t *testing.T) {
	dir := t.TempDir()
	pw := writeFile(t, dir, "pw", "x")
	p := writeFile(t, dir, "databases.yaml", "databases:\n  - name: default\n    host: h\n    dbname: d\n    username: u\n    passwordFile: "+pw)
	if _, _, err := Load(p, true, "postgres://localhost/x"); err == nil || !strings.Contains(err.Error(), "configured twice") {
		t.Errorf("DSN database must not shadow a file database named default: %v", err)
	}
}

func TestEmptyFile(t *testing.T) {
	p := writeFile(t, t.TempDir(), "databases.yaml", "")
	dbs, _, err := Load(p, true, "")
	if err != nil || len(dbs) != 0 {
		t.Errorf("empty file: got %v %v", dbs, err)
	}
}

const groupA = "team-a"

func TestAllows(t *testing.T) {
	open := Database{}
	if !open.Allows("anyone@example.com", nil) || open.Restricted() {
		t.Error("no allowlist must allow every authenticated caller")
	}
	byGroup := Database{AllowedGroups: []string{groupA}}
	if !byGroup.Allows("x@example.com", []string{"team-b", groupA}) {
		t.Error("group member must be allowed")
	}
	if byGroup.Allows("x@example.com", []string{"Team-A"}) {
		t.Error("groups match exactly, not case-insensitively")
	}
	if byGroup.Allows("x@example.com", nil) {
		t.Error("caller without groups must be refused")
	}
	byUser := Database{AllowedUsers: []string{"Alice@Example.com"}}
	if !byUser.Allows("alice@example.com", nil) {
		t.Error("emails match case-insensitively")
	}
	if byUser.Allows("", nil) {
		t.Error("empty email must never match")
	}
	if byUser.Allows("bob@example.com", []string{groupA}) {
		t.Error("group does not help when only users are listed")
	}
	both := Database{AllowedUsers: []string{"alice@example.com"}, AllowedGroups: []string{groupA}}
	if !both.Allows("bob@example.com", []string{groupA}) || !both.Allows("alice@example.com", nil) {
		t.Error("either list may grant access")
	}
	if !both.Restricted() {
		t.Error("Restricted must report an allowlist")
	}
}
