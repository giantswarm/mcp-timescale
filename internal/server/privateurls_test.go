package server

import (
	"strings"
	"testing"

	oauth "github.com/giantswarm/mcp-oauth"
)

const testOwnIssuer = "https://mcp.example/"

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
