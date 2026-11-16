package server

import (
	"os"

	oauth "github.com/giantswarm/mcp-oauth"
)

// Identity providers on private addresses.
//
// mcp-oauth guards the JWKS fetch that validates forwarded ID tokens
// (OAUTH_TRUSTED_AUDIENCES, muster auth.mode forward) against SSRF: a JWKS
// URL whose host resolves to a private (RFC 1918), loopback or link-local
// address is refused. On an installation whose Dex sits behind an
// internal-only load balancer the Dex hostname resolves to exactly such an
// address, so every forwarded token is refused for lack of a caller identity.
// OAUTH_ALLOW_PRIVATE_URLS lifts the guard for that JWKS fetch only; TLS
// verification stays on. The Dex client itself (discovery, token endpoint)
// needs no setting: mcp-oauth's default Dex client reaches private addresses
// and follows HTTP_PROXY, HTTPS_PROXY and NO_PROXY. mcp-capi and
// mcp-prometheus name the same knob MCP_OAUTH_ALLOW_PRIVATE_URLS, accepted
// here as an alias.
const (
	EnvAllowPrivateURLs      = "OAUTH_ALLOW_PRIVATE_URLS"
	EnvAllowPrivateURLsAlias = "MCP_OAUTH_ALLOW_PRIVATE_URLS"
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
