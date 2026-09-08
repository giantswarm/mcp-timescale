package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/giantswarm/mcp-toolkit/health"
	"github.com/giantswarm/mcp-toolkit/httpx"
	"github.com/giantswarm/mcp-toolkit/logging"
	"github.com/giantswarm/mcp-toolkit/middleware/responsecap"
	"github.com/giantswarm/mcp-toolkit/middleware/timeout"
	"github.com/giantswarm/mcp-toolkit/tracing"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/giantswarm/mcp-template/internal/example"
	"github.com/giantswarm/mcp-template/internal/server"
	"github.com/giantswarm/mcp-template/internal/tools"
)

var (
	flagTransport   string
	flagMCPAddr     string
	flagMetricsAddr string
	flagDebug       bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP server",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().StringVar(&flagTransport, "transport", server.EnvOr("MCP_TRANSPORT", server.TransportStreamableHTTP),
		server.TransportStdio+" | "+server.TransportSSE+" | "+server.TransportStreamableHTTP)
	serveCmd.Flags().StringVar(&flagMCPAddr, "mcp-addr", server.EnvOr("MCP_ADDR", ":8080"),
		"listen address for MCP HTTP transport")
	serveCmd.Flags().StringVar(&flagMetricsAddr, "metrics-addr", server.EnvOr("METRICS_ADDR", ":9091"),
		"listen address for /metrics, /healthz, /readyz")
	serveCmd.Flags().BoolVar(&flagDebug, "debug", false, "enable debug logging (overrides DEBUG env)")
}

func runServe(_ *cobra.Command, _ []string) error {
	if err := validateTransport(flagTransport); err != nil {
		return err
	}

	shutdownCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := server.LoadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	level := slog.LevelInfo
	if cfg.Debug || flagDebug {
		level = slog.LevelDebug
	}
	logger, shutdownLogging, err := logging.Init(shutdownCtx, logging.WithLevel(level))
	if err != nil {
		logger = slog.Default()
		logger.Warn("logging init failed; using default logger", "error", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownLogging(ctx)
		}()
	}

	shutdownTracing, err := tracing.Init(shutdownCtx, tracing.WithServiceName(serviceName), tracing.WithServiceVersion(version))
	if err != nil {
		logger.Warn("otel init failed; continuing without tracing", "error", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTracing(ctx)
		}()
	}

	exClient := example.NewFakeClient()

	mcp := mcpsrv.NewMCPServer(
		serviceName, version,
		mcpsrv.WithToolCapabilities(false),
		mcpsrv.WithRecovery(),
		mcpsrv.WithToolHandlerMiddleware(timeout.New(30*time.Second)),
		mcpsrv.WithToolHandlerMiddleware(responsecap.New(responsecap.Options{})),
	)
	tools.Register(mcp, tools.Deps{Client: exClient, Log: logger})

	if flagTransport == server.TransportStdio {
		logger.Info("MCP serving on stdio", "transport", server.TransportStdio)
		logger.Warn("stdio transport bypasses OAuth — tool calls hit authz errors unless the session installs a caller identity")
		return mcpsrv.ServeStdio(mcp)
	}

	var auth *server.Auth
	if cfg.OAuthEnabled {
		auth, err = server.NewAuth(shutdownCtx, logger)
		if err != nil {
			return fmt.Errorf("oauth: %w", err)
		}
		defer func() { _ = auth.Shutdown(context.Background()) }()
	} else {
		logger.Warn("OAuth is DISABLED — set OAUTH_ENABLED=true plus OAUTH_ISSUER, OAUTH_PROVIDER, and the provider-specific OAUTH_* vars for production")
	}

	mcpMux := server.BuildMCPMux(flagTransport, mcp, auth)

	hc := health.New()
	obsMux := http.NewServeMux()
	hc.Mount(obsMux)
	obsMux.Handle("/metrics", server.MetricsHandler())

	mcpServer := &http.Server{
		Addr:              flagMCPAddr,
		Handler:           mcpMux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	obsServer := &http.Server{
		Addr:              flagMetricsAddr,
		Handler:           obsMux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	mcpCtx, mcpCancel := context.WithCancel(context.Background())
	defer mcpCancel()
	obsCtx, obsCancel := context.WithCancel(context.Background())
	defer obsCancel()

	mcpDone := runHTTP(mcpCtx, mcpServer, 10*time.Second, "MCP", logger, cancel)
	obsDone := runHTTP(obsCtx, obsServer, 5*time.Second, "observability", logger, cancel)

	hc.SetReady(true)

	<-shutdownCtx.Done()
	logger.Info("shutdown requested")
	hc.SetReady(false)

	mcpCancel()
	<-mcpDone
	obsCancel()
	<-obsDone
	return nil
}

func validateTransport(t string) error {
	switch t {
	case server.TransportStdio, server.TransportSSE, server.TransportStreamableHTTP:
		return nil
	default:
		return fmt.Errorf("transport %q is not supported (want one of: %s, %s, %s)",
			t, server.TransportStdio, server.TransportSSE, server.TransportStreamableHTTP)
	}
}

// runHTTP launches srv via httpx.Run in a goroutine. A bind failure or
// unexpected server error triggers abort so the parent shutdownCtx cancels
// and the rest of the lifecycle drains. The returned channel emits the
// final error (nil on graceful shutdown) once the server has stopped.
func runHTTP(ctx context.Context, srv *http.Server, drain time.Duration, label string, log *slog.Logger, abort context.CancelFunc) <-chan error {
	done := make(chan error, 1)
	go func() {
		log.Info(label+" listening", "addr", srv.Addr)
		err := httpx.Run(ctx, srv, drain)
		if err != nil {
			log.Error(label+" server failed", "error", err)
			abort()
		}
		done <- err
	}()
	return done
}
