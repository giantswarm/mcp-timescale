// Package config loads and validates the database inventory of
// mcp-timescale: a YAML file listing the databases the server may connect
// to, plus an optional single-database DSN shortcut for local use.
//
// Secrets (passwords) are read from files or environment variables at load
// time and kept in memory only; nothing in this package logs them.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults and bounds for per-database settings.
const (
	DefaultPort             = 5432
	DefaultSSLMode          = "require"
	DefaultMaxRows          = 500
	MaxMaxRows              = 5000
	DefaultStatementTimeout = 30 * time.Second
	MaxStatementTimeout     = 10 * time.Minute
	DefaultMaxConnections   = 4
	MaxMaxConnections       = 64

	// DefaultDatabasesFile is where the Helm chart mounts databases.yaml.
	DefaultDatabasesFile = "/etc/mcp-timescale/databases.yaml"

	// DSNDatabaseName is the name of the database configured through
	// MCP_TIMESCALE_DSN.
	DSNDatabaseName = "default"

	// Environment variables.
	EnvDatabasesFile = "MCP_TIMESCALE_DATABASES_FILE"
	EnvDSN           = "MCP_TIMESCALE_DSN"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)
	sslModes  = []string{"disable", "require", "verify-ca", "verify-full"}
	errNoName = errors.New("name is required")
)

// File is the on-disk shape of databases.yaml.
type File struct {
	Databases []Database `yaml:"databases"`
}

// Database is one configured database. The YAML tags are the file format;
// the resolved credential fields are filled by Load and never serialized.
type Database struct {
	Name            string `yaml:"name"`
	Description     string `yaml:"description"`
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	DBName          string `yaml:"dbname"`
	SSLMode         string `yaml:"sslmode"`
	SSLRootCertFile string `yaml:"sslRootCertFile"`

	Username     string `yaml:"username"`
	UsernameFile string `yaml:"usernameFile"`
	PasswordFile string `yaml:"passwordFile"`
	PasswordEnv  string `yaml:"passwordEnv"`

	AllowedGroups []string `yaml:"allowedGroups"`
	AllowedUsers  []string `yaml:"allowedUsers"`

	MaxRows          int           `yaml:"maxRows"`
	StatementTimeout time.Duration `yaml:"statementTimeout"`
	MaxConnections   int           `yaml:"maxConnections"`

	// DSN is set for the MCP_TIMESCALE_DSN shortcut only. When non-empty
	// it carries the whole connection configuration including credentials
	// and Host/Port/DBName are informational.
	DSN string `yaml:"-"`
	// User and Password are the resolved credentials (from Username /
	// UsernameFile and PasswordFile / PasswordEnv).
	User     string `yaml:"-"`
	Password string `yaml:"-"`
}

// Allows reports whether a caller with the given email and groups may use
// the database. Both lists empty means every authenticated caller may.
// Emails match case-insensitively, groups exactly.
func (d Database) Allows(email string, groups []string) bool {
	if len(d.AllowedGroups) == 0 && len(d.AllowedUsers) == 0 {
		return true
	}
	for _, u := range d.AllowedUsers {
		if email != "" && strings.EqualFold(u, email) {
			return true
		}
	}
	for _, g := range d.AllowedGroups {
		if slices.Contains(groups, g) {
			return true
		}
	}
	return false
}

// Restricted reports whether the database has an allowlist at all.
func (d Database) Restricted() bool {
	return len(d.AllowedGroups) > 0 || len(d.AllowedUsers) > 0
}

// Source says where a Load result came from, for the startup log.
type Source struct {
	File string // path of the databases file that was read, or ""
	DSN  bool   // MCP_TIMESCALE_DSN contributed the "default" database
}

// Load reads the databases file at path (when it exists) and the
// MCP_TIMESCALE_DSN shortcut (when set) and returns the validated list.
// fileRequired makes a missing file an error; it is set when the path was
// given explicitly. An empty result is allowed so a freshly installed chart
// without databases still starts; tools then report that nothing is
// configured.
func Load(path string, fileRequired bool, dsn string) ([]Database, Source, error) {
	var (
		dbs []Database
		src Source
	)

	data, err := os.ReadFile(path) // #nosec G304 -- the path is operator configuration, not user input
	switch {
	case err == nil:
		f, err := parse(data)
		if err != nil {
			return nil, src, fmt.Errorf("databases file %s: %w", path, err)
		}
		dbs = append(dbs, f.Databases...)
		src.File = path
	case errors.Is(err, os.ErrNotExist) && !fileRequired:
		// Fine: local runs usually configure MCP_TIMESCALE_DSN only.
	default:
		return nil, src, fmt.Errorf("databases file %s: %w", path, err)
	}

	if dsn != "" {
		dbs = append(dbs, Database{Name: DSNDatabaseName, Description: "database from " + EnvDSN, DSN: dsn})
		src.DSN = true
	}

	for i := range dbs {
		if err := dbs[i].validateAndResolve(); err != nil {
			name := dbs[i].Name
			if name == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			return nil, src, fmt.Errorf("database %s: %w", name, err)
		}
	}
	seen := map[string]struct{}{}
	for _, d := range dbs {
		if _, dup := seen[d.Name]; dup {
			return nil, src, fmt.Errorf("database name %q is configured twice", d.Name)
		}
		seen[d.Name] = struct{}{}
	}
	return dbs, src, nil
}

// Parse decodes a databases file without resolving credentials. Unknown
// keys are errors so a typo cannot silently drop an allowlist.
func Parse(data []byte) (*File, error) {
	f, err := parse(data)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func parse(data []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("parse: %w", err)
	}
	return f, nil
}

// validateAndResolve applies defaults, checks bounds and reads the
// credential files / env var.
func (d *Database) validateAndResolve() error {
	if d.Name == "" {
		return errNoName
	}
	if !nameRe.MatchString(d.Name) {
		return fmt.Errorf("name %q must match %s", d.Name, nameRe.String())
	}

	if d.MaxRows == 0 {
		d.MaxRows = DefaultMaxRows
	}
	if d.MaxRows < 1 || d.MaxRows > MaxMaxRows {
		return fmt.Errorf("maxRows %d must be between 1 and %d", d.MaxRows, MaxMaxRows)
	}
	if d.StatementTimeout == 0 {
		d.StatementTimeout = DefaultStatementTimeout
	}
	if d.StatementTimeout < time.Second || d.StatementTimeout > MaxStatementTimeout {
		return fmt.Errorf("statementTimeout %s must be between 1s and %s", d.StatementTimeout, MaxStatementTimeout)
	}
	if d.MaxConnections == 0 {
		d.MaxConnections = DefaultMaxConnections
	}
	if d.MaxConnections < 1 || d.MaxConnections > MaxMaxConnections {
		return fmt.Errorf("maxConnections %d must be between 1 and %d", d.MaxConnections, MaxMaxConnections)
	}
	for _, u := range d.AllowedUsers {
		if strings.TrimSpace(u) == "" {
			return errors.New("allowedUsers contains an empty entry")
		}
	}
	for _, g := range d.AllowedGroups {
		if strings.TrimSpace(g) == "" {
			return errors.New("allowedGroups contains an empty entry")
		}
	}

	if d.DSN != "" {
		return d.validateDSNOnly()
	}

	if d.Host == "" {
		return errors.New("host is required")
	}
	if d.Port == 0 {
		d.Port = DefaultPort
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("port %d is out of range", d.Port)
	}
	if d.DBName == "" {
		return errors.New("dbname is required")
	}
	if d.SSLMode == "" {
		d.SSLMode = DefaultSSLMode
	}
	if !slices.Contains(sslModes, d.SSLMode) {
		return fmt.Errorf("sslmode %q must be one of %s", d.SSLMode, strings.Join(sslModes, ", "))
	}
	if d.SSLRootCertFile != "" {
		if _, err := os.Stat(d.SSLRootCertFile); err != nil {
			return fmt.Errorf("sslRootCertFile: %w", err)
		}
	}

	switch {
	case d.Username != "" && d.UsernameFile != "":
		return errors.New("set either username or usernameFile, not both")
	case d.Username != "":
		d.User = d.Username
	case d.UsernameFile != "":
		v, err := readSecretFile(d.UsernameFile)
		if err != nil {
			return fmt.Errorf("usernameFile: %w", err)
		}
		d.User = v
	default:
		return errors.New("username or usernameFile is required")
	}

	switch {
	case d.PasswordFile != "" && d.PasswordEnv != "":
		return errors.New("set either passwordFile or passwordEnv, not both")
	case d.PasswordFile != "":
		v, err := readSecretFile(d.PasswordFile)
		if err != nil {
			return fmt.Errorf("passwordFile: %w", err)
		}
		d.Password = v
	case d.PasswordEnv != "":
		v, ok := os.LookupEnv(d.PasswordEnv)
		if !ok {
			return fmt.Errorf("passwordEnv: %s is not set", d.PasswordEnv)
		}
		d.Password = v
	default:
		return errors.New("passwordFile or passwordEnv is required")
	}
	return nil
}

// validateDSNOnly rejects fields that make no sense next to a DSN.
func (d *Database) validateDSNOnly() error {
	if d.Host != "" || d.Port != 0 || d.DBName != "" || d.Username != "" || d.UsernameFile != "" ||
		d.PasswordFile != "" || d.PasswordEnv != "" || d.SSLMode != "" || d.SSLRootCertFile != "" {
		return errors.New("a DSN database takes no host/port/dbname/ssl/credential fields")
	}
	return nil
}

// readSecretFile reads a mounted credential and trims the trailing newline
// most secret tooling appends.
func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- operator-configured secret mount path
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}
