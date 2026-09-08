package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers/mock"
	"github.com/giantswarm/mcp-oauth/providers/oidc"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-timescale/internal/config"
	"github.com/giantswarm/mcp-timescale/internal/server"
	"github.com/giantswarm/mcp-timescale/internal/timescale"
	"github.com/giantswarm/mcp-timescale/internal/tools"
)

const (
	forwardedIssuer   = "https://dex.test.example"
	forwardedAudience = "mcp-timescale"
	forwardedEmail    = "user@test.example"
	msgAudit          = "tool call"
	msgMetadataMiss   = "Failed to retrieve token metadata"
)

// forwardedIDToken mints an RS256 ID token the way an SSO issuer (Dex) would,
// serves its public key from a TLS JWKS endpoint, and returns the token with
// a mock provider + CA pool that let mcp-oauth validate it as a forwarded
// (TrustedAudiences) bearer — the shape muster's auth.forwardToken produces.
func forwardedIDToken(t *testing.T) (string, *mock.Provider, *x509.CertPool) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const keyID = "test-key"
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: keyID, Algorithm: "RS256", Use: "sig"}}}
	jwksServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(jwksServer.Close)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(jwksServer.Certificate())

	provider := mock.NewProvider()
	provider.JWKSURIFunc = func(context.Context) (string, error) { return jwksServer.URL, nil }
	provider.IssuerURLFunc = func() string { return forwardedIssuer }

	opts := (&jose.SignerOptions{}).WithType("JWT")
	opts.WithHeader(jose.HeaderKey("kid"), keyID)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: keyID, Algorithm: "RS256", Use: "sig"}}, opts)
	if err != nil {
		t.Fatalf("jose.NewSigner: %v", err)
	}
	now := time.Now()
	token, err := josejwt.Signed(signer).Claims(oidc.IDTokenClaims{
		Claims: josejwt.Claims{
			Subject:  "user-subject-123",
			Issuer:   forwardedIssuer,
			Audience: josejwt.Audience{forwardedAudience},
			IssuedAt: josejwt.NewNumericDate(now),
			Expiry:   josejwt.NewNumericDate(now.Add(time.Hour)),
		},
		Email:         forwardedEmail,
		EmailVerified: true,
		Name:          "Test User",
	}).Serialize()
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token, provider, rootCAs
}

// newForwardedTokenServer stands up the real HTTP stack — mcp-oauth's
// ValidateToken gate, BuildMCPMux, the streamable-HTTP transport and the
// tools — with the memory storage backend and a logger capturing INFO+, and
// returns an MCP client that presents the forwarded token on every request.
func newForwardedTokenServer(t *testing.T) (*client.Client, *bytes.Buffer) {
	t.Helper()

	token, provider, rootCAs := forwardedIDToken(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	store := memory.New()
	t.Cleanup(store.Stop)
	oauthSrv, err := oauth.NewServerWithCombined(provider, store, &oauth.ServerConfig{
		Issuer:             forwardedIssuer,
		TrustedAudiences:   []string{forwardedAudience},
		AllowPrivateIPJWKS: true,
		JWKSRootCAs:        rootCAs,
	}, logger)
	if err != nil {
		t.Fatalf("oauth.NewServerWithCombined: %v", err)
	}
	auth := &server.Auth{Server: oauthSrv, Handler: handler.New(oauthSrv, logger)}

	reg, err := timescale.NewRegistry([]config.Database{{
		Name: "open", Host: "127.0.0.1", Port: 1, DBName: "x", SSLMode: "disable", User: "u", Password: "topsecret",
		MaxRows: 500, StatementTimeout: 30 * time.Second, MaxConnections: 1,
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(reg.Close)

	m := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(false), mcpsrv.WithStrictInputSchemaDefault())
	tools.Register(m, tools.Deps{Registry: reg, Log: logger})

	ts := httptest.NewServer(server.BuildMCPMux(server.TransportStreamableHTTP, m, auth))
	t.Cleanup(ts.Close)

	cli, err := client.NewStreamableHttpClient(ts.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	// Construction-time configuration warnings (AllowPrivateIPJWKS, memory
	// backend) are not per-call noise; only what the requests log matters.
	buf.Reset()
	return cli, &buf
}

func logEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// TestForwardedToken_OneAuditLinePerCall pins issue #5: a tool call carrying
// an SSO-forwarded id_token (muster auth.forwardToken) must log exactly one
// INFO line — this server's structured audit line, attributed to the
// forwarded identity — and nothing from mcp-oauth's token-metadata lookup,
// which has nothing to find for a bearer that was never stored.
func TestForwardedToken_OneAuditLinePerCall(t *testing.T) {
	cli, buf := newForwardedTokenServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := cli.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	const calls = 3
	for i := range calls {
		res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "timescale_list_databases"}})
		if err != nil {
			t.Fatalf("CallTool #%d: %v", i+1, err)
		}
		if res.IsError {
			t.Fatalf("CallTool #%d returned a tool error: %v", i+1, res.Content)
		}
	}

	var audit int
	for _, e := range logEntries(t, buf) {
		switch e["msg"] {
		case msgMetadataMiss:
			t.Fatalf("mcp-oauth logged %q for a forwarded token:\n%s", msgMetadataMiss, buf.String())
		case msgAudit:
			audit++
			if e["level"] != "INFO" {
				t.Errorf("audit line level = %v, want INFO", e["level"])
			}
			if e["caller"] != forwardedEmail {
				t.Errorf("audit line caller = %v, want %q (the forwarded identity)", e["caller"], forwardedEmail)
			}
			if e["tool"] != "timescale_list_databases" {
				t.Errorf("audit line tool = %v, want timescale_list_databases", e["tool"])
			}
		default:
			t.Errorf("unexpected %v log line besides the audit line: %v", e["level"], e)
		}
	}
	if audit != calls {
		t.Errorf("audit lines = %d, want exactly one per call (%d)", audit, calls)
	}
}
