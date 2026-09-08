package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/giantswarm/mcp-oauth/storage"
)

// Auth bundles the mcp-oauth Server + Handler with their teardown. nil Auth
// means OAuth is disabled — callers branch on cfg.OAuthEnabled before
// constructing.
//
// The Handler / Server fields are exposed so a server author can reach for
// any oauth.* method we don't pre-wire (custom rate limiters, audit hooks,
// session handlers, etc.) without forking this package.
type Auth struct {
	Server  *oauth.Server
	Handler *handler.Handler
	close   func()
}

// NewAuth wires the configured OAuth provider, the storage backend, the
// optional token encryptor, and the mcp-oauth server, and returns an *Auth
// ready to mount on the MCP mux. Every knob comes from OAUTH_* env vars
// parsed by mcp-oauth's oauthconfig package — see the README for the full
// list.
func NewAuth(_ context.Context, logger *slog.Logger) (*Auth, error) {
	provider, err := oauthconfig.ProviderFromEnv()
	if err != nil {
		return nil, fmt.Errorf("oauth provider from env: %w", err)
	}

	enc, err := oauthconfig.NewEncryptorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("oauth encryptor from env: %w", err)
	}

	store, storeClose, err := oauthconfig.StorageFromEnv(enc, nil, logger)
	if err != nil {
		return nil, fmt.Errorf("oauth storage from env: %w", err)
	}

	if backend := os.Getenv("OAUTH_STORAGE_BACKEND"); (backend == "" || backend == storage.BackendMemory) && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		logger.Warn("OAUTH_STORAGE_BACKEND=memory in a Kubernetes deployment — OAuth state is lost on pod restart and NOT shared across replicas; set OAUTH_STORAGE_BACKEND=valkey for production")
	}

	cfg, err := oauthconfig.FromEnv()
	if err != nil {
		_ = storeClose()
		return nil, fmt.Errorf("oauth config from env: %w", err)
	}

	srv, err := oauth.NewServerWithCombined(provider, store, cfg, logger)
	if err != nil {
		_ = storeClose()
		return nil, fmt.Errorf("oauth server: %w", err)
	}

	return &Auth{
		Server:  srv,
		Handler: handler.New(srv, logger),
		close:   func() { _ = storeClose() },
	}, nil
}

// Shutdown closes the storage backend.
func (a *Auth) Shutdown(_ context.Context) error {
	if a == nil || a.close == nil {
		return nil
	}
	a.close()
	return nil
}

// PromoteOAuthCaller lifts the UserInfo attached by mcp-oauth's
// ValidateToken middleware onto the context mcp-go passes to tool handlers.
// Pass it to mcpsrv.WithHTTPContextFunc / WithSSEContextFunc so tool
// handlers can resolve the caller via CallerFromContext.
//
// This is the bridge between mcp-oauth's HTTP layer and mcp-go's tool
// handler context. If/when mcp-oauth ships an upstream bridge package,
// swap this function for a re-export.
func PromoteOAuthCaller(ctx context.Context, r *http.Request) context.Context {
	if ui, ok := handler.UserInfoFromContext(r.Context()); ok {
		return context.WithValue(ctx, callerKey{}, ui)
	}
	return ctx
}

// Caller is the identity tools see. Subject is the OIDC sub claim (stable,
// non-spoofable); Email is the human-facing handle. The raw UserInfo is
// available for advanced use.
type Caller struct {
	Subject string
	Email   string
	Groups  []string
	Raw     *providers.UserInfo
}

// Empty reports whether no identifying fields were set.
func (c Caller) Empty() bool { return c.Subject == "" && c.Email == "" }

// CallerFromContext extracts the caller from a tool handler's context.
// Returns (zero, false) when no caller is attached (e.g., stdio transport
// or pre-auth middleware).
func CallerFromContext(ctx context.Context) (Caller, bool) {
	ui, ok := ctx.Value(callerKey{}).(*providers.UserInfo)
	if !ok || ui == nil {
		return Caller{}, false
	}
	return Caller{
		Subject: ui.ID,
		Email:   ui.Email,
		Groups:  ui.Groups,
		Raw:     ui,
	}, true
}

// callerKey is unexported so external packages cannot overwrite the value.
type callerKey struct{}
