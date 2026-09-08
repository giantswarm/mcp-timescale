package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/giantswarm/mcp-oauth/providers/mock"
	"github.com/giantswarm/mcp-oauth/providers/oidc"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

const (
	privateIssuer   = "https://dex.private.example"
	privateAudience = "dex-k8s-authenticator"
	privateEmail    = "analyst@private.example"
	msgTokenRefused = "Token validation failed"
	// mcp-oauth's WARN when a token with a trusted audience fails JWKS
	// validation; its error field carries the SSRF guard's reason.
	msgForwardedFailed = "Forwarded ID token validation failed, falling back to userinfo"
)

// localhostTLSServer serves h over TLS under the name "localhost", the way a
// Dex behind an internal load balancer is reached: by a hostname that
// resolves to a private or loopback address. httptest's own certificate
// carries only IP SANs, and an IP-literal URL is refused by URL validation
// regardless of the flag, so the guard under test is the resolve-time one.
// Returns the server's https://localhost:<port> base URL and a pool trusting
// its certificate.
func localhostTLSServer(t *testing.T, h http.Handler) (string, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return "https://localhost:" + port, pool
}

// privateJWKS publishes an RS256 signing key at https://localhost:<port>/keys
// and mints the ID token an SSO issuer would hand muster for a user — the
// forwarded-token shape this server accepts through OAUTH_TRUSTED_AUDIENCES.
func privateJWKS(t *testing.T) (token, jwksURL string, pool *x509.CertPool) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const keyID = "private-key"
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: keyID, Algorithm: "RS256", Use: "sig"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	base, pool := localhostTLSServer(t, mux)

	opts := (&jose.SignerOptions{}).WithType("JWT")
	opts.WithHeader(jose.HeaderKey("kid"), keyID)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: keyID, Algorithm: "RS256", Use: "sig"}}, opts)
	if err != nil {
		t.Fatalf("jose.NewSigner: %v", err)
	}
	now := time.Now()
	token, err = josejwt.Signed(signer).Claims(oidc.IDTokenClaims{
		Claims: josejwt.Claims{
			Subject:  "private-subject-1",
			Issuer:   privateIssuer,
			Audience: josejwt.Audience{privateAudience},
			IssuedAt: josejwt.NewNumericDate(now),
			Expiry:   josejwt.NewNumericDate(now.Add(time.Hour)),
		},
		Email:         privateEmail,
		EmailVerified: true,
	}).Serialize()
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token, base + "/keys", pool
}

// newPrivateJWKSServer stands up the real HTTP stack — mcp-oauth's
// ValidateToken gate, BuildMCPMux, the streamable-HTTP transport and a tool
// reporting the caller — with a server configuration shaped like
// oauthconfig.FromEnv's for a forwarded-token deployment, then applies the
// flag the way NewAuth does. JWKSRootCAs trusts only the test certificate, so
// the flag is the single difference between the two cases.
func newPrivateJWKSServer(t *testing.T, allow bool) (*client.Client, *bytes.Buffer) {
	t.Helper()

	token, jwksURL, pool := privateJWKS(t)
	provider := mock.NewProvider()
	provider.JWKSURIFunc = func(context.Context) (string, error) { return jwksURL, nil }
	provider.IssuerURLFunc = func() string { return privateIssuer }
	// After a failed JWKS validation mcp-oauth falls back to the provider's
	// userinfo endpoint; a real Dex answers an ID token there with 400, the
	// mock would invent a user. Refuse like Dex does.
	provider.ValidateTokenFunc = func(context.Context, string) (*providers.UserInfo, error) {
		return nil, errors.New("userinfo request failed with status 400")
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store := memory.New()
	t.Cleanup(store.Stop)

	cfg := &oauth.ServerConfig{
		Issuer:           "https://mcp.test.example",
		TrustedAudiences: []string{privateAudience},
		JWKSRootCAs:      pool,
	}
	allowPrivateURLs(cfg, allow)
	srv, err := oauth.NewServerWithCombined(provider, store, cfg, logger)
	if err != nil {
		t.Fatalf("oauth.NewServerWithCombined: %v", err)
	}
	auth := &Auth{Server: srv, Handler: handler.New(srv, logger)}

	m := mcpsrv.NewMCPServer("test", "0", mcpsrv.WithToolCapabilities(false))
	m.AddTool(mcp.NewTool("whoami"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, _ := CallerFromContext(ctx)
		return mcp.NewToolResultText(c.Email), nil
	})
	ts := httptest.NewServer(BuildMCPMux(TransportStreamableHTTP, m, auth))
	t.Cleanup(ts.Close)

	cli, err := client.NewStreamableHttpClient(ts.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	buf.Reset()
	return cli, &buf
}

func jsonLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
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

// TestForwardedToken_PrivateJWKS pins issue #14: the JWKS of an identity
// provider reached through a hostname that resolves to a private or loopback
// address is refused by mcp-oauth's SSRF guard, so a valid forwarded ID token
// with a trusted audience never yields a caller. OAUTH_ALLOW_PRIVATE_URLS
// lifts that guard and the same token is accepted, attributed to the
// forwarded identity.
func TestForwardedToken_PrivateJWKS(t *testing.T) {
	t.Run("default refuses the token: its JWKS resolves to a loopback address", func(t *testing.T) {
		cli, buf := newPrivateJWKSServer(t, false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := cli.Start(ctx)
		if err == nil {
			_, err = cli.Initialize(ctx, mcp.InitializeRequest{})
		}
		if err == nil {
			t.Fatalf("the session came up; want the 401 the SSRF guard causes. Log:\n%s", buf.String())
		}
		// mcp-go reports the 401 as "authorization required".
		if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "authorization required") {
			t.Fatalf("error = %v, want the 401 refusal", err)
		}
		var guarded, refused bool
		for _, e := range jsonLogLines(t, buf) {
			switch e["msg"] {
			case msgForwardedFailed:
				guarded = guarded || strings.Contains(fmt.Sprint(e["error"]), "restricted IP")
			case msgTokenRefused:
				refused = true
			}
		}
		if !guarded {
			t.Errorf("no %q line naming the restricted IP in:\n%s", msgForwardedFailed, buf.String())
		}
		if !refused {
			t.Errorf("no %q line in:\n%s", msgTokenRefused, buf.String())
		}
	})

	t.Run("OAUTH_ALLOW_PRIVATE_URLS accepts the same token", func(t *testing.T) {
		cli, buf := newPrivateJWKSServer(t, true)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := cli.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "whoami"}})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if len(res.Content) != 1 {
			t.Fatalf("content = %v, want one text item", res.Content)
		}
		text, ok := res.Content[0].(mcp.TextContent)
		if !ok || text.Text != privateEmail {
			t.Errorf("caller = %v, want %q (the forwarded identity)", res.Content[0], privateEmail)
		}
		for _, e := range jsonLogLines(t, buf) {
			if e["msg"] == msgTokenRefused || e["msg"] == msgForwardedFailed {
				t.Errorf("token refused with the flag on: %v", e)
			}
		}
	})
}
