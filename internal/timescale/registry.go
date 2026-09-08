// Package timescale talks to the configured TimescaleDB / PostgreSQL
// databases. Every statement runs inside a READ ONLY transaction on a
// connection whose session defaults are read-only as well, attributed to
// the calling person through application_name.
package timescale

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giantswarm/mcp-timescale/internal/config"
)

const (
	// ApplicationName is the base application_name every connection carries;
	// attributed sessions extend it with "/<caller>".
	ApplicationName = "mcp-timescale"

	connectTimeout  = 10 * time.Second
	maxConnIdleTime = 5 * time.Minute
	maxConnLifetime = time.Hour
	healthCheck     = time.Minute
)

// Registry holds every configured database in configuration order.
type Registry struct {
	dbs   map[string]*Database
	order []string
}

// NewRegistry builds the connection configuration of every database. No
// connection is opened; pools connect on first use.
func NewRegistry(cfgs []config.Database) (*Registry, error) {
	r := &Registry{dbs: make(map[string]*Database, len(cfgs))}
	for _, c := range cfgs {
		pc, err := poolConfig(c)
		if err != nil {
			return nil, fmt.Errorf("database %s: %w", c.Name, err)
		}
		if _, dup := r.dbs[c.Name]; dup {
			return nil, fmt.Errorf("database %s is configured twice", c.Name)
		}
		r.dbs[c.Name] = &Database{cfg: c, poolCfg: pc}
		r.order = append(r.order, c.Name)
	}
	return r, nil
}

// Names returns the database names in configuration order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// Len returns the number of configured databases.
func (r *Registry) Len() int { return len(r.order) }

// Get returns the named database.
func (r *Registry) Get(name string) (*Database, bool) {
	d, ok := r.dbs[name]
	return d, ok
}

// All returns every database in configuration order.
func (r *Registry) All() []*Database {
	out := make([]*Database, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.dbs[n])
	}
	return out
}

// MaxStatementTimeout is the largest configured statement timeout; the
// tool-handler timeout must stay above it.
func (r *Registry) MaxStatementTimeout() time.Duration {
	var m time.Duration
	for _, d := range r.dbs {
		if d.cfg.StatementTimeout > m {
			m = d.cfg.StatementTimeout
		}
	}
	return m
}

// Close closes every pool that was opened.
func (r *Registry) Close() {
	for _, d := range r.dbs {
		d.Close()
	}
}

// Database is one configured database with a lazily opened pool.
type Database struct {
	cfg     config.Database
	poolCfg *pgxpool.Config

	mu   sync.Mutex
	pool *pgxpool.Pool
}

// Name returns the tool-facing database name.
func (d *Database) Name() string { return d.cfg.Name }

// Config returns the database configuration without credentials, safe to
// log or return to callers.
func (d *Database) Config() config.Database {
	c := d.cfg
	c.Password = ""
	c.User = ""
	c.DSN = ""
	if d.cfg.DSN != "" {
		// Fill the informational fields from the parsed DSN so
		// list_databases can show where "default" points.
		cc := d.poolCfg.ConnConfig
		c.Host = cc.Host
		c.Port = int(cc.Port)
		c.DBName = cc.Database
		c.SSLMode = "disable"
		if cc.TLSConfig != nil {
			c.SSLMode = "require"
			if !cc.TLSConfig.InsecureSkipVerify {
				c.SSLMode = "verify-full"
			}
		}
	}
	return c
}

// MaxRows is the hard row cap of this database.
func (d *Database) MaxRows() int { return d.cfg.MaxRows }

// StatementTimeout is the session statement timeout of this database.
func (d *Database) StatementTimeout() time.Duration { return d.cfg.StatementTimeout }

// Allows reports whether the caller may use this database.
func (d *Database) Allows(email string, groups []string) bool {
	return d.cfg.Allows(email, groups)
}

// Pool returns the connection pool, opening it on first use. Opening does
// not connect; the first Acquire does.
func (d *Database) Pool(ctx context.Context) (*pgxpool.Pool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pool != nil {
		return d.pool, nil
	}
	p, err := pgxpool.NewWithConfig(ctx, d.poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	d.pool = p
	return p, nil
}

// Close closes the pool if it was opened.
func (d *Database) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pool != nil {
		d.pool.Close()
		d.pool = nil
	}
}

// poolConfig turns a database configuration into a pgxpool configuration
// with read-only session defaults.
func poolConfig(c config.Database) (*pgxpool.Config, error) {
	var (
		pc  *pgxpool.Config
		err error
	)
	if c.DSN != "" {
		pc, err = pgxpool.ParseConfig(c.DSN)
		if err != nil {
			return nil, fmt.Errorf("parse DSN: %w", err)
		}
	} else {
		q := url.Values{}
		q.Set("sslmode", c.SSLMode)
		if c.SSLRootCertFile != "" {
			q.Set("sslrootcert", c.SSLRootCertFile)
		}
		u := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(c.User, c.Password),
			Host:     net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
			Path:     "/" + c.DBName,
			RawQuery: q.Encode(),
		}
		pc, err = pgxpool.ParseConfig(u.String())
		if err != nil {
			return nil, fmt.Errorf("build connection config: %w", err)
		}
	}

	pc.MaxConns = int32(c.MaxConnections) // #nosec G115 -- bounded by config validation (<= 64)
	pc.MinConns = 0
	pc.MaxConnIdleTime = maxConnIdleTime
	pc.MaxConnLifetime = maxConnLifetime
	pc.HealthCheckPeriod = healthCheck
	pc.ConnConfig.ConnectTimeout = connectTimeout
	if pc.ConnConfig.RuntimeParams == nil {
		pc.ConnConfig.RuntimeParams = map[string]string{}
	}
	pc.ConnConfig.RuntimeParams["application_name"] = ApplicationName

	timeoutMS := c.StatementTimeout.Milliseconds()
	pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// Session defaults, set with SET rather than as startup parameters
		// so connection poolers that reject unknown startup parameters
		// (PgBouncer) still work. Every tool call additionally runs inside
		// an explicit READ ONLY transaction.
		for _, stmt := range []string{
			"SET default_transaction_read_only = on",
			fmt.Sprintf("SET statement_timeout = %d", timeoutMS),
			fmt.Sprintf("SET idle_in_transaction_session_timeout = %d", timeoutMS),
		} {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("session setup (%s): %w", stmt, err)
			}
		}
		return nil
	}
	return pc, nil
}
