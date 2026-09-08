package server

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/giantswarm/mcp-oauth/providers/dex"
)

// Identity providers on private addresses.
//
// mcp-oauth guards its outbound OIDC fetches against SSRF: a URL whose host
// resolves to a private (RFC 1918), loopback or link-local address is
// refused. On an installation whose Dex sits behind an internal-only load
// balancer the Dex hostname resolves to exactly such an address, so the JWKS
// fetch that validates forwarded ID tokens (OAUTH_TRUSTED_AUDIENCES, muster
// auth.mode forward) fails and every call is refused for lack of a caller
// identity. OAUTH_ALLOW_PRIVATE_URLS lifts the guard for the two
// operator-configured endpoints only — the Dex issuer (discovery, token
// endpoint) and its JWKS; TLS verification stays on. mcp-capi and
// mcp-prometheus name the same knob MCP_OAUTH_ALLOW_PRIVATE_URLS, accepted
// here as an alias.
const (
	EnvAllowPrivateURLs      = "OAUTH_ALLOW_PRIVATE_URLS"
	EnvAllowPrivateURLsAlias = "MCP_OAUTH_ALLOW_PRIVATE_URLS"

	envOAuthProvider       = "OAUTH_PROVIDER"
	envOAuthIssuer         = "OAUTH_ISSUER"
	envDexIssuerURL        = "OAUTH_DEX_ISSUER_URL"
	envDexClientID         = "OAUTH_DEX_CLIENT_ID"
	envDexClientSecret     = "OAUTH_DEX_CLIENT_SECRET" //nolint:gosec // the variable's name, not a credential
	envDexRedirectURL      = "OAUTH_DEX_REDIRECT_URL"
	envDexConnectorID      = "OAUTH_DEX_CONNECTOR_ID"
	providerDex            = "dex"
	dexDefaultCallbackPath = "/oauth/callback"
)

// AllowPrivateURLsFromEnv reads OAUTH_ALLOW_PRIVATE_URLS and, when that is
// unset, MCP_OAUTH_ALLOW_PRIVATE_URLS. Unset means false; a value that is not
// a bool fails startup so a typo cannot silently keep the guard on or off.
func AllowPrivateURLsFromEnv() (bool, error) {
	if os.Getenv(EnvAllowPrivateURLs) != "" {
		return EnvBool(EnvAllowPrivateURLs, false)
	}
	return EnvBool(EnvAllowPrivateURLsAlias, false)
}

// allowPrivateURLs applies the flag to the mcp-oauth server configuration:
// the JWKS client that validates forwarded ID tokens may then resolve to a
// private or loopback address. Off leaves cfg exactly as oauthconfig built it.
func allowPrivateURLs(cfg *oauth.ServerConfig, allow bool) {
	if allow {
		cfg.AllowPrivateIPJWKS = true
	}
}

// newProvider returns the configured identity provider. With the flag off it
// is oauthconfig.ProviderFromEnv(), unchanged. With the flag on and
// OAUTH_PROVIDER=dex the Dex provider is built here with AllowPrivateIP — the
// escape hatch oauthconfig documents for this case — from the same
// OAUTH_DEX_* variables, so a deployment sets nothing else. Google and GitHub
// serve their endpoints publicly; for them the flag only affects the JWKS
// setting and the provider comes from oauthconfig as before.
func newProvider(logger *slog.Logger, allowPrivate bool) (providers.Provider, error) {
	if !allowPrivate || os.Getenv(envOAuthProvider) != providerDex {
		return oauthconfig.ProviderFromEnv()
	}
	cfg, err := dexConfigFromEnv()
	if err != nil {
		return nil, err
	}
	cfg.AllowPrivateIP = true
	cfg.Logger = logger
	return dex.NewProvider(cfg)
}

// dexConfigFromEnv reads the variable set oauthconfig.DexFromEnv reads —
// OAUTH_DEX_ISSUER_URL, OAUTH_DEX_CLIENT_ID, OAUTH_DEX_CLIENT_SECRET[_FILE],
// OAUTH_DEX_REDIRECT_URL (default "${OAUTH_ISSUER}/oauth/callback") and
// OAUTH_DEX_CONNECTOR_ID — with the same required/optional split and error
// wording, and leaves scopes and timeouts at the Dex defaults as oauthconfig
// does.
func dexConfigFromEnv() (*dex.Config, error) {
	issuer, err := requireEnv(envDexIssuerURL)
	if err != nil {
		return nil, err
	}
	clientID, err := requireEnv(envDexClientID)
	if err != nil {
		return nil, err
	}
	redirectURL, err := dexRedirectURL()
	if err != nil {
		return nil, err
	}
	clientSecret, err := requireSecretEnv(envDexClientSecret)
	if err != nil {
		return nil, err
	}
	return &dex.Config{
		IssuerURL:    issuer,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		ConnectorID:  os.Getenv(envDexConnectorID),
	}, nil
}

// dexRedirectURL is OAUTH_DEX_REDIRECT_URL when set, otherwise this server's
// own OAUTH_ISSUER plus mcp-oauth's provider-callback path (a trailing slash
// on the issuer is tolerated); with neither set the redirect URL is reported
// as the missing variable.
func dexRedirectURL() (string, error) {
	if v := os.Getenv(envDexRedirectURL); v != "" {
		return v, nil
	}
	if issuer := os.Getenv(envOAuthIssuer); issuer != "" {
		return strings.TrimRight(issuer, "/") + dexDefaultCallbackPath, nil
	}
	return requireEnv(envDexRedirectURL)
}

func requireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", name)
	}
	return v, nil
}

// requireSecretEnv resolves NAME_FILE (the file's contents without a trailing
// newline) before NAME, the convention oauthconfig applies to every secret.
func requireSecretEnv(name string) (string, error) {
	v := os.Getenv(name)
	if raw := os.Getenv(name + "_FILE"); raw != "" {
		b, err := os.ReadFile(filepath.Clean(raw))
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		v = strings.TrimSuffix(string(b), "\n")
	}
	if v == "" {
		return "", fmt.Errorf("required environment variable %s (or %s_FILE) is not set", name, name)
	}
	return v, nil
}
