package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
)

const (
	testDexIssuer   = "https://dex.example"
	testClientID    = "mcp-timescale"
	testSecret      = "from-env"
	testOwnIssuer   = "https://mcp.example/"
	testRedirectDef = "https://mcp.example/oauth/callback"
)

func TestAllowPrivateURLsFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		primary string
		alias   string
		want    bool
		wantErr string
	}{
		{name: "unset is off"},
		{name: "primary true", primary: "true", want: true},
		{name: "primary 1", primary: "1", want: true},
		{name: "alias true (mcp-capi / mcp-prometheus spelling)", alias: "true", want: true},
		{name: "primary false wins over alias true", primary: "false", alias: "true"},
		{name: "primary that is not a bool fails startup", primary: "yes", wantErr: EnvAllowPrivateURLs},
		{name: "alias that is not a bool fails startup", alias: "nope", wantErr: EnvAllowPrivateURLsAlias},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvAllowPrivateURLs, tt.primary)
			t.Setenv(EnvAllowPrivateURLsAlias, tt.alias)

			got, err := AllowPrivateURLsFromEnv()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllowPrivateURLs_ServerConfig(t *testing.T) {
	tests := []struct {
		name  string
		pre   bool
		allow bool
		want  bool
	}{
		{name: "off leaves the JWKS guard on", pre: false, allow: false, want: false},
		{name: "on lifts the JWKS guard", pre: false, allow: true, want: true},
		{name: "off leaves a preset value alone", pre: true, allow: false, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &oauth.ServerConfig{Issuer: testOwnIssuer, TrustedAudiences: []string{"aud"}, AllowPrivateIPJWKS: tt.pre}

			allowPrivateURLs(cfg, tt.allow)

			if cfg.AllowPrivateIPJWKS != tt.want {
				t.Errorf("AllowPrivateIPJWKS = %v, want %v", cfg.AllowPrivateIPJWKS, tt.want)
			}
			if cfg.Issuer != testOwnIssuer || len(cfg.TrustedAudiences) != 1 || cfg.AllowPrivateIPClientMetadata || cfg.JWKSRootCAs != nil {
				t.Errorf("fields besides AllowPrivateIPJWKS changed: %+v", cfg)
			}
		})
	}
}

// dexFields is the part of dex.Config the environment decides.
type dexFields struct {
	Issuer, ClientID, Secret, Redirect, Connector string
}

func TestDexConfigFromEnv(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		envDexIssuerURL:    testDexIssuer,
		envDexClientID:     testClientID,
		envDexClientSecret: testSecret,
		envOAuthIssuer:     testOwnIssuer,
	}
	optional := []string{envDexRedirectURL, envDexConnectorID, envDexClientSecret + "_FILE"}

	tests := []struct {
		name    string
		env     map[string]string // laid over base; "" unsets
		want    dexFields
		wantErr string
	}{
		{
			name: "redirect derived from this server's issuer, secret from the environment",
			want: dexFields{Issuer: testDexIssuer, ClientID: testClientID, Secret: testSecret, Redirect: testRedirectDef},
		},
		{
			name: "explicit redirect URL and connector",
			env:  map[string]string{envDexRedirectURL: "https://other.example/cb", envDexConnectorID: "github"},
			want: dexFields{Issuer: testDexIssuer, ClientID: testClientID, Secret: testSecret, Redirect: "https://other.example/cb", Connector: "github"},
		},
		{
			name: "secret file wins over the environment and loses its trailing newline",
			env:  map[string]string{envDexClientSecret + "_FILE": secretFile},
			want: dexFields{Issuer: testDexIssuer, ClientID: testClientID, Secret: "from-file", Redirect: testRedirectDef},
		},
		{name: "Dex issuer required", env: map[string]string{envDexIssuerURL: ""}, wantErr: envDexIssuerURL},
		{name: "client ID required", env: map[string]string{envDexClientID: ""}, wantErr: envDexClientID},
		{name: "client secret required", env: map[string]string{envDexClientSecret: ""}, wantErr: envDexClientSecret + " (or " + envDexClientSecret + "_FILE)"},
		{name: "redirect URL required when there is no issuer to derive it from", env: map[string]string{envOAuthIssuer: ""}, wantErr: envDexRedirectURL},
		{name: "unreadable secret file", env: map[string]string{envDexClientSecret + "_FILE": filepath.Join(t.TempDir(), "missing")}, wantErr: envDexClientSecret + "_FILE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range base {
				t.Setenv(k, v)
			}
			for _, k := range optional {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := dexConfigFromEnv()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotFields := dexFields{Issuer: got.IssuerURL, ClientID: got.ClientID, Secret: got.ClientSecret, Redirect: got.RedirectURL, Connector: got.ConnectorID}
			if gotFields != tt.want {
				t.Errorf("got %+v, want %+v", gotFields, tt.want)
			}
			if got.AllowPrivateIP || len(got.Scopes) != 0 || got.HTTPClient != nil || got.RequestTimeout != 0 {
				t.Errorf("loader must leave the posture and the Dex defaults to newProvider: %+v", got)
			}
		})
	}
}

// TestNewProvider_Dispatch pins which loader answers, without a network: the
// error each loader returns for a missing variable tells them apart, and the
// Dex variable set is the same on both paths.
func TestNewProvider_Dispatch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name     string
		provider string
		allow    bool
		wantErr  string
	}{
		{name: "off: oauthconfig decides", provider: "", allow: false, wantErr: "OAUTH_PROVIDER is required"},
		{name: "off with dex: oauthconfig's Dex loader", provider: providerDex, allow: false, wantErr: envDexIssuerURL},
		{name: "on with dex: this package's Dex loader, same variables", provider: providerDex, allow: true, wantErr: envDexIssuerURL},
		{name: "on with google: nothing to lift, oauthconfig's loader", provider: "google", allow: true, wantErr: "OAUTH_GOOGLE_CLIENT_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envOAuthProvider, tt.provider)
			for _, k := range []string{envDexIssuerURL, envDexClientID, envDexClientSecret, "OAUTH_GOOGLE_CLIENT_ID"} {
				t.Setenv(k, "")
			}

			_, err := newProvider(logger, tt.allow)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
