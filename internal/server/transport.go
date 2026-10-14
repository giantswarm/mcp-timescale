package server

import (
	"net/http"

	"github.com/giantswarm/mcp-oauth/handler"
	mcpsrv "github.com/mark3labs/mcp-go/v2/server"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Transport constants. mcp-go does not export these.
const (
	TransportStdio          = "stdio"
	TransportSSE            = "sse"
	TransportStreamableHTTP = "streamable-http"
)

// MaxBodyBytes bounds a request body. mcp-go reads a POST body whole before
// validating it (CVE-2026-108859); the bound keeps that read small.
const MaxBodyBytes = 1 << 20

// BuildMCPMux wires the OAuth flow + discovery routes (when auth != nil) and
// the transport-specific MCP handler, then wraps the result in otelhttp so
// inbound W3C traceparents become server spans. Every request body is bounded
// at MaxBodyBytes. transport must be SSE or
// streamable-HTTP — stdio servers do not have an HTTP mux.
func BuildMCPMux(transport string, mcp *mcpsrv.MCPServer, auth *Auth) http.Handler {
	mux := http.NewServeMux()

	if auth != nil {
		resourcePath := "/mcp"
		if transport == TransportSSE {
			resourcePath = "/sse"
		}
		auth.Handler.RegisterOAuthRoutes(mux, handler.OAuthRoutesOptions{
			MCPPath:         resourcePath,
			IncludeMetadata: true,
		})
	}

	switch transport {
	case TransportStreamableHTTP:
		h := mcpsrv.NewStreamableHTTPServer(
			mcp,
			mcpsrv.WithEndpointPath("/mcp"),
			mcpsrv.WithHTTPContextFunc(PromoteOAuthCaller),
		)
		mux.Handle("/mcp", gateWithAuth(auth, h))
	case TransportSSE:
		h := mcpsrv.NewSSEServer(
			mcp,
			mcpsrv.WithSSEEndpoint("/sse"),
			mcpsrv.WithMessageEndpoint("/message"),
			mcpsrv.WithSSEContextFunc(PromoteOAuthCaller),
		)
		mux.Handle("/sse", gateWithAuth(auth, h))
		mux.Handle("/message", gateWithAuth(auth, h))
	}

	return otelhttp.NewHandler(http.MaxBytesHandler(mux, MaxBodyBytes), "mcp",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

// gateWithAuth wraps h with mcp-oauth's ValidateToken middleware when auth
// is non-nil; otherwise returns h unchanged.
func gateWithAuth(auth *Auth, h http.Handler) http.Handler {
	if auth == nil {
		return h
	}
	return auth.Handler.ValidateToken(h)
}
