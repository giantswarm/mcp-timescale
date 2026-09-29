package config

import (
	"strings"
	"testing"
)

func TestLoadInsertPath(t *testing.T) {
	dir := t.TempDir()
	pass := writeFile(t, dir, "password", "reader-pw\n")
	ipass := writeFile(t, dir, "insert-password", "writer-pw\n")
	cfg := writeFile(t, dir, "databases.yaml", `
databases:
  - name: plant
    host: db-repl.example.svc
    dbname: plant
    username: reader
    passwordFile: `+pass+`
    insert:
      host: db.example.svc
      username: case_writer
      passwordFile: `+ipass+`
      tables: [werk.cases]
`)
	dbs, _, err := Load(cfg, true, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	in := dbs[0].Insert
	if in == nil || in.Host != "db.example.svc" || in.Port != DefaultPort || in.User != "case_writer" || in.Password != "writer-pw" {
		t.Fatalf("insert = %+v", in)
	}
	if dbs[0].User != "reader" || dbs[0].Password != "reader-pw" {
		t.Errorf("read credentials changed: %+v", dbs[0])
	}
}

func TestLoadInsertPathRejects(t *testing.T) {
	dir := t.TempDir()
	pass := writeFile(t, dir, "password", "pw\n")
	base := `
databases:
  - name: plant
    host: db.example.svc
    dbname: plant
    username: reader
    passwordFile: ` + pass + `
    insert:
`
	for name, tc := range map[string]struct{ insert, want string }{
		"no tables":         {"      username: w\n      passwordFile: " + pass + "\n", "tables is required"},
		"unqualified table": {"      username: w\n      passwordFile: " + pass + "\n      tables: [cases]\n", "schema-qualified"},
		"quoted table":      {"      username: w\n      passwordFile: " + pass + "\n      tables: ['werk.\"Cases\"']\n", "schema-qualified"},
		"no credentials":    {"      tables: [werk.cases]\n", "username or usernameFile is required"},
		"no password":       {"      username: w\n      tables: [werk.cases]\n", "passwordFile or passwordEnv is required"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := writeFile(t, dir, "databases.yaml", base+tc.insert)
			_, _, err := Load(cfg, true, "")
			if err == nil || !strings.Contains(err.Error(), "insert: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want insert error mentioning %q", err, tc.want)
			}
		})
	}
}
