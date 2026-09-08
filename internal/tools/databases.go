package tools

import (
	"context"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
)

// databaseSummary is one entry of timescale_list_databases.
type databaseSummary struct {
	Name                    string   `json:"name"`
	Description             string   `json:"description,omitempty"`
	Host                    string   `json:"host"`
	Port                    int      `json:"port"`
	DBName                  string   `json:"dbname"`
	SSLMode                 string   `json:"sslmode"`
	MaxRows                 int      `json:"max_rows"`
	StatementTimeoutSeconds int64    `json:"statement_timeout_seconds"`
	AllowedGroups           []string `json:"allowed_groups,omitempty"`
	AllowedUsers            []string `json:"allowed_users,omitempty"`
	timescale.ProbeResult
}

type listDatabasesResult struct {
	Items       []databaseSummary `json:"items"`
	Count       int               `json:"count"`
	HiddenCount int               `json:"hidden_count"`
	Caller      string            `json:"caller"`
}

func registerListDatabases(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_list_databases",
		"List the TimescaleDB/PostgreSQL databases you may use, with reachability, PostgreSQL and TimescaleDB versions. "+
			"Databases whose allowlist excludes you are hidden (hidden_count). Start here to learn the database names the other tools take.")
	s.AddTool(tool, deps.wrap(tool.Name, nil, func(ctx context.Context, _ mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		res := listDatabasesResult{Items: []databaseSummary{}, Caller: callerName(caller)}
		if deps.Registry == nil {
			return res, callStats{}, nil
		}
		var visible []*timescale.Database
		for _, db := range deps.Registry.All() {
			if db.Allows(caller.Email, caller.Groups) {
				visible = append(visible, db)
			} else {
				res.HiddenCount++
			}
		}
		res.Items = make([]databaseSummary, len(visible))
		var wg sync.WaitGroup
		for i, db := range visible {
			cfg := db.Config()
			res.Items[i] = databaseSummary{
				Name:                    cfg.Name,
				Description:             cfg.Description,
				Host:                    cfg.Host,
				Port:                    cfg.Port,
				DBName:                  cfg.DBName,
				SSLMode:                 cfg.SSLMode,
				MaxRows:                 cfg.MaxRows,
				StatementTimeoutSeconds: int64(cfg.StatementTimeout.Seconds()),
				AllowedGroups:           cfg.AllowedGroups,
				AllowedUsers:            cfg.AllowedUsers,
			}
			wg.Add(1)
			go func(i int, db *timescale.Database) {
				defer wg.Done()
				res.Items[i].ProbeResult = db.Probe(ctx, callerName(caller))
			}(i, db)
		}
		wg.Wait()
		res.Count = len(res.Items)
		return res, callStats{rows: res.Count}, nil
	}))
}

func registerGetDatabaseInfo(s *mcpsrv.MCPServer, deps Deps) {
	tool := readOnlyTool("timescale_get_database_info",
		"Server facts for one database: PostgreSQL version, TimescaleDB version and license (apache = no compression/continuous-aggregate policies), "+
			"connected role, read-only status, database size, timezone, server time, installed extensions and the number of hypertables, continuous aggregates and background jobs.",
		databaseArg(deps),
	)
	s.AddTool(tool, deps.wrap(tool.Name, []string{argDatabase}, func(ctx context.Context, req mcp.CallToolRequest, caller server.Caller) (any, callStats, error) {
		db, err := deps.database(req, caller)
		if err != nil {
			return nil, callStats{}, err
		}
		info, err := db.Info(ctx, callerName(caller))
		return info, callStats{database: db.Name()}, err
	}))
}
