package server

import (
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/stretchr/testify/require"
)

const testOwnIssuer = "https://mcp.example/"

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

func TestAllowPrivateIPJWKS_ServerConfig(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		preset bool
		allow  bool
		want   bool
	}{
		{name: "off keeps the JWKS guard"},
		{name: "on lifts the JWKS guard", allow: true, want: true},
		{name: "off preserves an explicit upstream policy", preset: true, want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := &oauth.ServerConfig{Issuer: testOwnIssuer, TrustedAudiences: []string{"aud"}, AllowPrivateIPJWKS: testCase.preset}
			allowPrivateIPJWKS(configuration, testCase.allow)
			require.Equal(t, testCase.want, configuration.AllowPrivateIPJWKS)
			require.Equal(t, testOwnIssuer, configuration.Issuer)
			require.Equal(t, []string{"aud"}, configuration.TrustedAudiences)
			require.False(t, configuration.AllowPrivateIPClientMetadata)
			require.Nil(t, configuration.JWKSRootCAs)
		})
	}
}
