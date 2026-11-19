package server

import (
	"fmt"
	"os"
	"strconv"
)

const EnvAllowPrivateIPJWKS = "OAUTH_ALLOW_PRIVATE_IP_JWKS"

// AllowPrivateIPJWKSFromEnv reads the deprecated JWKS address setting, which has
// no effect, and rejects obsolete settings.
func AllowPrivateIPJWKSFromEnv() (bool, error) {
	for _, name := range []string{"OAUTH_ALLOW_PRIVATE_URLS", "MCP_OAUTH_ALLOW_PRIVATE_URLS"} {
		if raw := os.Getenv(name); raw != "" {
			obsolete, err := strconv.ParseBool(raw)
			if err != nil {
				return false, fmt.Errorf("%s: expected a boolean: %w", name, err)
			}
			if obsolete {
				return false, fmt.Errorf("%s is obsolete; remove it; Dex discovery and the issuer host's JWKS need no setting", name)
			}
		}
	}
	return EnvBool(EnvAllowPrivateIPJWKS, false)
}
