package server

import (
	"fmt"
	"os"
	"strconv"

	oauth "github.com/giantswarm/mcp-oauth"
)

const EnvAllowPrivateIPJWKS = "OAUTH_ALLOW_PRIVATE_IP_JWKS"

// AllowPrivateIPJWKSFromEnv reads the JWKS address policy, rejecting obsolete settings.
func AllowPrivateIPJWKSFromEnv() (bool, error) {
	for _, name := range []string{"OAUTH_ALLOW_PRIVATE_URLS", "MCP_OAUTH_ALLOW_PRIVATE_URLS"} {
		if raw := os.Getenv(name); raw != "" {
			obsolete, err := strconv.ParseBool(raw)
			if err != nil {
				return false, fmt.Errorf("%s: expected a boolean: %w", name, err)
			}
			if obsolete {
				return false, fmt.Errorf("%s is obsolete; remove it; for private-address JWKS fetching use %s=true; Dex discovery needs no setting", name, EnvAllowPrivateIPJWKS)
			}
		}
	}
	return EnvBool(EnvAllowPrivateIPJWKS, false)
}

// allowPrivateIPJWKS applies the flag to the mcp-oauth server configuration:
// the JWKS client that validates forwarded ID tokens may then resolve to a
// private or loopback address. Off leaves cfg exactly as oauthconfig built it.
func allowPrivateIPJWKS(configuration *oauth.ServerConfig, allow bool) {
	if allow {
		configuration.AllowPrivateIPJWKS = true
	}
}
