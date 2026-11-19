package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateIPJWKSFromEnv(t *testing.T) {
	const canonicalName = "OAUTH_ALLOW_PRIVATE_IP_JWKS"
	const obsoleteName = "OAUTH_ALLOW_PRIVATE_URLS"
	const aliasName = "MCP_OAUTH_ALLOW_PRIVATE_URLS"

	tests := []struct {
		name      string
		canonical string
		obsolete  string
		alias     string
		want      bool
		wantError string
	}{
		{name: "unset is off"},
		{name: "canonical true", canonical: "true", want: true},
		{name: "canonical 1", canonical: "1", want: true},
		{name: "canonical false", canonical: "false"},
		{name: "canonical malformed", canonical: "yes", wantError: canonicalName},
		{name: "obsolete false", obsolete: "false"},
		{name: "obsolete true", obsolete: "true", wantError: obsoleteName + " is obsolete"},
		{name: "obsolete true cannot be shadowed", canonical: "false", obsolete: "true", wantError: obsoleteName + " is obsolete"},
		{name: "obsolete malformed", canonical: "false", obsolete: "yes", wantError: obsoleteName},
		{name: "alias false", alias: "false"},
		{name: "alias true cannot be shadowed", canonical: "false", obsolete: "false", alias: "true", wantError: aliasName + " is obsolete"},
		{name: "alias malformed cannot be shadowed", canonical: "false", obsolete: "false", alias: "nope", wantError: aliasName},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(canonicalName, testCase.canonical)
			t.Setenv(obsoleteName, testCase.obsolete)
			t.Setenv(aliasName, testCase.alias)
			got, err := AllowPrivateIPJWKSFromEnv()
			if testCase.wantError != "" {
				require.ErrorContains(t, err, testCase.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}
}

func TestNewAuth_PrivateIPJWKSIsInert(t *testing.T) {
	t.Setenv("OAUTH_PROVIDER", "google")
	t.Setenv("OAUTH_ISSUER", "https://mcp-timescale.example.com")
	t.Setenv("OAUTH_GOOGLE_CLIENT_ID", "mcp-timescale")
	t.Setenv("OAUTH_GOOGLE_CLIENT_SECRET", "secret")
	t.Setenv("OAUTH_GOOGLE_REDIRECT_URL", "https://mcp-timescale.example.com/oauth/callback")

	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			t.Setenv(EnvAllowPrivateIPJWKS, strconv.FormatBool(enabled))
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))

			auth, err := NewAuth(t.Context(), logger)
			require.NoError(t, err)
			t.Cleanup(func() { _ = auth.Shutdown(t.Context()) })

			require.False(t, auth.Server.Config.AllowPrivateIPJWKS) //nolint:staticcheck // SA1019: asserting the deprecated field stays unset
			require.Equal(t, enabled, strings.Contains(buf.String(), EnvAllowPrivateIPJWKS+" (Helm oauth.allowPrivateIPJWKS) has no effect"))
		})
	}
}
